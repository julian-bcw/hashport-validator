/*
 * Copyright 2021 LimeChain Ltd.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package cryptotransfer

import (
	"errors"
	"fmt"
	"github.com/hashgraph/hedera-sdk-go/v2"
	"github.com/limechain/hedera-eth-bridge-validator/app/clients/hedera/mirror-node"
	"github.com/limechain/hedera-eth-bridge-validator/app/core/queue"
	"github.com/limechain/hedera-eth-bridge-validator/app/domain/client"
	qi "github.com/limechain/hedera-eth-bridge-validator/app/domain/queue"
	"github.com/limechain/hedera-eth-bridge-validator/app/domain/repository"
	"github.com/limechain/hedera-eth-bridge-validator/app/domain/service"
	"github.com/limechain/hedera-eth-bridge-validator/app/helper/timestamp"
	"github.com/limechain/hedera-eth-bridge-validator/app/model/transfer"
	"github.com/limechain/hedera-eth-bridge-validator/config"
	"github.com/limechain/hedera-eth-bridge-validator/constants"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"time"
)

type Watcher struct {
	transfers        service.Transfers
	client           client.MirrorNode
	accountID        hedera.AccountID
	pollingInterval  time.Duration
	statusRepository repository.Status
	startTimestamp   int64
	logger           *log.Entry
	contractServices map[int64]service.Contracts
	mappings         config.Assets
	validator        bool
}

func NewWatcher(
	transfers service.Transfers,
	client client.MirrorNode,
	accountID string,
	pollingInterval time.Duration,
	repository repository.Status,
	startTimestamp int64,
	contractServices map[int64]service.Contracts,
	mappings config.Assets,
	validator bool,
) *Watcher {
	id, err := hedera.AccountIDFromString(accountID)
	if err != nil {
		log.Fatalf("Could not start Crypto Transfer Watcher for account [%s] - Error: [%s]", accountID, err)
	}

	return &Watcher{
		transfers:        transfers,
		client:           client,
		accountID:        id,
		pollingInterval:  pollingInterval,
		statusRepository: repository,
		startTimestamp:   startTimestamp,
		logger:           config.GetLoggerFor(fmt.Sprintf("[%s] Transfer Watcher", accountID)),
		contractServices: contractServices,
		mappings:         mappings,
		validator:        validator,
	}
}

func (ctw Watcher) Watch(q qi.Queue) {
	if !ctw.client.AccountExists(ctw.accountID) {
		ctw.logger.Errorf("Could not start monitoring account [%s] - Account not found.", ctw.accountID.String())
		return
	}

	account := ctw.accountID.String()
	_, err := ctw.statusRepository.GetLastFetchedTimestamp(account)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err := ctw.statusRepository.CreateTimestamp(account, ctw.startTimestamp)
			if err != nil {
				ctw.logger.Fatalf("Failed to create Transfer Watcher timestamp. Error [%s]", err)
			}
			ctw.logger.Tracef("Created new Transfer Watcher timestamp [%s]", timestamp.ToHumanReadable(ctw.startTimestamp))
		} else {
			ctw.logger.Fatalf("Failed to fetch last Transfer Watcher timestamp. Error: [%s]", err)
		}
	} else {
		ctw.updateStatusTimestamp(ctw.startTimestamp)
	}

	go ctw.beginWatching(q)
	ctw.logger.Infof("Watching for Transfers after Timestamp [%s]", timestamp.ToHumanReadable(ctw.startTimestamp))
}

func (ctw Watcher) updateStatusTimestamp(ts int64) {
	err := ctw.statusRepository.UpdateLastFetchedTimestamp(ctw.accountID.String(), ts)
	if err != nil {
		ctw.logger.Fatalf("Failed to update Transfer Watcher Status timestamp. Error [%s]", err)
	}
	ctw.logger.Tracef("Updated Transfer Watcher timestamp to [%s]", timestamp.ToHumanReadable(ts))
}

func (ctw Watcher) beginWatching(q qi.Queue) {
	milestoneTimestamp, err := ctw.statusRepository.GetLastFetchedTimestamp(ctw.accountID.String())
	if err != nil {
		ctw.logger.Fatalf("Failed to retrieve Transfer Watcher Status timestamp. Error [%s]", err)
	}

	for {
		transactions, e := ctw.client.GetAccountCreditTransactionsAfterTimestamp(ctw.accountID, milestoneTimestamp)
		if e != nil {
			ctw.logger.Errorf("Suddenly stopped monitoring account - [%s]", e)
			go ctw.beginWatching(q)
			return
		}

		ctw.logger.Tracef("Polling found [%d] Transactions", len(transactions.Transactions))
		if len(transactions.Transactions) > 0 {
			for _, tx := range transactions.Transactions {
				go ctw.processTransaction(tx, q)
			}
			var err error
			milestoneTimestamp, err = timestamp.FromString(transactions.Transactions[len(transactions.Transactions)-1].ConsensusTimestamp)
			if err != nil {
				ctw.logger.Errorf("Unable to parse latest transfer timestamp. Error - [%s].", err)
				continue
			}

			ctw.updateStatusTimestamp(milestoneTimestamp)
		}
		time.Sleep(ctw.pollingInterval * time.Second)
	}
}

func (ctw Watcher) processTransaction(tx mirror_node.Transaction, q qi.Queue) {
	ctw.logger.Infof("New Transaction with ID: [%s]", tx.TransactionID)
	amount, asset, err := tx.GetIncomingTransfer(ctw.accountID.String())
	if err != nil {
		ctw.logger.Errorf("[%s] - Could not extract incoming transfer. Error: [%s]", tx.TransactionID, err)
		return
	}

	targetChainId, receiverAddress, err := ctw.transfers.SanityCheckTransfer(tx)
	if err != nil {
		ctw.logger.Errorf("[%s] - Sanity check failed. Error: [%s]", tx.TransactionID, err)
		return
	}

	nativeAsset := &config.NativeAsset{
		ChainId: 0,
		Asset:   asset,
	}
	targetChainAsset := ctw.mappings.NativeToWrapped(asset, 0, targetChainId)
	if targetChainAsset == "" {
		nativeAsset = ctw.mappings.WrappedToNative(asset, 0)
		if nativeAsset == nil {
			ctw.logger.Errorf("[%s] - Could not parse asset [%s] to its target chain correlation", tx.TransactionID, asset)
			return
		}
		targetChainAsset = nativeAsset.Asset
		if nativeAsset.ChainId != targetChainId {
			ctw.logger.Errorf("[%s] - Wrapped to Wrapped transfers currently not supported [%s] - [%d] for [%d]", tx.TransactionID, nativeAsset.Asset, nativeAsset.ChainId, targetChainId)
			return
		}
	}

	transferMessage := transfer.New(
		tx.TransactionID,
		0,
		targetChainId,
		nativeAsset.ChainId,
		receiverAddress,
		asset,
		targetChainAsset,
		nativeAsset.Asset,
		amount,
		ctw.contractServices[targetChainId].Address().String())

	// TODO: Extend for recoverability
	if ctw.validator {
		if nativeAsset.ChainId == 0 {
			transferMessage.HasFee = true
			q.Push(&queue.Message{Payload: transferMessage, Topic: constants.HederaTransferMessageSubmission})
		} else {
			q.Push(&queue.Message{Payload: transferMessage, Topic: constants.HederaBurnMessageSubmission})
		}
	} else {
		transferMessage.Timestamp = tx.ConsensusTimestamp
		if nativeAsset.ChainId == 0 {
			transferMessage.HasFee = true
			q.Push(&queue.Message{Payload: transferMessage, Topic: constants.ReadOnlyHederaFeeTransfer})
		} else {
			q.Push(&queue.Message{Payload: transferMessage, Topic: constants.ReadOnlyHederaBurn})
		}
	}
}

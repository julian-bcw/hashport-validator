/*
 * Copyright 2022 LimeChain Ltd.
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
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/limechain/hedera-eth-bridge-validator/app/process/payload"

	"github.com/hashgraph/hedera-sdk-go/v2"
	"github.com/limechain/hedera-eth-bridge-validator/app/clients/hedera/mirror-node/model/transaction"
	"github.com/limechain/hedera-eth-bridge-validator/app/core/queue"
	"github.com/limechain/hedera-eth-bridge-validator/app/domain/client"
	qi "github.com/limechain/hedera-eth-bridge-validator/app/domain/queue"
	"github.com/limechain/hedera-eth-bridge-validator/app/domain/repository"
	"github.com/limechain/hedera-eth-bridge-validator/app/domain/service"
	"github.com/limechain/hedera-eth-bridge-validator/app/helper/decimal"
	hederaHelper "github.com/limechain/hedera-eth-bridge-validator/app/helper/hedera"
	"github.com/limechain/hedera-eth-bridge-validator/app/helper/metrics"
	"github.com/limechain/hedera-eth-bridge-validator/app/helper/timestamp"
	"github.com/limechain/hedera-eth-bridge-validator/app/model/asset"
	"github.com/limechain/hedera-eth-bridge-validator/config"
	"github.com/limechain/hedera-eth-bridge-validator/constants"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Watcher struct {
	transfers         service.Transfers
	client            client.MirrorNode
	accountID         hedera.AccountID
	pollingInterval   time.Duration
	statusRepository  repository.Status
	targetTimestamp   int64
	logger            *log.Entry
	contractServices  map[uint64]service.Contracts
	assetsService     service.Assets
	validator         bool
	prometheusService service.Prometheus
	pricingService    service.Pricing
}

func NewWatcher(
	transfers service.Transfers,
	client client.MirrorNode,
	accountID string,
	pollingInterval time.Duration,
	repository repository.Status,
	startTimestamp int64,
	contractServices map[uint64]service.Contracts,
	assetsService service.Assets,
	validator bool,
	prometheusService service.Prometheus,
	pricingService service.Pricing,
) *Watcher {
	id, err := hedera.AccountIDFromString(accountID)
	if err != nil {
		log.Fatalf("Could not start Crypto Transfer Watcher for account [%s] - Error: [%s]", accountID, err)
	}

	targetTimestamp := time.Now().UnixNano()
	timeStamp := startTimestamp
	if startTimestamp == 0 {
		_, err := repository.Get(accountID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				err := repository.Create(accountID, targetTimestamp)
				if err != nil {
					log.Fatalf("Failed to create Transfer Watcher timestamp. Error: [%s]", err)
				}
				log.Tracef("Created new Transfer Watcher timestamp [%s]", timestamp.ToHumanReadable(targetTimestamp))
			} else {
				log.Fatalf("Failed to fetch last Transfer Watcher timestamp. Error: [%s]", err)
			}
		}
	} else {
		err := repository.Update(accountID, timeStamp)
		if err != nil {
			log.Fatalf("Failed to update Transfer Watcher Status timestamp. Error [%s]", err)
		}
		targetTimestamp = timeStamp
		log.Tracef("Updated Transfer Watcher timestamp to [%s]", timestamp.ToHumanReadable(timeStamp))
	}
	instance := &Watcher{
		transfers:         transfers,
		client:            client,
		accountID:         id,
		pollingInterval:   pollingInterval,
		statusRepository:  repository,
		targetTimestamp:   targetTimestamp,
		logger:            config.GetLoggerFor(fmt.Sprintf("[%s] Transfer Watcher", accountID)),
		contractServices:  contractServices,
		assetsService:     assetsService,
		validator:         validator,
		pricingService:    pricingService,
		prometheusService: prometheusService,
	}

	return instance

}

func (ctw Watcher) Watch(q qi.Queue) {
	if !ctw.client.AccountExists(ctw.accountID) {
		ctw.logger.Errorf("Could not start monitoring account [%s] - Account not found.", ctw.accountID.String())
		return
	}

	go ctw.beginWatching(q)
}

func (ctw Watcher) updateStatusTimestamp(ts int64) {
	err := ctw.statusRepository.Update(ctw.accountID.String(), ts)
	if err != nil {
		ctw.logger.Fatalf("Failed to update Transfer Watcher Status timestamp. Error [%s]", err)
	}
	ctw.logger.Tracef("Updated Transfer Watcher timestamp to [%s]", timestamp.ToHumanReadable(ts))
}

func (ctw Watcher) beginWatching(q qi.Queue) {
	milestoneTimestamp, err := ctw.statusRepository.Get(ctw.accountID.String())
	if err != nil {
		ctw.logger.Fatalf("Failed to retrieve Transfer Watcher Status timestamp. Error [%s]", err)
	}
	ctw.logger.Infof("Watching for Transfers after Timestamp [%s]", timestamp.ToHumanReadable(milestoneTimestamp))

	for {
		transactions, e := ctw.client.GetAccountCreditTransactionsAfterTimestamp(ctw.accountID, milestoneTimestamp)
		if e != nil {
			ctw.logger.Errorf("Suddenly stopped monitoring account. Error: [%s]", e)
			time.Sleep(ctw.pollingInterval * time.Second)
			go ctw.beginWatching(q)
			return
		}

		ctw.logger.Tracef("Polling found [%d] Transactions", len(transactions.Transactions))
		if len(transactions.Transactions) > 0 {
			for _, tx := range transactions.Transactions {
				go ctw.processTransaction(tx.TransactionID, q)
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

func (ctw Watcher) processTransaction(txID string, q qi.Queue) {
	ctw.logger.Infof("New Transaction with ID: [%s]", txID)

	tx, err := ctw.client.GetSuccessfulTransaction(txID)
	if err != nil {
		ctw.logger.Errorf("[%s] - Failed to get Transaction. Error: [%s]", txID, err)
		return
	}

	parsedTransfer, err := tx.GetIncomingTransfer(ctw.accountID.String())
	if err != nil {
		ctw.logger.Errorf("[%s] - Could not extract incoming transfer. Error: [%s]", tx.TransactionID, err)
		return
	}
	sourceAsset := parsedTransfer.Asset
	checkResult := ctw.transfers.SanityCheckTransfer(tx)
	if checkResult.Err != nil {
		ctw.logger.Errorf("[%s] - Sanity check failed. Error: [%s]", tx.TransactionID, checkResult.Err)
		return
	}
	targetChainId := checkResult.ChainId

	if checkResult.NftId == nil {
		ctw.initSuccessRatePrometheusMetrics(tx, constants.HederaNetworkId, targetChainId, sourceAsset)
	} else {
		sourceAsset = checkResult.NftId.TokenID.String()
	}

	nativeAsset := &asset.NativeAsset{
		ChainId: constants.HederaNetworkId,
		Asset:   sourceAsset,
	}

	targetChainAsset := ctw.assetsService.NativeToWrapped(sourceAsset, constants.HederaNetworkId, targetChainId)
	if targetChainAsset == "" {
		nativeAsset = ctw.assetsService.WrappedToNative(sourceAsset, constants.HederaNetworkId)
		if nativeAsset == nil {
			ctw.logger.Errorf("[%s] - Could not parse asset [%s] to its target chain correlation", tx.TransactionID, sourceAsset)
			return
		}
		targetChainAsset = nativeAsset.Asset
		if nativeAsset.ChainId != targetChainId {
			ctw.logger.Errorf("[%s] - Wrapped to Wrapped transfers currently not supported [%s] - [%d] for [%d]", tx.TransactionID, nativeAsset.Asset, nativeAsset.ChainId, targetChainId)
			return
		}
	}

	var transferMessage *payload.Transfer
	originator := hederaHelper.OriginatorFromTxId(tx.TransactionID)
	if checkResult.NftId != nil {
		nftAssetInfo, ok := ctw.assetsService.NonFungibleAssetInfo(constants.HederaNetworkId, sourceAsset)
		if !ok {
			ctw.logger.Errorf("[%s] - Failed to get asset info for NFT [%s] not found.", tx.TransactionID, sourceAsset)
			return
		}

		feeSent, found := tx.GetHBARTransfer(ctw.accountID.String())
		if !found {
			ctw.logger.Errorf("[%s] - Transfer to [%s] not found.", tx.TransactionID, ctw.accountID.String())
			return
		}

		fee, ok := ctw.pricingService.GetHederaNftFee(sourceAsset)
		if !ok {
			ctw.logger.Errorf("[%s] - Fee for [%s] not found.", tx.TransactionID, sourceAsset)
			return
		}

		totalHbarFeeExpected := fee

		feeForValidators := feeSent
		if originator != nftAssetInfo.TreasuryAccountId { // Custom Fees are expected only for Non-Treasury Account ID
			totalHbarFeeExpected += nftAssetInfo.CustomFeeTotalAmounts.FallbackFeeAmountInHbar
			// Validate that the required Custom fees by Token ID are sent
			if len(nftAssetInfo.CustomFeeTotalAmounts.FallbackFeeAmountsByTokenId) > 0 {
				if !ctw.validateNftTokenCustomFees(nftAssetInfo, tx, sourceAsset) {
					return
				}
			}
			feeForValidators = feeForValidators - nftAssetInfo.CustomFeeTotalAmounts.FallbackFeeAmountInHbar
		}

		// Validate that the HBAR fee is sent (including the Custom Fee in HBAR)
		if feeSent < totalHbarFeeExpected {
			ctw.logger.Errorf("[%s] - Invalid provided NFT Fee for [%s] in HBARs. It should be [%d], but was [%d].", tx.TransactionID, sourceAsset, fee, feeSent)
			return
		}

		transferMessage, err = ctw.createNonFungiblePayload(
			tx.TransactionID, checkResult.EvmAddress, sourceAsset, *nativeAsset, checkResult.NftId.SerialNumber, targetChainId, targetChainAsset, feeForValidators)

	} else {
		transferMessage, err = ctw.createFungiblePayload(
			tx.TransactionID, checkResult.EvmAddress, sourceAsset, *nativeAsset, parsedTransfer.AmountOrSerialNum, targetChainId, targetChainAsset)
	}

	if err != nil {
		ctw.logger.Errorf("[%s] - Failed to create payload. Error: [%s]", tx.TransactionID, err)
		return
	}

	transactionTimestamp, err := timestamp.FromString(tx.ConsensusTimestamp)
	if err != nil {
		ctw.logger.Errorf("[%s] - Failed to parse consensus timestamp [%s]. Error: [%s]", tx.TransactionID, tx.ConsensusTimestamp, err)
		return
	}

	transferMessage.Timestamp = time.Unix(0, transactionTimestamp)
	transferMessage.Originator = originator

	topic := ""
	if ctw.validator && transactionTimestamp > ctw.targetTimestamp {
		if nativeAsset.ChainId == constants.HederaNetworkId {
			if checkResult.NftId != nil {
				topic = constants.HederaNativeNftTransfer
			} else {
				topic = constants.HederaTransferMessageSubmission
			}
		} else {
			if checkResult.NftId != nil {
				ctw.logger.Errorf("[%s] - NFT Transfer not supported", tx.TransactionID)
				return
			}
			topic = constants.HederaBurnMessageSubmission
		}
	} else {
		transferMessage.NetworkTimestamp = tx.ConsensusTimestamp
		if nativeAsset.ChainId == constants.HederaNetworkId {
			if checkResult.NftId != nil {
				topic = constants.ReadOnlyHederaNativeNftTransfer
			} else {
				topic = constants.ReadOnlyHederaFeeTransfer
			}
		} else {
			if checkResult.NftId != nil {
				ctw.logger.Errorf("[%s] - NFT Read-only Transfer not supported", tx.TransactionID)
				return
			}
			topic = constants.ReadOnlyHederaBurn
		}
	}

	q.Push(&queue.Message{Payload: transferMessage, Topic: topic})
}

func (ctw Watcher) validateNftTokenCustomFees(nftAssetInfo *asset.NonFungibleAssetInfo, tx transaction.Transaction, sourceAsset string) bool {
	for tokenId := range nftAssetInfo.CustomFeeTotalAmounts.FallbackFeeAmountsByTokenId {
		feeForToken := nftAssetInfo.CustomFeeTotalAmounts.FallbackFeeAmountsByTokenId[tokenId]
		if feeForToken > 0 {
			tokenFeeSent, ok := tx.GetTokenTransfer(ctw.accountID.String())
			if !ok {
				ctw.logger.Errorf("[%s] - Transfer to [%s] not found.", tx.TransactionID, ctw.accountID.String())
				return false
			}

			if tokenFeeSent < feeForToken {
				ctw.logger.Errorf("[%s] - Invalid provided NFT Fee for [%s] in token [%s]. It should be [%d], but was [%d].", tx.TransactionID, sourceAsset, tokenId, feeForToken, tokenFeeSent)
				return false
			}
		}
	}

	return true
}

func (ctw Watcher) createFungiblePayload(transactionID string, receiver string, sourceAsset string, asset asset.NativeAsset, amount int64, targetChainId uint64, targetChainAsset string) (*payload.Transfer, error) {
	nativeAsset := ctw.assetsService.FungibleNativeAsset(asset.ChainId, asset.Asset)

	sourceAssetInfo, exists := ctw.assetsService.FungibleAssetInfo(constants.HederaNetworkId, sourceAsset)
	if !exists {
		return nil, fmt.Errorf("Failed to retrieve fungible asset info of [%s].", sourceAsset)
	}

	targetAssetInfo, exists := ctw.assetsService.FungibleAssetInfo(targetChainId, targetChainAsset)
	if !exists {
		return nil, fmt.Errorf("Failed to retrieve fungible asset info of [%s].", targetChainAsset)
	}

	targetAmount := decimal.TargetAmount(sourceAssetInfo.Decimals, targetAssetInfo.Decimals, big.NewInt(amount))
	if targetAmount.Cmp(big.NewInt(0)) == 0 {
		return nil, fmt.Errorf("Insufficient amount provided: Amount [%d] and Target Amount [%s].", amount, targetAmount)
	}

	tokenPriceInfo, exist := ctw.pricingService.GetTokenPriceInfo(asset.ChainId, nativeAsset.Asset)
	if !exist {
		errMsg := fmt.Sprintf("[%s] - Couldn't get price info in USD for asset [%s].", transactionID, nativeAsset.Asset)
		return nil, errors.New(errMsg)
	}

	if targetAmount.Cmp(tokenPriceInfo.MinAmountWithFee) < 0 {
		return nil, fmt.Errorf("[%s] - Transfer Amount [%s] is less than Minimum Amount [%s].", transactionID, targetAmount, tokenPriceInfo.MinAmountWithFee)
	}

	return payload.New(
		transactionID,
		constants.HederaNetworkId,
		targetChainId,
		nativeAsset.ChainId,
		receiver,
		sourceAsset,
		targetChainAsset,
		nativeAsset.Asset,
		targetAmount.String()), nil
}

func (ctw Watcher) createNonFungiblePayload(
	transactionID string,
	receiver string,
	sourceAsset string,
	nativeAsset asset.NativeAsset,
	serialNum int64,
	targetChainId uint64,
	targetChainAsset string,
	fee int64) (*payload.Transfer, error) {

	nftData, err := ctw.client.GetNft(sourceAsset, serialNum)
	if err != nil {
		return nil, err
	}
	
	decodedMetadata, e := base64.StdEncoding.DecodeString(nftData.Metadata)
	if e != nil {
		return nil, fmt.Errorf("[%s] - Failed to decode metadata [%s]. Error [%s]", transactionID, nftData.Metadata, e)
	}

	return payload.NewNft(
		transactionID,
		constants.HederaNetworkId,
		targetChainId,
		nativeAsset.ChainId,
		receiver,
		sourceAsset,
		targetChainAsset,
		nativeAsset.Asset,
		serialNum,
		string(decodedMetadata),
		fee), nil
}

func (ctw Watcher) initSuccessRatePrometheusMetrics(tx transaction.Transaction, sourceChainId, targetChainId uint64, asset string) {
	if !ctw.prometheusService.GetIsMonitoringEnabled() {
		return
	}

	metrics.CreateMajorityReachedIfNotExists(sourceChainId, targetChainId, asset, tx.TransactionID, ctw.prometheusService, ctw.logger)
	if ctw.assetsService.IsNative(constants.HederaNetworkId, asset) && targetChainId != constants.HederaNetworkId {
		metrics.CreateFeeTransferredIfNotExists(sourceChainId, targetChainId, asset, tx.TransactionID, ctw.prometheusService, ctw.logger)
	}
	metrics.CreateUserGetHisTokensIfNotExists(sourceChainId, targetChainId, asset, tx.TransactionID, ctw.prometheusService, ctw.logger)
}

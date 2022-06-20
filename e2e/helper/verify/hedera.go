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

package verify

import (
	"fmt"
	"math/big"
	"testing"
	"time"

	evmSetup "github.com/limechain/hedera-eth-bridge-validator/e2e/setup/evm"

	"github.com/limechain/hedera-eth-bridge-validator/e2e/helper/expected"

	"github.com/limechain/hedera-eth-bridge-validator/e2e/helper/fetch"

	mirror_node "github.com/limechain/hedera-eth-bridge-validator/app/clients/hedera/mirror-node"

	"github.com/limechain/hedera-eth-bridge-validator/app/clients/hedera/mirror-node/model/transaction"

	"github.com/limechain/hedera-eth-bridge-validator/constants"

	"github.com/limechain/hedera-eth-bridge-validator/e2e/helper/submit"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/hashgraph/hedera-sdk-go/v2"
	model "github.com/limechain/hedera-eth-bridge-validator/proto"
	"google.golang.org/protobuf/proto"
)

const expectedValidatorsCount = 3

func TransferToBridgeAccount(t *testing.T, hederaClient *hedera.Client, bridgeAccount hedera.AccountID, wrappedAsset string, evm evmSetup.Utils, memo string, whbarReceiverAddress common.Address, expectedAmount int64) (hedera.TransactionResponse, *big.Int) {
	t.Helper()
	instance, err := evmSetup.InitAssetContract(wrappedAsset, evm.EVMClient)
	if err != nil {
		t.Fatal(err)
	}
	// Get the wrapped hbar balance of the receiver before the transfer
	whbarBalanceBefore, err := instance.BalanceOf(&bind.CallOpts{}, whbarReceiverAddress)
	if err != nil {
		t.Fatal(err)
	}

	fmt.Println(fmt.Sprintf("WHBAR balance before transaction: [%s]", whbarBalanceBefore))
	// Get bridge account hbar balance before transfer
	receiverBalance := fetch.HederaAccountBalance(t, hederaClient, bridgeAccount).Hbars.AsTinybar()

	fmt.Println(fmt.Sprintf("Bridge account balance HBAR balance before transaction: [%d]", receiverBalance))

	// Get the transaction receipt to verify the transaction was executed
	transactionResponse, err := submit.HbarToBridgeAccount(hederaClient, bridgeAccount, memo, expectedAmount)
	if err != nil {
		t.Fatalf("Unable to send HBARs to Bridge Account, Error: [%s]", err)
	}

	transactionReceipt, err := transactionResponse.GetReceipt(hederaClient)
	if err != nil {
		t.Fatalf("Transaction unsuccessful, Error: [%s]", err)
	}

	fmt.Println(fmt.Sprintf("Successfully sent HBAR to bridge account, Status: [%s]", transactionReceipt.Status))

	// Get bridge account hbar balance after transfer
	receiverBalanceNew := fetch.HederaAccountBalance(t, hederaClient, bridgeAccount).Hbars.AsTinybar()

	fmt.Println(fmt.Sprintf("Bridge Account HBAR balance after transaction: [%d]", receiverBalanceNew))

	// Verify that the custodial address has received exactly the amount sent
	amount := receiverBalanceNew - receiverBalance

	// Verify that the bridge account has received exactly the amount sent
	if amount != expectedAmount {
		t.Fatalf("Expected to receive the exact transfer amount of hbar: [%v], but was [%v]", expectedAmount, amount)
	}

	return *transactionResponse, whbarBalanceBefore
}

func TokenTransferToBridgeAccount(t *testing.T, hederaClient *hedera.Client, bridgeAccount hedera.AccountID, evmAsset string, tokenID hedera.TokenID, evm evmSetup.Utils, memo string, wTokenReceiverAddress common.Address, amount int64) (hedera.TransactionResponse, *big.Int) {
	t.Helper()
	instance, err := evmSetup.InitAssetContract(evmAsset, evm.EVMClient)
	if err != nil {
		t.Fatal(err)
	}
	// Get the wrapped hts token balance of the receiver before the transfer
	wrappedBalanceBefore, err := instance.BalanceOf(&bind.CallOpts{}, wTokenReceiverAddress)
	if err != nil {
		t.Fatalf("Unable to query the token balance of the receiver account. Error: [%s]", err)
	}

	fmt.Println(fmt.Sprintf("Token balance before transaction: [%s]", wrappedBalanceBefore))
	// Get bridge account token balance before transfer
	receiverBalance := fetch.HederaAccountBalance(t, hederaClient, bridgeAccount)

	fmt.Println(fmt.Sprintf("Bridge account Token balance before transaction: [%d]", receiverBalance.Tokens.Get(tokenID)))
	// Get the transaction receipt to verify the transaction was executed
	transactionResponse, err := submit.TokensToBridgeAccount(hederaClient, bridgeAccount, tokenID, memo, amount)
	if err != nil {
		t.Fatalf(fmt.Sprintf("Unable to send Tokens to Bridge Account, Error: [%s]", err))
	}
	transactionReceipt, err := transactionResponse.GetReceipt(hederaClient)
	if err != nil {
		t.Fatalf(fmt.Sprintf("Transaction unsuccessful, Error: [%s]", err))
	}
	fmt.Println(fmt.Sprintf("Successfully sent Tokens to bridge account, Status: [%s]", transactionReceipt.Status))

	// Get bridge account HTS token balance after transfer
	receiverBalanceNew := fetch.HederaAccountBalance(t, hederaClient, bridgeAccount)

	fmt.Println(fmt.Sprintf("Bridge Account Token balance after transaction: [%d]", receiverBalanceNew.Tokens.Get(tokenID)))

	// Verify that the custodial address has received exactly the amount sent
	resultAmount := receiverBalanceNew.Tokens.Get(tokenID) - receiverBalance.Tokens.Get(tokenID)
	// Verify that the bridge account has received exactly the amount sent
	if resultAmount != uint64(amount) {
		t.Fatalf("Expected to receive the exact transfer amount of hbar: [%v], but received: [%v]", amount, resultAmount)
	}

	return *transactionResponse, wrappedBalanceBefore
}

func TopicMessages(t *testing.T, hederaClient *hedera.Client, txId string, topicId hedera.TopicID) []string {
	t.Helper()
	ethSignaturesCollected := 0
	var receivedSignatures []string

	fmt.Println(fmt.Sprintf("Waiting for Signatures & TX Hash to be published to Topic [%v]", topicId.String()))

	// Subscribe to Topic
	subscription, err := hedera.NewTopicMessageQuery().
		SetStartTime(time.Unix(0, time.Now().UnixNano())).
		SetTopicID(topicId).
		Subscribe(
			hederaClient,
			func(response hedera.TopicMessage) {
				msg := &model.TopicMessage{}
				err := proto.Unmarshal(response.Contents, msg)
				if err != nil {
					t.Fatal(err)
				}

				var transferID string
				var signature string
				switch msg.Message.(type) {
				case *model.TopicMessage_FungibleSignatureMessage:
					message := msg.GetFungibleSignatureMessage()
					transferID = message.TransferID
					signature = message.Signature
					break
				case *model.TopicMessage_NftSignatureMessage:
					message := msg.GetNftSignatureMessage()
					transferID = message.TransferID
					signature = message.Signature
				}

				//Verify that all the submitted messages have signed the same transaction
				if transferID != txId {
					fmt.Println(fmt.Sprintf(`Expected signature message to contain the transaction id: [%s]`, txId))
				} else {
					receivedSignatures = append(receivedSignatures, signature)
					ethSignaturesCollected++
					fmt.Println(fmt.Sprintf("Received Auth Signature [%s]", signature))
				}
			},
		)
	if err != nil {
		t.Fatalf("Unable to subscribe to Topic [%s]", topicId)
	}

	select {
	case <-time.After(120 * time.Second):
		if ethSignaturesCollected != expectedValidatorsCount {
			t.Fatalf("Expected the count of collected signatures to equal the number of validators: [%v], but was: [%v]", expectedValidatorsCount, ethSignaturesCollected)
		}
		subscription.Unsubscribe()
		return receivedSignatures
	}
	// Not possible end-case
	return nil
}

func NftOwner(t *testing.T, hederaClient *hedera.Client, tokenID string, serialNumber int64, expectedOwner hedera.AccountID) {
	t.Helper()
	nftID, err := hedera.NftIDFromString(fmt.Sprintf("%d@%s", serialNumber, tokenID))
	if err != nil {
		t.Fatal(err)
	}

	nftInfo, err := hedera.NewTokenNftInfoQuery().
		SetNftID(nftID).
		Execute(hederaClient)
	if err != nil {
		t.Fatal(err)
	}

	if len(nftInfo) != 1 {
		t.Fatalf("Invalid NFT Info [%s] length result. Result: [%v]", nftID.String(), nftInfo)
	}

	owner := nftInfo[0].AccountID
	if owner != expectedOwner {
		t.Fatalf("Invalid NftID [%s] owner. Expected [%s], actual [%s].", nftID.String(), expectedOwner, owner)
	}
}

func ReceiverAccountBalance(t *testing.T, hederaClient *hedera.Client, expectedReceiveAmount uint64, beforeHbarBalance hedera.AccountBalance, asset string, tokenId hedera.TokenID) {
	t.Helper()
	afterHbarBalance := fetch.HederaAccountBalance(t, hederaClient, hederaClient.GetOperatorAccountID())

	var beforeTransfer uint64
	var afterTransfer uint64

	if asset == constants.Hbar {
		beforeTransfer = uint64(beforeHbarBalance.Hbars.AsTinybar())
		afterTransfer = uint64(afterHbarBalance.Hbars.AsTinybar())
	} else {
		beforeTransfer = beforeHbarBalance.Tokens.Get(tokenId)
		afterTransfer = afterHbarBalance.Tokens.Get(tokenId)
	}

	if afterTransfer-beforeTransfer != expectedReceiveAmount {
		t.Fatalf("[%s] Expected %s balance after - [%d], but was [%d]. Expected to receive [%d], but was [%d]",
			hederaClient.GetOperatorAccountID(), asset, beforeTransfer+expectedReceiveAmount, afterTransfer, expectedReceiveAmount, afterTransfer-beforeTransfer)
	}
}

func AccountBalance(t *testing.T, hederaClient *hedera.Client, hederaID hedera.AccountID, expectedReceiveAmount uint64, beforeHbarBalance hedera.AccountBalance, asset string) {
	t.Helper()
	afterHbarBalance := fetch.HederaAccountBalance(t, hederaClient, hederaID)

	tokenAsset, err := hedera.TokenIDFromString(asset)
	if err != nil {
		t.Fatal(err)
	}

	beforeTransfer := beforeHbarBalance.Tokens.Get(tokenAsset)
	afterTransfer := afterHbarBalance.Tokens.Get(tokenAsset)

	if afterTransfer-beforeTransfer != expectedReceiveAmount {
		t.Fatalf("[%s] Expected %s balance after - [%d], but was [%d]. Expected to receive [%d], but was [%d]", hederaClient.GetOperatorAccountID(), asset, beforeTransfer+expectedReceiveAmount, afterTransfer, expectedReceiveAmount, afterTransfer-beforeTransfer)
	}
}

func SubmittedScheduledTx(t *testing.T, hederaClient *hedera.Client, mirrorNodeClient *mirror_node.Client, members []hedera.AccountID, asset string, expectedTransfers []transaction.Transfer, now time.Time) (transactionID, scheduleID string) {
	t.Helper()
	receiverTransactionID, receiverScheduleID := ScheduledTx(t, hederaClient, mirrorNodeClient, hederaClient.GetOperatorAccountID(), asset, expectedTransfers, now)

	membersTransactionID, membersScheduleID := MembersScheduledTxs(t, hederaClient, mirrorNodeClient, members, asset, expectedTransfers, now)

	if receiverTransactionID != membersTransactionID {
		t.Fatalf("Scheduled Transactions between members are different. Receiver [%s], Member [%s]", receiverTransactionID, membersTransactionID)
	}

	if receiverScheduleID != membersScheduleID {
		t.Fatalf("Scheduled IDs between members are different. Receiver [%s], Member [%s]", receiverScheduleID, membersScheduleID)
	}

	return receiverTransactionID, receiverScheduleID
}

func ScheduledMintTx(t *testing.T, hederaClient *hedera.Client, mirrorNodeClient *mirror_node.Client, account hedera.AccountID, asset string, expectedTransfers []transaction.Transfer, now time.Time) (transactionID, scheduleID string) {
	t.Helper()
	timeLeft := 180
	for {
		response, err := mirrorNodeClient.GetAccountTokenMintTransactionsAfterTimestamp(account, now.UnixNano())
		if err != nil {
			t.Fatal(err)
		}

		if len(response.Transactions) > 1 {
			t.Fatalf("[%s] - Found [%d] new transactions, must be 1.", account, len(response.Transactions))
		}

		txId, entityId := ListenForTx(t, response, mirrorNodeClient, expectedTransfers, asset)
		if txId != "" && entityId != "" {
			return txId, entityId
		}

		if timeLeft > 0 {
			fmt.Println(fmt.Sprintf("Could not find any scheduled transactions for account [%s]. Trying again. Time left: ~[%d] seconds", account, timeLeft))
			timeLeft -= 10
			time.Sleep(10 * time.Second)
			continue
		}
		break
	}

	t.Fatalf("Could not find any scheduled transactions for account [%s]", hederaClient.GetOperatorAccountID())
	return "", ""
}

func ScheduledBurnTx(t *testing.T, hederaClient *hedera.Client, mirrorNodeClient *mirror_node.Client, account hedera.AccountID, asset string, expectedTransfers []transaction.Transfer, now time.Time) (transactionID, scheduleID string) {
	t.Helper()
	timeLeft := 180
	for {
		response, err := mirrorNodeClient.GetAccountTokenBurnTransactionsAfterTimestamp(account, now.UnixNano())
		if err != nil {
			t.Fatal(err)
		}

		if len(response.Transactions) > 1 {
			t.Fatalf("[%s] - Found [%d] new transactions, must be 1.", account, len(response.Transactions))
		}

		txId, entityId := ListenForTx(t, response, mirrorNodeClient, expectedTransfers, asset)
		if txId != "" && entityId != "" {
			return txId, entityId
		}

		if timeLeft > 0 {
			fmt.Println(fmt.Sprintf("Could not find any scheduled transactions for account [%s]. Trying again. Time left: ~[%d] seconds", account, timeLeft))
			timeLeft -= 10
			time.Sleep(10 * time.Second)
			continue
		}
		break
	}

	t.Fatalf("Could not find any scheduled transactions for account [%s]", hederaClient.GetOperatorAccountID())
	return "", ""
}

func ScheduledNftTransfer(t *testing.T, hederaClient *hedera.Client, mirrorNodeClient *mirror_node.Client, bridgeAccount hedera.AccountID, expectedTransactionID, token string, serialNum int64) (transactionID, scheduleID string) {
	t.Helper()
	receiver := hederaClient.GetOperatorAccountID()
	timeLeft := 180

	for {
		response, err := mirrorNodeClient.GetNftTransactions(token, serialNum)
		if err != nil {
			t.Fatal(err)
		}

		for _, nftTransfer := range response.Transactions {
			if nftTransfer.Type == "CRYPTOTRANSFER" &&
				nftTransfer.ReceiverAccountID == receiver.String() &&
				nftTransfer.SenderAccountID == bridgeAccount.String() {

				scheduledTx, err := mirrorNodeClient.GetScheduledTransaction(nftTransfer.TransactionID)
				if err != nil {
					t.Fatalf("Failed to retrieve scheduled transaction [%s]. Error: [%s]", nftTransfer.TransactionID, err)
				}
				for _, tx := range scheduledTx.Transactions {
					if tx.Result == hedera.StatusSuccess.String() {
						schedule, err := mirrorNodeClient.GetSchedule(tx.EntityId)
						if err != nil {
							t.Fatalf("[%s] - Failed to get scheduled entity [%s]. Error: [%s]", expectedTransactionID, scheduleID, err)
						}
						if schedule.Memo == expectedTransactionID {
							return nftTransfer.TransactionID, schedule.ScheduleId
						}
					}
				}
			}
		}

		if timeLeft > 0 {
			fmt.Println(fmt.Sprintf("Could not find any scheduled transactions for account [%s]. Trying again. Time left: ~[%d] seconds", receiver, timeLeft))
			timeLeft -= 10
			time.Sleep(10 * time.Second)
			continue
		}
		break
	}

	t.Fatalf("Could not find any scheduled transactions for account [%s]", hederaClient.GetOperatorAccountID())
	return "", ""
}

func ScheduledTx(t *testing.T, hederaClient *hedera.Client, mirrorNodeClient *mirror_node.Client, account hedera.AccountID, asset string, expectedTransfers []transaction.Transfer, now time.Time) (transactionID, scheduleID string) {
	t.Helper()
	timeLeft := 180
	for {
		response, err := mirrorNodeClient.GetAccountCreditTransactionsAfterTimestamp(account, now.UnixNano())
		if err != nil {
			t.Fatal(err)
		}

		if len(response.Transactions) > 1 {
			t.Fatalf("[%s] - Found [%d] new transactions, must be 1.", account, len(response.Transactions))
		}

		txId, entityId := ListenForTx(t, response, mirrorNodeClient, expectedTransfers, asset)
		if txId != "" && entityId != "" {
			return txId, entityId
		}

		if timeLeft > 0 {
			fmt.Println(fmt.Sprintf("Could not find any scheduled transactions for account [%s]. Trying again. Time left: ~[%d] seconds", hederaClient.GetOperatorAccountID(), timeLeft))
			timeLeft -= 10
			time.Sleep(10 * time.Second)
			continue
		}
		break
	}

	t.Fatalf("Could not find any scheduled transactions for account [%s]", hederaClient.GetOperatorAccountID())
	return "", ""
}

func MembersScheduledTxs(t *testing.T, hederaClient *hedera.Client, mirrorNodeClient *mirror_node.Client, members []hedera.AccountID, asset string, expectedTransfers []transaction.Transfer, now time.Time) (transactionID, scheduleID string) {
	t.Helper()
	if len(members) == 0 {
		return "", ""
	}

	var transactions []string
	var scheduleIDs []string
	for _, member := range members {
		txID, scheduleID := ScheduledTx(t, hederaClient, mirrorNodeClient, member, asset, expectedTransfers, now)
		transactions = append(transactions, txID)

		if !expected.AllSame(transactions) {
			t.Fatalf("Transaction [%s] does not match with previously added transactions.", txID)
		}
		scheduleIDs = append(scheduleIDs, scheduleID)

		if !expected.AllSame(scheduleIDs) {
			t.Fatalf("ScheduleID [%s] does not match with previously added ids", scheduleID)
		}
	}

	return transactions[0], scheduleIDs[0]
}

func ListenForTx(t *testing.T, response *transaction.Response, mirrorNode *mirror_node.Client, expectedTransfers []transaction.Transfer, asset string) (string, string) {
	t.Helper()
	for _, transaction := range response.Transactions {
		if transaction.Scheduled == true {
			scheduleCreateTx, err := mirrorNode.GetTransaction(transaction.TransactionID)
			if err != nil {
				t.Fatal(err)
			}

			for _, expectedTransfer := range expectedTransfers {
				found := false
				if asset == constants.Hbar {
					for _, transfer := range transaction.Transfers {
						if expectedTransfer == transfer {
							found = true
							break
						}
					}
				} else {
					for _, transfer := range transaction.TokenTransfers {
						if expectedTransfer == transfer {
							found = true
							break
						}
					}
				}

				if !found {
					t.Fatalf("[%s] - Expected transfer [%v] not found.", transaction.TransactionID, expectedTransfer)
				}
			}

			for _, tx := range scheduleCreateTx.Transactions {
				if tx.EntityId != "" {
					return tx.TransactionID, tx.EntityId
				}
			}
		}
	}
	return "", ""
}
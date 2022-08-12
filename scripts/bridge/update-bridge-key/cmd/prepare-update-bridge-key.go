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

package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"github.com/hashgraph/hedera-sdk-go/v2"
	clientScript "github.com/limechain/hedera-eth-bridge-validator/scripts/client"
	"strings"
	"time"
)

func main() {
	executorAccountID := flag.String("executorAccountID", "", "Hedera Account Id")
	bridgeAccount := flag.String("bridgeAccount", "", "Bridge Account Id")
	network := flag.String("network", "", "Hedera Network Type")
	nodeAccountID := flag.String("nodeAccountID", "0.0.3", "Node account id on which to process the transaction.")
	validStartMinutes := flag.Int("validStartMinutes", 2, "Valid minutes for which the transaction needs to be signed and submitted after.")
	publicKeys := flag.String("publicKeys", "", "Public keys from which to generate the new bridge account key.")
	keyThreshold := flag.Int("keyThreshold", 0, "Bridge accounts key threshold")
	flag.Parse()
	validatePrepareUpdateBridgeKeyParams(executorAccountID, bridgeAccount, network, validStartMinutes, publicKeys, keyThreshold)

	keys, bridgeAccountParsed, executor, nodeAccount := parseParams(publicKeys, bridgeAccount, executorAccountID, nodeAccountID)

	newBridgeKey := hedera.KeyListWithThreshold(uint(*keyThreshold))
	for _, key := range keys {
		newBridgeKey.Add(key)
	}

	fmt.Println("Creating new bridge account key with public keys:", keys)

	client := clientScript.GetClientForNetwork(*network)
	validTime := time.Minute * time.Duration(*validStartMinutes)
	transactionID := hedera.TransactionIDGenerate(executor)
	frozenTx, err := hedera.NewAccountUpdateTransaction().
		SetTransactionID(transactionID).
		SetTransactionValidDuration(validTime).
		SetAccountID(bridgeAccountParsed).
		SetKey(newBridgeKey).
		SetNodeAccountIDs([]hedera.AccountID{nodeAccount}).
		FreezeWith(client)
	if err != nil {
		panic(err)
	}

	bytes, err := frozenTx.ToBytes()
	if err != nil {
		panic(err)
	}
	fmt.Println("Transaction Bytes:")
	fmt.Println(hex.EncodeToString(bytes))
}

func parseParams(
	publicKeys *string,
	bridgeAccount *string,
	executorId *string,
	nodeAccountId *string,
) ([]hedera.PublicKey, hedera.AccountID, hedera.AccountID, hedera.AccountID) {
	pubKeysSlice := strings.Split(*publicKeys, ",")
	var keys []hedera.PublicKey
	for _, pubKey := range pubKeysSlice {
		publicKeyFromStr, err := hedera.PublicKeyFromString(pubKey)
		if err != nil {
			panic(err)
		}
		keys = append(keys, publicKeyFromStr)
	}

	bridgeAccountParsed, err := hedera.AccountIDFromString(*bridgeAccount)
	if err != nil {
		panic(err)
	}
	executor, err := hedera.AccountIDFromString(*executorId)
	if err != nil {
		panic(err)
	}
	nodeAccount, err := hedera.AccountIDFromString(*nodeAccountId)
	if err != nil {
		panic(fmt.Sprintf("Invalid Node Account Id. Err: %s", err))
	}
	return keys, bridgeAccountParsed, executor, nodeAccount
}

func validatePrepareUpdateBridgeKeyParams(
	executorId *string,
	bridgeAccount *string,
	network *string,
	validStartMinutes *int,
	publicKeys *string,
	keyThreshold *int,
) {
	if *executorId == "0.0" {
		panic("Executor id was not provided")
	}
	if *bridgeAccount == "" {
		panic("bridgeAccount not provided")
	}
	if *network == "" {
		panic("network not provided")
	}
	if *validStartMinutes == 0 {
		panic("validStartMinutes not provided")
	}
	if *publicKeys == "" {
		panic("publicKeys not provided")
	}
	if *keyThreshold == 0 {
		panic("keyThreshold not provided")
	}
}

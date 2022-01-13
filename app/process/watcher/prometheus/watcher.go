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

package prometheus

import (
	"fmt"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/limechain/hedera-eth-bridge-validator/app/clients/evm/contracts/router"
	"github.com/limechain/hedera-eth-bridge-validator/app/clients/evm/contracts/wtoken"
	"github.com/limechain/hedera-eth-bridge-validator/app/clients/hedera/mirror-node/model"
	"github.com/limechain/hedera-eth-bridge-validator/app/domain/client"
	qi "github.com/limechain/hedera-eth-bridge-validator/app/domain/queue"
	prometheusServices "github.com/limechain/hedera-eth-bridge-validator/app/services/prometheus"
	"github.com/limechain/hedera-eth-bridge-validator/config"
	"github.com/limechain/hedera-eth-bridge-validator/constants"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	"math/big"
	"strconv"
	"strings"
	"time"
)

var (
	payerAccountBalance = prometheusServices.NewGaugeMetric(constants.FeeAccountAmountName,
		constants.FeeAccountAmountHelp)
	bridgeAccountBalance = prometheusServices.NewGaugeMetric(constants.BridgeAccountAmountName,
		constants.BridgeAccountAmountHelp)
	operatorAccountBalance = prometheusServices.NewGaugeMetric(
		constants.OperatorAccountAmountName,
		constants.OperatorAccountAmountHelp)
	// A mapping, storing all asset metrics per network
	assetsMetrics = map[int64]map[string]prometheus.Gauge{}
	// A mapping, storing all bridge asset metrics
	bridgeAssetsMetrics = map[string]prometheus.Gauge{}
	// A mapping, storing all asset for the EVM balance metrics per network
	balanceAssetsMetrics = map[int64]map[string]prometheus.Gauge{}
	// A mapping, storing all asset for the EVM count metrics per network
	countAssetsMetrics = map[int64]map[string]prometheus.Gauge{}
)

func registerMetrics(node client.MirrorNode, bridgeConfig config.Bridge, EVMClient map[int64]client.EVM) {
	//Fee Account Balance
	prometheusServices.RegisterGaugeMetric(payerAccountBalance)
	//Bridge Account Balance
	prometheusServices.RegisterGaugeMetric(bridgeAccountBalance)
	//Operator Account Balance
	prometheusServices.RegisterGaugeMetric(operatorAccountBalance)
	//Assets balance
	registerAssetsMetrics(node, bridgeConfig, EVMClient)
	//Bridge Account Assets
	registerBridgeAssetsMetrics(node, bridgeConfig)
}

type Watcher struct {
	dashboardPolling time.Duration
	client           client.MirrorNode
	configuration    config.Config
	EVMClient        map[int64]client.EVM
}

func NewWatcher(
	dashboardPolling time.Duration,
	client client.MirrorNode,
	configuration config.Config,
	EVMClient map[int64]client.EVM,
) *Watcher {
	return &Watcher{
		dashboardPolling: dashboardPolling,
		client:           client,
		configuration:    configuration,
		EVMClient:        EVMClient,
	}
}

func (pw Watcher) Watch(q qi.Queue) {
	// there will be no handler, so the q is to implement the interface
	go pw.beginWatching()
}

func (pw Watcher) beginWatching() {
	//The queue will be not used
	dashboardPolling := pw.dashboardPolling
	node := pw.client
	configuration := pw.configuration
	EVMClient := pw.EVMClient
	registerMetrics(
		node,
		configuration.Bridge,
		EVMClient,
	)
	setMetrics(
		node,
		configuration,
		EVMClient,
		dashboardPolling,
	)
}

func registerAssetsMetrics(node client.MirrorNode, bridgeConfig config.Bridge, EVMClient map[int64]client.EVM) {
	fungibleNetworkAssets := bridgeConfig.Assets.GetFungibleNetworkAssets()
	for chainId, assetArr := range fungibleNetworkAssets {
		for _, asset := range assetArr {
			if chainId == 0 { // Hedera
				if asset != constants.Hbar {
					res, e := node.GetToken(asset)
					if e != nil {
						panic(e)
					}
					assetMetricName := tokenIDtoMetricName(res.TokenID)
					assetMetricHelp := fmt.Sprintf("%s%s", constants.AssetMetricsHelpPrefix, res.Name)
					addAssetMetric(chainId, res.TokenID, assetMetricName, assetMetricHelp, assetsMetrics)
				} else { // HBAR
					assetMetricName := tokenIDtoMetricName(asset)
					assetMetricHelp := fmt.Sprintf("%s%s", constants.AssetMetricsHelpPrefix, asset)
					addAssetMetric(chainId, asset, assetMetricName, assetMetricHelp, assetsMetrics)
				}
			} else { // EVM
				evm := EVMClient[chainId].GetClient()
				wrappedInstance, e := wtoken.NewWtoken(common.HexToAddress(asset), evm)
				if e != nil {
					panic(e)
				}
				name, e := wrappedInstance.Name(&bind.CallOpts{})
				if e != nil {
					panic(e)
				}
				assetMetricName := tokenIDtoMetricName(asset)
				assetMetricHelp := fmt.Sprintf("%s%s", constants.AssetMetricsHelpPrefix, name)
				addAssetMetric(chainId, asset, assetMetricName, assetMetricHelp, assetsMetrics)

				balanceAssetMetricName := fmt.Sprintf("%s%s",
					constants.BalanceAssetMetricNamePrefix,
					tokenIDtoMetricName(asset))
				balanceAssetMetricHelp := fmt.Sprintf("%s%s%s%s",
					constants.BalanceAssetMetricHelpPrefix,
					name,
					constants.AssetMetricHelpSuffix,
					bridgeConfig.EVMs[chainId].RouterContractAddress)
				addAssetMetric(chainId, asset, balanceAssetMetricName, balanceAssetMetricHelp, balanceAssetsMetrics)

				countAssetMetricName := fmt.Sprintf("%s%s",
					constants.CountAssetMetricNamePrefix,
					tokenIDtoMetricName(asset))
				countAssetMetricHelp := fmt.Sprintf("%s%s%s%s",
					constants.CountAssetMetricHelpPrefix,
					name,
					constants.AssetMetricHelpSuffix,
					bridgeConfig.EVMs[chainId].RouterContractAddress)
				addAssetMetric(chainId, asset, countAssetMetricName, countAssetMetricHelp, countAssetsMetrics)
			}
		}
	}
}

func registerBridgeAssetsMetrics(node client.MirrorNode, bridgeConfig config.Bridge) {
	bridgeAcc := getAccount(node, bridgeConfig.Hedera.BridgeAccount)
	for _, token := range bridgeAcc.Balance.Tokens {
		res, e := node.GetToken(token.TokenID)
		if e != nil {
			panic(e)
		}

		fixName := tokenIDtoMetricName(res.TokenID)
		name := fmt.Sprintf("%s%s", constants.BridgeAssetMetricsNamePrefix, fixName)
		help := fmt.Sprintf("%s%s", constants.BridgeAssetMetricsNameHelp, res.Name)

		metric := prometheusServices.NewGaugeMetric(name, help)
		prometheusServices.RegisterGaugeMetric(metric)
		bridgeAssetsMetrics[res.TokenID] = metric

		log.Infof("Registered Token with ID [%s] in the Bridge Account in metrics with name [%s]", res.TokenID, name)
	}
}

func tokenIDtoMetricName(id string) string {
	replace := strings.Replace(id, constants.DotSymbol, constants.ReplaceDotSymbol, constants.DotSymbolRep)
	result := fmt.Sprintf("%s%s", constants.AssetMetricsNamePrefix, replace)

	return result
}

func addAssetMetric(chainId int64,
	tokenID string,
	name string,
	help string,
	assetsMap map[int64]map[string]prometheus.Gauge,
) {
	metric := prometheusServices.NewGaugeMetric(name, help)
	prometheusServices.RegisterGaugeMetric(metric)
	if assetsMap[chainId] == nil {
		assetsMap[chainId] = make(map[string]prometheus.Gauge)
	}
	assetsMap[chainId][tokenID] = metric
	log.Infof("Registered Token with ID [%s] in metrics with name [%s] help [%s]", tokenID, name, help)
}

func getAccount(node client.MirrorNode, accountId string) *model.AccountsResponse {
	account, e := node.GetAccount(accountId)
	if e != nil {
		panic(e)
	}
	return account
}

func setMetrics(
	node client.MirrorNode,
	config config.Config,
	EVMClient map[int64]client.EVM,
	dashboardPolling time.Duration,
) {
	for {
		payerAcc := getAccount(node, config.Bridge.Hedera.PayerAccount)
		bridgeAcc := getAccount(node, config.Bridge.Hedera.BridgeAccount)
		operatorAcc := getAccount(node, config.Node.Clients.Hedera.Operator.AccountId)

		payerAccountBalance.Set(getAccountBalance(payerAcc))
		bridgeAccountBalance.Set(getAccountBalance(bridgeAcc))
		operatorAccountBalance.Set(getAccountBalance(operatorAcc))

		setAssetsTotalSupply(node, EVMClient)
		setAssetsBalance(EVMClient, config.Bridge)
		setAssetsCount(EVMClient, config.Bridge)

		setBridgeAccAssetsTotalSupply(bridgeAcc)

		log.Infoln("Dashboard Polling interval: ", dashboardPolling)
		time.Sleep(dashboardPolling)
	}
}

func getAccountBalance(account *model.AccountsResponse) float64 {
	//accountBalance := float64(account.Balance.Balance)
	//tinyBarBalance := float64(hedera.NewHbar(accountBalance).AsTinybar())
	//
	//log.Infof("The Account with ID [%s] has balance AsTinybar = %f", account.Account, tinyBarBalance)
	//
	//return tinyBarBalance

	balance := float64(account.Balance.Balance)
	log.Infof("The Account with ID [%s] has balance = %f", account.Account, balance)

	return balance
}

func setAssetsTotalSupply(node client.MirrorNode, EVMClient map[int64]client.EVM) {
	for chainID, assets := range assetsMetrics {
		for assetID, assetMetric := range assets {
			totalSupply := getAssetTotalSupply(node, EVMClient, chainID, assetID)
			assetMetric.Set(totalSupply)
		}
	}
}

func setAssetsBalance(EVMClient map[int64]client.EVM, bridgeConfig config.Bridge) {
	for chainID, assets := range balanceAssetsMetrics {
		evm := EVMClient[chainID].GetClient()
		address := common.HexToAddress(bridgeConfig.EVMs[chainID].RouterContractAddress)
		for assetID, assetMetric := range assets {
			wrappedInstance, e := wtoken.NewWtoken(common.HexToAddress(assetID), evm)
			if e != nil {
				panic(e)
			}

			b, e := wrappedInstance.BalanceOf(&bind.CallOpts{}, address)
			if e != nil {
				panic(e)
			}

			balance, _ := new(big.Float).SetInt(b).Float64()
			assetMetric.Set(balance)

			log.Infof("The Asset with ID [%s] has Balance of  = %f at router address [%s]", assetID, balance, bridgeConfig.EVMs[chainID].RouterContractAddress)
		}
	}
}

func setAssetsCount(EVMClient map[int64]client.EVM, bridgeConfig config.Bridge) {
	for chainID, assets := range countAssetsMetrics {
		evm := EVMClient[chainID].GetClient()
		address := common.HexToAddress(bridgeConfig.EVMs[chainID].RouterContractAddress)
		for assetID, assetMetric := range assets {
			routerInstance, e := router.NewRouter(address, evm)
			if e != nil {
				panic(e)
			}

			c, e := routerInstance.NativeTokensCount(&bind.CallOpts{})
			if e != nil {
				panic(e)
			}

			count, _ := new(big.Float).SetInt(c).Float64()
			assetMetric.Set(count)

			log.Infof("The Asset with ID [%s] has Count of  = %f at router address [%s]", assetID, count, bridgeConfig.EVMs[chainID].RouterContractAddress)
		}
	}
}

func getAssetTotalSupply(node client.MirrorNode, EVMClient map[int64]client.EVM, chainID int64, assetID string) float64 {
	var totalSupply float64
	if chainID == 0 { // Hedera
		var supply string
		if assetID != constants.Hbar {
			token, e := node.GetToken(assetID)
			if e != nil {
				panic(e)
			}
			supply = token.TotalSupply
		} else { // HBAR
			token, e := node.GetNetworkSupply()
			if e != nil {
				panic(e)
			}
			supply = token.TotalSupply
		}
		ts, e := strconv.ParseFloat(supply, 64)
		if e != nil {
			panic(e)
		}
		totalSupply = ts
	} else { // EVM
		evm := EVMClient[chainID].GetClient()
		wrappedInstance, e := wtoken.NewWtoken(common.HexToAddress(assetID), evm)
		if e != nil {
			panic(e)
		}
		ts, e := wrappedInstance.TotalSupply(&bind.CallOpts{})
		if e != nil {
			panic(e)
		}
		totalSupply, _ = new(big.Float).SetInt(ts).Float64()
	}
	log.Infof("The Asset with ID [%s] has Total Supply  = %f", assetID, totalSupply)
	return totalSupply
}

func setBridgeAccAssetsTotalSupply(bridgeAcc *model.AccountsResponse) {
	for _, token := range bridgeAcc.Balance.Tokens {
		assetMetric := bridgeAssetsMetrics[token.TokenID]
		assetMetric.Set(float64(token.Balance))
		log.Infof("The Bridge Account asset with ID [%s] has Balance = %f", token.TokenID, float64(token.Balance))
	}
}

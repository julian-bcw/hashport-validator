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

package fee_policy_config

import (
	"time"

	"github.com/hashgraph/hedera-sdk-go/v2"
	qi "github.com/limechain/hedera-eth-bridge-validator/app/domain/queue"
	"github.com/limechain/hedera-eth-bridge-validator/app/domain/service"
	"github.com/limechain/hedera-eth-bridge-validator/config"
	log "github.com/sirupsen/logrus"
)

var (
	sleepTime = 10 * time.Minute
)

type Watcher struct {
	svc              service.FeePolicyHandler
	feePolicyTopicID hedera.TopicID
	logger           *log.Entry
}

func NewWatcher(svc service.FeePolicyHandler, feePolicyTopicID hedera.TopicID) *Watcher {
	return &Watcher{
		svc:              svc,
		feePolicyTopicID: feePolicyTopicID,
		logger:           config.GetLoggerFor("Fee Policy Config Watcher"),
	}
}

func (wather *Watcher) Watch(q qi.Queue) {
	// there will be no handler, so the q is to implement the interface
	go func() {
		for {
			wather.watchIteration()
			time.Sleep(sleepTime)
		}
	}()
}

func (wather *Watcher) watchIteration() {
	wather.logger.Infof("Checking for new Fee Policy Config ...")
	_, err := wather.svc.ProcessLatestFeePolicyConfig(wather.feePolicyTopicID)
	if err != nil {
		wather.logger.Errorf(err.Error())
	} else {
		wather.logger.Infof("Fee Policy Config finished successfully!")
	}
}

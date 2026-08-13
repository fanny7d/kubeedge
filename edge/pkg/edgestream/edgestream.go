/*
Copyright 2020 The KubeEdge Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

   http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package edgestream

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubeedge/api/apis/componentconfig/edgecore/v1alpha2"
	"github.com/kubeedge/beehive/pkg/core"
	beehiveContext "github.com/kubeedge/beehive/pkg/core/context"
	"github.com/kubeedge/kubeedge/edge/pkg/common/modules"
	"github.com/kubeedge/kubeedge/edge/pkg/edgehub"
	"github.com/kubeedge/kubeedge/edge/pkg/edgestream/config"
	"github.com/kubeedge/kubeedge/pkg/features"
	"github.com/kubeedge/kubeedge/pkg/stream"
	"github.com/kubeedge/kubeedge/pkg/util"
)

const (
	edgeStreamInitialReconnectDelay = 2 * time.Second
	edgeStreamMaxReconnectDelay     = 30 * time.Second
	edgeStreamStableSessionDuration = time.Minute
	edgeStreamReconnectFactor       = 2.0
	edgeStreamReconnectJitter       = 0.2
	edgeStreamReconnectSteps        = 5
)

type edgestream struct {
	enable           bool
	hostnameOverride string
	nodeIP           string
}

var _ core.Module = (*edgestream)(nil)

func newEdgeStream(enable bool, hostnameOverride, nodeIP string) *edgestream {
	return &edgestream{
		enable:           enable,
		hostnameOverride: hostnameOverride,
		nodeIP:           nodeIP,
	}
}

// Register register edgestream
func Register(s *v1alpha2.EdgeStream, hostnameOverride, nodeIP string) {
	config.InitConfigure(s)
	core.Register(newEdgeStream(s.Enable, hostnameOverride, nodeIP))
}

func (e *edgestream) Name() string {
	return modules.EdgeStreamModuleName
}

func (e *edgestream) Group() string {
	return modules.StreamGroup
}

func (e *edgestream) Enable() bool {
	return e.enable
}

func (e *edgestream) RestartPolicy() *core.ModuleRestartPolicy {
	if !features.DefaultFeatureGate.Enabled(features.ModuleRestart) {
		return nil
	}
	return &core.ModuleRestartPolicy{
		RestartType:            core.RestartTypeOnFailure,
		IntervalTimeGrowthRate: 2.0,
	}
}

func (e *edgestream) Start() {
	serverURL := url.URL{
		Scheme: "wss",
		Host:   config.Config.TunnelServer,
		Path:   "/v1/kubeedge/connect",
	}
	// TODO: Will improve in the future
	if ok := <-edgehub.GetCertSyncChannel()[e.Name()]; !ok {
		klog.Exitf("Failed to find cert key pair")
	}

	cert, err := tls.LoadX509KeyPair(config.Config.TLSTunnelCertFile, config.Config.TLSTunnelPrivateKeyFile)
	if err != nil {
		klog.Exitf("Failed to load x509 key pair: %v", err)
	}
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		Certificates:       []tls.Certificate{cert},
	}

	backoff := newEdgeStreamReconnectBackoff(edgeStreamReconnectJitter)
	for {
		delay := backoff.Step()
		if !waitForEdgeStreamReconnect(beehiveContext.Done(), delay) {
			return
		}

		started := time.Now()
		err := e.TLSClientConnect(serverURL, tlsConfig)
		sessionDuration := time.Since(started)
		if resetEdgeStreamReconnectBackoff(&backoff, sessionDuration) {
			klog.V(2).Infof("Reset EdgeStream reconnect backoff after a stable session lasting %s", sessionDuration.Round(time.Second))
		}
		if err != nil {
			klog.Warningf("EdgeStream connection ended after %s: %v; next retry base delay is %s", sessionDuration.Round(time.Second), err, backoff.Duration)
			continue
		}
		klog.Warningf("EdgeStream connection ended without an error after %s; next retry base delay is %s", sessionDuration.Round(time.Second), backoff.Duration)
	}
}

func newEdgeStreamReconnectBackoff(jitter float64) wait.Backoff {
	return wait.Backoff{
		Duration: edgeStreamInitialReconnectDelay,
		Factor:   edgeStreamReconnectFactor,
		Jitter:   jitter,
		Steps:    edgeStreamReconnectSteps,
		Cap:      edgeStreamMaxReconnectDelay,
	}
}

func resetEdgeStreamReconnectBackoff(backoff *wait.Backoff, sessionDuration time.Duration) bool {
	if sessionDuration < edgeStreamStableSessionDuration {
		return false
	}
	*backoff = newEdgeStreamReconnectBackoff(backoff.Jitter)
	return true
}

func waitForEdgeStreamReconnect(stop <-chan struct{}, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-stop:
		return false
	case <-timer.C:
		return true
	}
}

func (e *edgestream) TLSClientConnect(url url.URL, tlsConfig *tls.Config) error {
	klog.V(2).Info("Start a new tunnel stream connection ...")

	// If the node IP address is not specified in the configuration file,
	// the node IP address is reacquired each time the tunnel stream is reconnected
	var nodeIP string
	if e.nodeIP == "" {
		ip, err := util.GetLocalIP(util.GetHostname())
		if err != nil {
			return fmt.Errorf("failed to get Local IP address: %v", err)
		}
		klog.Infof("get node local IP address successfully: %s", ip)
		nodeIP = ip
	} else {
		nodeIP = e.nodeIP
	}

	dial := websocket.Dialer{
		TLSClientConfig:  tlsConfig,
		HandshakeTimeout: time.Duration(config.Config.HandshakeTimeout) * time.Second,
	}
	header := http.Header{}
	header.Add(stream.SessionKeyHostNameOverride, e.hostnameOverride)
	header.Add(stream.SessionKeyInternalIP, nodeIP)

	con, _, err := dial.Dial(url.String(), header)
	if err != nil {
		return fmt.Errorf("dial %s: %w", url.String(), err)
	}
	session := NewTunnelSession(con)
	if err := session.Serve(); err != nil {
		return fmt.Errorf("serve tunnel session: %w", err)
	}
	return nil
}

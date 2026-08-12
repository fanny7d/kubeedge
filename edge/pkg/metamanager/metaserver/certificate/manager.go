/*
Copyright 2024 The Kubernetes Authors.

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

package certificate

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	certificates "k8s.io/api/certificates/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/certificate"
	"k8s.io/klog/v2"

	beehivecontext "github.com/kubeedge/beehive/pkg/core/context"
	"github.com/kubeedge/beehive/pkg/core/model"
	"github.com/kubeedge/kubeedge/edge/pkg/common/cloudconnection"
	"github.com/kubeedge/kubeedge/edge/pkg/common/message"
	"github.com/kubeedge/kubeedge/edge/pkg/common/modules"
)

const (
	pairNamePrefix = "metaserver"
	caFileName     = "ca.crt"
	pollInterval   = 5 * time.Second

	// CertificatesDir defines default certificate directory
	CertificatesDir = "/etc/kubeedge/pki/metaserver"
)

type ServerCertificateManager struct {
	certificate.Manager

	nodeName      types.NodeName
	ips           []net.IP
	certDirectory string
	now           func() time.Time

	cloudConnected func() bool
	fetchCA        func() ([]byte, error)
	retryInterval  time.Duration
	remoteCASynced atomic.Bool
}

func NewServerCertificateManager(
	kubeClient clientset.Interface,
	nodeName types.NodeName,
	ips []net.IP,
	certDirectory string) (*ServerCertificateManager, error) {
	var clientsetFn certificate.ClientsetFunc
	if kubeClient != nil {
		clientsetFn = func(current *tls.Certificate) (clientset.Interface, error) {
			return kubeClient, nil
		}
	}

	certificateStore, err := certificate.NewFileStore(pairNamePrefix, certDirectory, certDirectory, "", "")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize server certificate store: %v", err)
	}

	getTemplate := func() *x509.CertificateRequest {
		if len(ips) == 0 {
			return nil
		}
		return &x509.CertificateRequest{
			Subject: pkix.Name{
				CommonName:   fmt.Sprintf("system:node:%s", nodeName),
				Organization: []string{"system:nodes"},
			},
			IPAddresses: ips,
		}
	}

	m, err := certificate.NewManager(&certificate.Config{
		Name:        fmt.Sprintf("metaserver-csr-%s", nodeName),
		ClientsetFn: clientsetFn,
		GetTemplate: getTemplate,
		SignerName:  certificates.KubeletServingSignerName,
		Usages: []certificates.KeyUsage{
			certificates.UsageDigitalSignature,
			certificates.UsageKeyEncipherment,
			certificates.UsageServerAuth,
		},
		CertificateStore: certificateStore,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize server certificate manager: %v", err)
	}

	scm := &ServerCertificateManager{
		Manager:        m,
		nodeName:       nodeName,
		ips:            append([]net.IP(nil), ips...),
		certDirectory:  certDirectory,
		now:            time.Now,
		cloudConnected: cloudconnection.IsConnected,
		retryInterval:  pollInterval,
	}
	scm.fetchCA = scm.getKubeAPIServerCA
	return scm, nil
}

func (scm *ServerCertificateManager) caPath() string {
	return filepath.Join(scm.certDirectory, caFileName)
}

func (scm *ServerCertificateManager) validateCAFile() (*x509.CertPool, error) {
	content, err := os.ReadFile(scm.caPath())
	if err != nil {
		return nil, fmt.Errorf("load cached Kubernetes CA: %w", err)
	}
	return newVerifiedCAPool(content)
}

func newVerifiedCAPool(content []byte) (*x509.CertPool, error) {
	certificates, err := cert.ParseCertsPEM(content)
	if err != nil {
		return nil, fmt.Errorf("parse Kubernetes CA bundle: %w", err)
	}
	if len(certificates) == 0 {
		return nil, fmt.Errorf("Kubernetes CA bundle contains no certificates")
	}
	pool := x509.NewCertPool()
	for _, certificate := range certificates {
		if !certificate.IsCA {
			return nil, fmt.Errorf("Kubernetes CA bundle contains a non-CA certificate with subject %q", certificate.Subject.String())
		}
		if certificate.KeyUsage != 0 && certificate.KeyUsage&x509.KeyUsageCertSign == 0 {
			return nil, fmt.Errorf("Kubernetes CA certificate %q cannot sign certificates", certificate.Subject.String())
		}
		pool.AddCert(certificate)
	}
	return pool, nil
}

func (scm *ServerCertificateManager) validateCurrentServingCertificate() error {
	roots, err := scm.validateCAFile()
	if err != nil {
		return err
	}
	return scm.validateCurrentServingCertificateWithRoots(roots)
}

func (scm *ServerCertificateManager) validateCurrentServingCertificateWithRoots(roots *x509.CertPool) error {
	current := scm.Current()
	if current == nil {
		return fmt.Errorf("cached MetaServer serving certificate is missing or expired")
	}

	leaf := current.Leaf
	if leaf == nil {
		if len(current.Certificate) == 0 {
			return fmt.Errorf("cached MetaServer serving certificate has no leaf")
		}
		parsed, err := x509.ParseCertificate(current.Certificate[0])
		if err != nil {
			return fmt.Errorf("parse cached MetaServer serving certificate: %w", err)
		}
		leaf = parsed
	}

	now := scm.now()
	if now.Before(leaf.NotBefore) {
		return fmt.Errorf("cached MetaServer serving certificate is not valid before %s", leaf.NotBefore.Format(time.RFC3339))
	}
	if !now.Before(leaf.NotAfter) {
		return fmt.Errorf("cached MetaServer serving certificate expired at %s", leaf.NotAfter.Format(time.RFC3339))
	}

	expectedCommonName := fmt.Sprintf("system:node:%s", scm.nodeName)
	if leaf.Subject.CommonName != expectedCommonName {
		return fmt.Errorf("cached MetaServer serving certificate common name %q does not match %q", leaf.Subject.CommonName, expectedCommonName)
	}
	if !containsString(leaf.Subject.Organization, "system:nodes") {
		return fmt.Errorf("cached MetaServer serving certificate is not in system:nodes")
	}
	for _, ip := range scm.ips {
		if ip == nil {
			return fmt.Errorf("MetaServer serving certificate requires an invalid IP")
		}
		if err := leaf.VerifyHostname(ip.String()); err != nil {
			return fmt.Errorf("cached MetaServer serving certificate does not cover IP %s: %w", ip, err)
		}
	}

	intermediates := x509.NewCertPool()
	for _, raw := range current.Certificate[1:] {
		intermediate, err := x509.ParseCertificate(raw)
		if err != nil {
			return fmt.Errorf("parse cached MetaServer intermediate certificate: %w", err)
		}
		intermediates.AddCert(intermediate)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return fmt.Errorf("verify cached MetaServer serving certificate: %w", err)
	}
	return nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// WaitForCertReady waits until a valid serving certificate for this node and
// the configured MetaServer IPs is available. It is intentionally bounded by
// the EdgeCore lifecycle context rather than a startup timeout: losing cloud
// connectivity must not terminate EdgeCore.
func (scm *ServerCertificateManager) WaitForCertReady(ctx context.Context) error {
	return wait.PollUntilContextCancel(ctx, scm.retryInterval, true, func(context.Context) (bool, error) {
		if err := scm.validateCurrentServingCertificate(); err != nil {
			klog.V(4).Infof("MetaServer serving certificate is not ready: %v", err)
			return false, nil
		}
		return true, nil
	})
}

// WaitForCAReady uses a valid cached CA and serving certificate immediately,
// allowing an already-provisioned edge node to start while offline. A node
// without trustworthy cached credentials waits for CloudCore without making
// the rest of EdgeCore exit or restart.
func (scm *ServerCertificateManager) WaitForCAReady(ctx context.Context) error {
	if err := scm.validateCurrentServingCertificate(); err == nil {
		klog.Infof("Using valid cached Kubernetes CA and MetaServer serving certificate for offline startup")
		return nil
	} else {
		klog.Infof("Cached MetaServer credentials cannot be used for offline startup: %v", err)
	}

	return wait.PollUntilContextCancel(ctx, scm.retryInterval, true, func(context.Context) (bool, error) {
		if !scm.cloudConnected() {
			return false, nil
		}
		content, err := scm.fetchCA()
		if err != nil {
			klog.Errorf("get k8s CA failed, %v", err)
			return false, nil
		}
		if err := scm.writeCAAtomically(content); err != nil {
			klog.Errorf("downloaded k8s CA is invalid, %v", err)
			return false, nil
		}
		scm.remoteCASynced.Store(true)
		return true, nil
	})
}

// RefreshCAWhenConnected refreshes the cached CA once connectivity returns.
// The refreshed file is used by the next MetaServer start; offline startup is
// never delayed by this best-effort refresh.
func (scm *ServerCertificateManager) RefreshCAWhenConnected(ctx context.Context) error {
	if scm.remoteCASynced.Load() {
		return nil
	}
	return wait.PollUntilContextCancel(ctx, scm.retryInterval, true, func(context.Context) (bool, error) {
		if !scm.cloudConnected() {
			return false, nil
		}
		content, err := scm.fetchCA()
		if err != nil {
			klog.Errorf("refresh k8s CA failed, %v", err)
			return false, nil
		}
		roots, err := newVerifiedCAPool(content)
		if err != nil {
			klog.Errorf("refreshed k8s CA is invalid, %v", err)
			return false, nil
		}
		if err := scm.validateCurrentServingCertificateWithRoots(roots); err != nil {
			klog.Warningf("Keep the cached Kubernetes CA because its replacement does not verify the current MetaServer serving certificate: %v", err)
			return true, nil
		}
		if err := scm.writeCAAtomically(content); err != nil {
			klog.Errorf("store refreshed k8s CA failed, %v", err)
			return false, nil
		}
		scm.remoteCASynced.Store(true)
		klog.Infof("Refreshed cached Kubernetes CA after CloudCore connection became available")
		return true, nil
	})
}

func (scm *ServerCertificateManager) writeCAAtomically(content []byte) error {
	if _, err := newVerifiedCAPool(content); err != nil {
		return fmt.Errorf("validate k8s CA certificate: %w", err)
	}
	if err := os.MkdirAll(scm.certDirectory, 0o755); err != nil {
		return fmt.Errorf("create certificate directory: %w", err)
	}

	temporary, err := os.CreateTemp(scm.certDirectory, ".ca.crt-*")
	if err != nil {
		return fmt.Errorf("create temporary k8s CA file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	defer temporary.Close()

	if err := temporary.Chmod(0o644); err != nil {
		return fmt.Errorf("set temporary k8s CA permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write temporary k8s CA file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary k8s CA file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary k8s CA file: %w", err)
	}
	if err := os.Rename(temporaryPath, scm.caPath()); err != nil {
		return fmt.Errorf("replace cached k8s CA file: %w", err)
	}
	return nil
}

func (scm *ServerCertificateManager) getKubeAPIServerCA() ([]byte, error) {
	msg := message.BuildMsg(modules.MetaGroup, "", modules.MetaManagerModuleName, model.ResourceTypeK8sCA, model.QueryOperation, nil)
	resp, err := beehivecontext.SendSync(modules.EdgeHubModuleName, *msg, 1*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("send sync message %s failed: %v", msg.GetResource(), err)
	}

	content, err := resp.GetContentData()
	if err != nil || content == nil {
		return nil, fmt.Errorf("parse message %s failed, err: %v", msg.GetResource(), err)
	}
	return content, nil
}

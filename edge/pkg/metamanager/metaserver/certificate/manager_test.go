/*
Copyright 2026 The KubeEdge Authors.

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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

var (
	testNodeName = types.NodeName("edge-node")
	testIPs      = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("169.254.30.10")}
)

type testCredentials struct {
	caPEM      []byte
	servingPEM []byte
}

type servingCertificateOptions struct {
	nodeName      types.NodeName
	ips           []net.IP
	organizations []string
	notBefore     time.Time
	notAfter      time.Time
	root          *testCertificateAuthority
}

type testCertificateAuthority struct {
	certificate *x509.Certificate
	privateKey  *ecdsa.PrivateKey
	pem         []byte
}

func newTestCertificateAuthority(t *testing.T, now time.Time) *testCertificateAuthority {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-root"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	raw, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	parsed, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	return &testCertificateAuthority{
		certificate: parsed,
		privateKey:  key,
		pem:         pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}),
	}
}

func newTestCredentials(t *testing.T, now time.Time, options servingCertificateOptions) testCredentials {
	t.Helper()

	if options.nodeName == "" {
		options.nodeName = testNodeName
	}
	if options.ips == nil {
		options.ips = testIPs
	}
	if options.organizations == nil {
		options.organizations = []string{"system:nodes"}
	}
	if options.notBefore.IsZero() {
		options.notBefore = now.Add(-time.Hour)
	}
	if options.notAfter.IsZero() {
		options.notAfter = now.Add(12 * time.Hour)
	}
	if options.root == nil {
		options.root = newTestCertificateAuthority(t, now)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate serving key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			CommonName:   "system:node:" + string(options.nodeName),
			Organization: append([]string(nil), options.organizations...),
		},
		NotBefore:   options.notBefore,
		NotAfter:    options.notAfter,
		IPAddresses: append([]net.IP(nil), options.ips...),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	raw, err := x509.CreateCertificate(rand.Reader, template, options.root.certificate, &key.PublicKey, options.root.privateKey)
	if err != nil {
		t.Fatalf("create serving certificate: %v", err)
	}
	keyRaw, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal serving key: %v", err)
	}
	servingPEM := append([]byte{}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})...)
	servingPEM = append(servingPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyRaw})...)
	return testCredentials{caPEM: options.root.pem, servingPEM: servingPEM}
}

func writeTestCredentials(t *testing.T, directory string, credentials testCredentials) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, caFileName), credentials.caPEM, 0o644); err != nil {
		t.Fatalf("write CA: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, pairNamePrefix+"-current.pem"), credentials.servingPEM, 0o600); err != nil {
		t.Fatalf("write serving certificate: %v", err)
	}
}

func newTestManager(t *testing.T, directory string, now time.Time) *ServerCertificateManager {
	t.Helper()
	manager, err := NewServerCertificateManager(nil, testNodeName, testIPs, directory)
	if err != nil {
		t.Fatalf("create certificate manager: %v", err)
	}
	manager.now = func() time.Time { return now }
	manager.retryInterval = time.Millisecond
	return manager
}

func TestValidateCurrentServingCertificate(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	tests := []struct {
		name        string
		credentials func(*testing.T) testCredentials
		wantError   string
	}{
		{
			name: "valid cached credentials",
			credentials: func(t *testing.T) testCredentials {
				return newTestCredentials(t, now, servingCertificateOptions{})
			},
		},
		{
			name: "certificate is not valid yet",
			credentials: func(t *testing.T) testCredentials {
				return newTestCredentials(t, now, servingCertificateOptions{
					notBefore: now.Add(time.Hour),
					notAfter:  now.Add(2 * time.Hour),
				})
			},
			wantError: "is not valid before",
		},
		{
			name: "wrong node identity",
			credentials: func(t *testing.T) testCredentials {
				return newTestCredentials(t, now, servingCertificateOptions{nodeName: "another-node"})
			},
			wantError: "does not match",
		},
		{
			name: "missing system nodes organization",
			credentials: func(t *testing.T) testCredentials {
				return newTestCredentials(t, now, servingCertificateOptions{organizations: []string{"another-group"}})
			},
			wantError: "is not in system:nodes",
		},
		{
			name: "missing configured IP",
			credentials: func(t *testing.T) testCredentials {
				return newTestCredentials(t, now, servingCertificateOptions{ips: testIPs[:1]})
			},
			wantError: "does not cover IP 169.254.30.10",
		},
		{
			name: "certificate signed by a different CA",
			credentials: func(t *testing.T) testCredentials {
				credentials := newTestCredentials(t, now, servingCertificateOptions{})
				credentials.caPEM = newTestCertificateAuthority(t, now).pem
				return credentials
			},
			wantError: "verify cached MetaServer serving certificate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			directory := t.TempDir()
			writeTestCredentials(t, directory, tt.credentials(t))
			manager := newTestManager(t, directory, now)
			err := manager.validateCurrentServingCertificate()
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("validate cached credentials: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("expected error containing %q, got %v", tt.wantError, err)
			}
		})
	}
}

func TestWaitForCAReadyUsesValidCacheWhileOffline(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	directory := t.TempDir()
	writeTestCredentials(t, directory, newTestCredentials(t, now, servingCertificateOptions{}))
	manager := newTestManager(t, directory, now)
	manager.cloudConnected = func() bool { return false }
	manager.fetchCA = func() ([]byte, error) {
		t.Fatal("CA fetch must not run while valid cached credentials are available")
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := manager.WaitForCAReady(ctx); err != nil {
		t.Fatalf("use valid cached credentials: %v", err)
	}
}

func TestWaitForCAReadyKeepsWaitingWithoutValidOfflineCache(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	manager := newTestManager(t, t.TempDir(), now)
	manager.cloudConnected = func() bool { return false }
	manager.fetchCA = func() ([]byte, error) {
		t.Fatal("CA fetch must not run while CloudCore is disconnected")
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := manager.WaitForCAReady(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected lifecycle context deadline, got %v", err)
	}
}

func TestWaitForCAReadyRetriesInvalidCloudResponse(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	directory := t.TempDir()
	manager := newTestManager(t, directory, now)
	manager.cloudConnected = func() bool { return true }
	validCA := newTestCertificateAuthority(t, now).pem
	var calls atomic.Int32
	manager.fetchCA = func() ([]byte, error) {
		if calls.Add(1) == 1 {
			return []byte("invalid CA"), nil
		}
		return validCA, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := manager.WaitForCAReady(ctx); err != nil {
		t.Fatalf("wait for a valid downloaded CA: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected two CA fetches, got %d", calls.Load())
	}
}

func TestRefreshCAWhenConnectedReplacesOnlyCompatibleCA(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	tests := []struct {
		name        string
		replacement func(*testing.T, *testCertificateAuthority) []byte
		wantReplace bool
	}{
		{
			name: "compatible CA",
			replacement: func(t *testing.T, root *testCertificateAuthority) []byte {
				bundle := append([]byte{}, root.pem...)
				return append(bundle, newTestCertificateAuthority(t, now).pem...)
			},
			wantReplace: true,
		},
		{
			name: "incompatible CA",
			replacement: func(t *testing.T, _ *testCertificateAuthority) []byte {
				return newTestCertificateAuthority(t, now).pem
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			directory := t.TempDir()
			root := newTestCertificateAuthority(t, now)
			credentials := newTestCredentials(t, now, servingCertificateOptions{root: root})
			writeTestCredentials(t, directory, credentials)
			manager := newTestManager(t, directory, now)
			manager.cloudConnected = func() bool { return true }
			replacement := tt.replacement(t, root)
			manager.fetchCA = func() ([]byte, error) { return replacement, nil }

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if err := manager.RefreshCAWhenConnected(ctx); err != nil {
				t.Fatalf("refresh CA: %v", err)
			}
			current, err := os.ReadFile(manager.caPath())
			if err != nil {
				t.Fatalf("read cached CA: %v", err)
			}
			if tt.wantReplace {
				if string(current) != string(replacement) {
					t.Fatal("compatible CA was not installed")
				}
				return
			}
			if string(current) != string(credentials.caPEM) {
				t.Fatal("incompatible CA replaced the cached CA")
			}
		})
	}
}

func TestWriteCAAtomicallyRejectsInvalidReplacement(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	directory := t.TempDir()
	manager := newTestManager(t, directory, now)
	original := newTestCertificateAuthority(t, now).pem
	if err := manager.writeCAAtomically(original); err != nil {
		t.Fatalf("write original CA: %v", err)
	}

	if err := manager.writeCAAtomically([]byte("invalid CA")); err == nil {
		t.Fatal("expected invalid CA replacement to be rejected")
	}
	current, err := os.ReadFile(manager.caPath())
	if err != nil {
		t.Fatalf("read cached CA: %v", err)
	}
	if string(current) != string(original) {
		t.Fatal("invalid replacement changed the cached CA")
	}

	serving := newTestCredentials(t, now, servingCertificateOptions{}).servingPEM
	if err := manager.writeCAAtomically(serving); err == nil || !strings.Contains(err.Error(), "non-CA certificate") {
		t.Fatalf("expected a serving certificate to be rejected as a CA, got %v", err)
	}
	current, err = os.ReadFile(manager.caPath())
	if err != nil {
		t.Fatalf("read cached CA after serving certificate rejection: %v", err)
	}
	if string(current) != string(original) {
		t.Fatal("non-CA replacement changed the cached CA")
	}
}

func TestWaitForCertReadyRejectsMismatchedCachedIdentity(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	directory := t.TempDir()
	writeTestCredentials(t, directory, newTestCredentials(t, now, servingCertificateOptions{nodeName: "another-node"}))
	manager := newTestManager(t, directory, now)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := manager.WaitForCertReady(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected lifecycle context deadline, got %v", err)
	}
}

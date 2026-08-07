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

package metaserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
)

func TestVerifiedClientCertificateAuthenticator(t *testing.T) {
	cert := &x509.Certificate{
		Raw: []byte("node-certificate"),
		Subject: pkix.Name{
			CommonName:   "system:node:edge-node",
			Organization: []string{"system:nodes"},
		},
	}
	req := httptest.NewRequest("POST", "https://127.0.0.1:10550/api/v1/taskupgrade/confirm-upgrade", nil)
	req.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert}}}

	response, ok, err := (&verifiedClientCertificateAuthenticator{}).AuthenticateRequest(req)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, cert.Subject.CommonName, response.User.GetName())
	require.Equal(t, cert.Subject.Organization, response.User.GetGroups())
	require.Contains(t, response.User.GetExtra()[user.CredentialIDKey][0], "X509SHA256=")

	unverifiedReq := httptest.NewRequest("POST", "https://127.0.0.1:10550/api/v1/taskupgrade/confirm-upgrade", nil)
	unverifiedReq.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	response, ok, err = (&verifiedClientCertificateAuthenticator{}).AuthenticateRequest(unverifiedReq)
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, response)
}

func TestLocalNodeUpgradeConfirmAuthorizer(t *testing.T) {
	x509User := &user.DefaultInfo{
		Name:   "system:node:edge-node",
		Groups: []string{"system:nodes"},
		Extra: map[string][]string{
			user.CredentialIDKey: {"X509SHA256=0123456789abcdef"},
		},
	}
	base := authorizer.AttributesRecord{
		User:            x509User,
		Verb:            "create",
		APIGroup:        "",
		APIVersion:      "v1",
		Resource:        "taskupgrade",
		Subresource:     "",
		Name:            "confirm-upgrade",
		Namespace:       "",
		ResourceRequest: true,
	}
	authorizerUnderTest := &localNodeUpgradeConfirmAuthorizer{nodeName: "edge-node"}

	decision, _, err := authorizerUnderTest.Authorize(context.Background(), base)
	require.NoError(t, err)
	require.Equal(t, authorizer.DecisionAllow, decision)

	tests := map[string]func(*authorizer.AttributesRecord){
		"wrong node": func(attrs *authorizer.AttributesRecord) {
			attrs.User = &user.DefaultInfo{Name: "system:node:other", Groups: []string{"system:nodes"}, Extra: x509User.Extra}
		},
		"missing node group": func(attrs *authorizer.AttributesRecord) {
			attrs.User = &user.DefaultInfo{Name: x509User.Name, Extra: x509User.Extra}
		},
		"not an x509 identity": func(attrs *authorizer.AttributesRecord) {
			attrs.User = &user.DefaultInfo{Name: x509User.Name, Groups: x509User.Groups}
		},
		"wrong verb":        func(attrs *authorizer.AttributesRecord) { attrs.Verb = "get" },
		"wrong API group":   func(attrs *authorizer.AttributesRecord) { attrs.APIGroup = "upgrade.kubeedge.io" },
		"wrong API version": func(attrs *authorizer.AttributesRecord) { attrs.APIVersion = "v1beta1" },
		"wrong resource":    func(attrs *authorizer.AttributesRecord) { attrs.Resource = "nodes" },
		"wrong name":        func(attrs *authorizer.AttributesRecord) { attrs.Name = "other" },
		"with subresource":  func(attrs *authorizer.AttributesRecord) { attrs.Subresource = "confirm-upgrade" },
		"with namespace":    func(attrs *authorizer.AttributesRecord) { attrs.Namespace = "default" },
		"non-resource":      func(attrs *authorizer.AttributesRecord) { attrs.ResourceRequest = false },
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			attrs := base
			mutate(&attrs)
			decision, _, err := authorizerUnderTest.Authorize(context.Background(), attrs)
			require.NoError(t, err)
			require.Equal(t, authorizer.DecisionNoOpinion, decision)
		})
	}
}

func TestNewClientCAPoolLoadsEveryCA(t *testing.T) {
	tempDir := t.TempDir()
	firstCA := filepath.Join(tempDir, "first-ca.crt")
	secondCA := filepath.Join(tempDir, "second-ca.crt")
	require.NoError(t, os.WriteFile(firstCA, newTestCAPEM(t, "first-ca"), 0600))
	require.NoError(t, os.WriteFile(secondCA, newTestCAPEM(t, "second-ca"), 0600))

	pool, err := newClientCAPool(firstCA, secondCA)
	require.NoError(t, err)
	require.Len(t, pool.Subjects(), 2)
}

func newTestCAPEM(t *testing.T, commonName string) []byte {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

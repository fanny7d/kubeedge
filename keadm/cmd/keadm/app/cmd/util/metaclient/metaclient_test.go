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

package metaclient

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubeedge/api/apis/common/constants"
	cfgv1alpha2 "github.com/kubeedge/api/apis/componentconfig/edgecore/v1alpha2"
)

func TestGetKubeConfigWithConfigUsesMetaServerServingCA(t *testing.T) {
	config := &cfgv1alpha2.EdgeCoreConfig{
		FeatureGates: map[string]bool{"requireAuthorization": true},
		Modules: &cfgv1alpha2.Modules{
			MetaManager: &cfgv1alpha2.MetaManager{
				MetaServer: &cfgv1alpha2.MetaServer{
					Enable:            true,
					Server:            "127.0.0.1:10550",
					TLSCaFile:         "/etc/kubeedge/ca/rootCA.crt",
					TLSCertFile:       "/etc/kubeedge/certs/server.crt",
					TLSPrivateKeyFile: "/etc/kubeedge/certs/server.key",
				},
			},
		},
	}

	kubeConfig, err := GetKubeConfigWithConfig(config)
	require.NoError(t, err)
	require.Equal(t, "https://127.0.0.1:10550", kubeConfig.Host)
	require.Equal(t, filepath.Join(constants.KubeEdgePath, "pki", "metaserver", "ca.crt"), kubeConfig.CAFile)
	require.Equal(t, config.Modules.MetaManager.MetaServer.TLSCertFile, kubeConfig.CertFile)
	require.Equal(t, config.Modules.MetaManager.MetaServer.TLSPrivateKeyFile, kubeConfig.KeyFile)
}

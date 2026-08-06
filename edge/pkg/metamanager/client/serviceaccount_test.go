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

package client

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubeedge/api/apis/common/constants"
	metaserverconfig "github.com/kubeedge/kubeedge/edge/pkg/metamanager/metaserver/config"
)

func TestKeyFuncNormalizesDefaultAudiences(t *testing.T) {
	originalIssuers := append([]string(nil), metaserverconfig.Config.ServiceAccountIssuers...)
	originalAudiences := append([]string(nil), metaserverconfig.Config.APIAudiences...)
	t.Cleanup(func() {
		metaserverconfig.Config.ServiceAccountIssuers = originalIssuers
		metaserverconfig.Config.APIAudiences = originalAudiences
	})

	expirationSeconds := int64(3600)
	request := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			ExpirationSeconds: &expirationSeconds,
			BoundObjectRef: &authenticationv1.BoundObjectReference{
				Kind:       "Pod",
				APIVersion: corev1.SchemeGroupVersion.String(),
				Name:       "test-pod",
				UID:        types.UID("test-uid"),
			},
		},
	}

	t.Run("configured API audience", func(t *testing.T) {
		issuer := "https://issuer.example.com"
		audience := "https://kubernetes.default.svc"
		metaserverconfig.Config.ServiceAccountIssuers = []string{issuer}
		metaserverconfig.Config.APIAudiences = []string{audience}
		response := request.DeepCopy()
		response.Spec.Audiences = []string{audience}
		emptyRequest := request.DeepCopy()
		emptyRequest.Spec.Audiences = []string{}

		assert.Equal(t,
			KeyFunc("default", "default", response),
			KeyFunc("default", "default", request),
		)
		assert.Equal(t,
			KeyFunc("default", "default", response),
			KeyFunc("default", "default", emptyRequest),
		)
		assert.Nil(t, request.Spec.Audiences)
		assert.NotNil(t, emptyRequest.Spec.Audiences)
	})

	t.Run("issuer fallback", func(t *testing.T) {
		issuer := "https://kubernetes.default.svc"
		metaserverconfig.Config.ServiceAccountIssuers = []string{issuer}
		metaserverconfig.Config.APIAudiences = nil
		response := request.DeepCopy()
		response.Spec.Audiences = []string{issuer}

		assert.Equal(t,
			KeyFunc("default", "default", response),
			KeyFunc("default", "default", request),
		)
	})

	t.Run("default issuer fallback", func(t *testing.T) {
		metaserverconfig.Config.ServiceAccountIssuers = nil
		metaserverconfig.Config.APIAudiences = nil
		response := request.DeepCopy()
		response.Spec.Audiences = []string{constants.DefaultServiceAccountIssuer}

		assert.Equal(t,
			KeyFunc("default", "default", response),
			KeyFunc("default", "default", request),
		)
	})

	t.Run("explicit audience", func(t *testing.T) {
		metaserverconfig.Config.ServiceAccountIssuers = []string{"https://issuer.example.com"}
		metaserverconfig.Config.APIAudiences = []string{"https://kubernetes.default.svc"}
		explicit := request.DeepCopy()
		explicit.Spec.Audiences = []string{"custom-audience"}

		assert.NotEqual(t,
			KeyFunc("default", "default", request),
			KeyFunc("default", "default", explicit),
		)
		require.Equal(t, []string{"custom-audience"}, explicit.Spec.Audiences)
	})
}

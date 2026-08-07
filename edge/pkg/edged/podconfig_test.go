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

package edged

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/pkg/kubelet/config"
	kubelettypes "k8s.io/kubernetes/pkg/kubelet/types"
)

func TestPodConfigWaitsForConsumedInitialSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	podConfig := config.NewPodConfig(config.PodConfigNotificationIncremental, nil, nil)
	podConfig.Channel(ctx, kubelettypes.ApiserverSource)
	podConfig.SetInitPodReady(true)

	if podConfig.SeenAllSources(sets.New[string]()) {
		t.Fatal("pod sources became ready before kubelet consumed the initial source update")
	}

	seenSources := sets.New[string](kubelettypes.ApiserverSource)
	if !podConfig.SeenAllSources(seenSources) {
		t.Fatal("pod sources did not become ready after kubelet consumed the initial source update")
	}

	podConfig.SetInitPodReady(false)
	if podConfig.SeenAllSources(seenSources) {
		t.Fatal("pod sources remained ready after initial pod readiness was cleared")
	}
}

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

package imitator

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/klog/v2"

	edgecorev1alpha2 "github.com/kubeedge/api/apis/componentconfig/edgecore/v1alpha2"
	policyv1alpha1 "github.com/kubeedge/api/apis/policy/v1alpha1"
	"github.com/kubeedge/beehive/pkg/core/model"
	cloudmodules "github.com/kubeedge/kubeedge/cloud/pkg/common/modules"
	edgemodules "github.com/kubeedge/kubeedge/edge/pkg/common/modules"
	"github.com/kubeedge/kubeedge/edge/pkg/metamanager/dao"
	"github.com/kubeedge/kubeedge/edge/pkg/metamanager/dao/dbclient"
	"github.com/kubeedge/kubeedge/pkg/metaserver"
)

var initTestDB sync.Once

func resetTestMetaV2(t *testing.T) {
	t.Helper()
	initTestDB.Do(func() {
		dao.Init("file:imitator-tests?mode=memory&cache=shared", &edgecorev1alpha2.MetaManager{Enable: true})
	})
	require.NoError(t, dao.GetDB().Exec("DELETE FROM meta_v2").Error)
}

func newTestPod(name string, uid types.UID) *unstructured.Unstructured {
	pod := &unstructured.Unstructured{}
	pod.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Pod"))
	pod.SetNamespace("default")
	pod.SetName(name)
	pod.SetUID(uid)
	pod.SetResourceVersion("1")
	return pod
}

func TestEventSkipsEdgeOriginatedPodDeleteRequest(t *testing.T) {
	state := klog.CaptureState()
	t.Cleanup(state.Restore)
	var errorLogs bytes.Buffer
	klog.LogToStderr(false)
	klog.SetOutputBySeverity("ERROR", &errorLogs)

	zero := int64(0)
	msg := model.NewMessage("").
		BuildRouter(edgemodules.EdgedModuleName, "resource", "default/pod/test-pod", model.DeleteOperation).
		FillBody(metav1.DeleteOptions{
			GracePeriodSeconds: &zero,
			Preconditions:      metav1.NewUIDPreconditions("old-pod-uid"),
		})

	client := newV2Client().(*imitator)
	assert.Empty(t, client.Event(msg))
	klog.Flush()
	assert.Empty(t, errorLogs.String())
}

func TestEventSkipsMessagesThatAreNotMetaServerWatchEvents(t *testing.T) {
	state := klog.CaptureState()
	t.Cleanup(state.Restore)
	var errorLogs bytes.Buffer
	klog.LogToStderr(false)
	klog.SetOutputBySeverity("ERROR", &errorLogs)

	tests := []struct {
		name string
		msg  *model.Message
	}{
		{
			name: "edge-originated node request",
			msg: model.NewMessage("").
				BuildRouter(edgemodules.EdgedModuleName, "resource", "default/node/test-node", model.UpdateOperation).
				FillBody(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "test-node"}}),
		},
		{
			name: "service account access snapshot",
			msg: model.NewMessage("").
				BuildRouter(cloudmodules.PolicyControllerModuleName, "resource", "default/serviceaccountaccess/default", model.UpdateOperation).
				FillBody(&policyv1alpha1.ServiceAccountAccess{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "default"}}),
		},
	}

	client := newV2Client().(*imitator)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Empty(t, client.Event(tt.msg))
		})
	}
	klog.Flush()
	assert.Empty(t, errorLogs.String())
}

func TestEventAcceptsAuthoritativePodTombstone(t *testing.T) {
	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       "Pod",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-pod",
			UID:       types.UID("old-pod-uid"),
		},
	}
	msg := model.NewMessage("").
		BuildRouter(cloudmodules.EdgeControllerModuleName, "resource", "default/pod/test-pod", model.DeleteOperation).
		FillBody(pod)

	client := newV2Client().(*imitator)
	events := client.Event(msg)
	require.Len(t, events, 1)
	assert.Equal(t, watch.Deleted, events[0].Type)
	deleted, ok := events[0].Object.(*unstructured.Unstructured)
	require.True(t, ok)
	assert.Equal(t, corev1.SchemeGroupVersion.WithKind("Pod"), deleted.GroupVersionKind())
	assert.Equal(t, pod.UID, deleted.GetUID())
}

func TestEventAcceptsAuthoritativeCloudNode(t *testing.T) {
	node := &corev1.Node{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       "Node",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
	}
	msg := model.NewMessage("").
		BuildRouter(cloudmodules.EdgeControllerModuleName, "resource", "default/node/test-node", model.UpdateOperation).
		FillBody(node)

	client := newV2Client().(*imitator)
	events := client.Event(msg)
	require.Len(t, events, 1)
	assert.Equal(t, watch.Modified, events[0].Type)
	updated, ok := events[0].Object.(*unstructured.Unstructured)
	require.True(t, ok)
	assert.Equal(t, corev1.SchemeGroupVersion.WithKind("Node"), updated.GroupVersionKind())
}

func TestDeleteObjHonorsUID(t *testing.T) {
	resetTestMetaV2(t)
	client := newV2Client().(*imitator)
	current := newTestPod("reused-name", types.UID("current-uid"))
	require.NoError(t, client.InsertOrUpdateObj(context.Background(), current))

	key, err := metaserver.KeyFuncObj(current)
	require.NoError(t, err)

	deleted, err := client.deleteObj(context.Background(), newTestPod("reused-name", types.UID("stale-uid")))
	require.NoError(t, err)
	assert.False(t, deleted)
	_, found, err := dbclient.NewMetaV2Service().FindByKey(key)
	require.NoError(t, err)
	assert.True(t, found, "a delayed delete must not remove a same-name replacement")

	deleted, err = client.deleteObj(context.Background(), newTestPod("reused-name", ""))
	require.NoError(t, err)
	assert.False(t, deleted)
	_, found, err = dbclient.NewMetaV2Service().FindByKey(key)
	require.NoError(t, err)
	assert.True(t, found, "an empty UID must not bypass the replacement guard")

	deleted, err = client.deleteObj(context.Background(), current.DeepCopy())
	require.NoError(t, err)
	assert.True(t, deleted)
	_, found, err = dbclient.NewMetaV2Service().FindByKey(key)
	require.NoError(t, err)
	assert.False(t, found)

	deleted, err = client.deleteObj(context.Background(), current.DeepCopy())
	require.NoError(t, err)
	assert.False(t, deleted, "repeated deletes must be idempotent")
}

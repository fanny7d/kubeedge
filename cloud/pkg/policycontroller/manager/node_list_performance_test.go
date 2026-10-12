package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	policyv1alpha1 "github.com/kubeedge/api/apis/policy/v1alpha1"
	commonconstants "github.com/kubeedge/kubeedge/common/constants"
)

type nodeListCountingReader struct {
	client.Reader
	nodeLists int
	podLists  int
	nodeError error
	podError  error
}

func (r *nodeListCountingReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	switch list.(type) {
	case *corev1.NodeList:
		r.nodeLists++
		if r.nodeError != nil {
			return r.nodeError
		}
	case *corev1.PodList:
		r.podLists++
		if r.podError != nil {
			return r.podError
		}
	}
	return r.Reader.List(ctx, list, opts...)
}

func TestNodeListSnapshotMembership(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	node := func(name, role string, ready corev1.ConditionStatus) *corev1.Node {
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{commonconstants.EdgeNodeRoleKey: role}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}}}}
	}
	pod := func(name, ns, sa, n string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: corev1.PodSpec{ServiceAccountName: sa, NodeName: n}}
	}
	objects := []client.Object{
		node("edge-online", commonconstants.EdgeNodeRoleValue, corev1.ConditionTrue),
		node("edge-offline", commonconstants.EdgeNodeRoleValue, corev1.ConditionUnknown),
		node("cloud", "not-edge", corev1.ConditionTrue),
		pod("a", "ns", "sa", "edge-online"), pod("b", "ns", "sa", "edge-online"),
		pod("c", "ns", "sa", "edge-offline"), pod("d", "ns", "sa", "cloud"),
		pod("e", "ns", "sa", "missing"), pod("f", "ns", "sa", ""),
		pod("g", "ns", "other-sa", "edge-online"), pod("h", "other-ns", "sa", "edge-online"),
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithIndex(&corev1.Pod{}, podServiceAccountNameField, func(o client.Object) []string { return []string{o.(*corev1.Pod).Spec.ServiceAccountName} }).Build()
	r := &nodeListCountingReader{Reader: c}
	acc := &policyv1alpha1.ServiceAccountAccess{ObjectMeta: metav1.ObjectMeta{Namespace: "ns"}, Spec: policyv1alpha1.AccessSpec{ServiceAccount: corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "sa"}}}}
	got, err := getNodeListOfServiceAccountAccess(ctx, r, acc)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"edge-online", "edge-offline"}) {
		t.Fatalf("nodes = %v", got)
	}
	if r.nodeLists != 1 || r.podLists != 1 {
		t.Fatalf("List calls: Nodes=%d Pods=%d", r.nodeLists, r.podLists)
	}
	// A later reconcile must see changed role labels; no process-wide stale snapshot.
	n := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "edge-online"}, n); err != nil {
		t.Fatal(err)
	}
	n.Labels[commonconstants.EdgeNodeRoleKey] = "not-edge"
	if err := c.Update(ctx, n); err != nil {
		t.Fatal(err)
	}
	got, err = getNodeListOfServiceAccountAccess(ctx, r, acc)
	if err != nil || !reflect.DeepEqual(got, []string{"edge-offline"}) {
		t.Fatalf("fresh snapshot = %v, %v", got, err)
	}
}

func TestNodeListSnapshotErrorsAndUnscheduledPods(t *testing.T) {
	boom := errors.New("API unavailable")
	for _, tc := range []struct {
		name, node      string
		podErr, nodeErr error
		wantLists       int
	}{
		{name: "pod list error", node: "edge", podErr: boom},
		{name: "node list error", node: "edge", nodeErr: boom, wantLists: 1},
		{name: "unscheduled", node: "", nodeErr: boom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &snapshotNodeListReader{pods: corev1.PodList{Items: []corev1.Pod{{Spec: corev1.PodSpec{NodeName: tc.node}}}}}
			reader := &nodeListCountingReader{Reader: r, nodeError: tc.nodeErr, podError: tc.podErr}
			acc := &policyv1alpha1.ServiceAccountAccess{}
			got, err := getNodeListOfServiceAccountAccess(context.Background(), reader, acc)
			if tc.node != "" && !errors.Is(err, boom) {
				t.Fatalf("error = %v, want %v", err, boom)
			}
			if tc.node == "" && (err != nil || len(got) != 0) {
				t.Fatalf("unscheduled result = %v, %v", got, err)
			}
			if reader.nodeLists != tc.wantLists {
				t.Fatalf("Node Lists=%d, want %d", reader.nodeLists, tc.wantLists)
			}
		})
	}
}

func TestNodeListFailurePreservesExistingAccess(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := policyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "sa", Namespace: "ns", UID: types.UID("sa-uid")}}
	acc := &policyv1alpha1.ServiceAccountAccess{ObjectMeta: metav1.ObjectMeta{Name: "sa", Namespace: "ns", UID: types.UID("access-uid")}, Spec: policyv1alpha1.AccessSpec{ServiceAccount: *sa, ServiceAccountUID: sa.UID}, Status: policyv1alpha1.AccessStatus{NodeList: []string{"edge"}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns"}, Spec: corev1.PodSpec{ServiceAccountName: "sa", NodeName: "edge"}}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sa, acc, pod).WithStatusSubresource(acc).WithIndex(&corev1.Pod{}, podServiceAccountNameField, func(o client.Object) []string { return []string{o.(*corev1.Pod).Spec.ServiceAccountName} }).Build()
	boom := errors.New("Node List temporarily unavailable")
	r := &nodeListCountingReader{Reader: c, nodeError: boom}
	ml := &recordingMessageLayer{}
	ctr := &Controller{Client: c, Reader: r, MessageLayer: ml}
	_, err := ctr.Reconcile(context.Background(), controllerruntime.Request{NamespacedName: client.ObjectKeyFromObject(acc)})
	if !errors.Is(err, boom) {
		t.Fatalf("Reconcile error = %v", err)
	}
	persisted := &policyv1alpha1.ServiceAccountAccess{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(acc), persisted); err != nil {
		t.Fatalf("existing access removed: %v", err)
	}
	if !reflect.DeepEqual(persisted.Spec, acc.Spec) || !reflect.DeepEqual(persisted.Status, acc.Status) {
		t.Fatalf("access changed after failed Node List: %+v", persisted)
	}
	if len(ml.messages) != 0 {
		t.Fatalf("unexpected policy delivery after failed Node List: %v", ml.messages)
	}
}

// Models independent typed objects returned by each API List, without network
// latency, JSON decoding, Kubernetes access, or shared cache pointers.
type snapshotNodeListReader struct {
	client.Reader
	pods      corev1.PodList
	nodes     corev1.NodeList
	nodeLists int
}

func (r *snapshotNodeListReader) List(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
	switch dst := list.(type) {
	case *corev1.PodList:
		r.pods.DeepCopyInto(dst)
	case *corev1.NodeList:
		r.nodeLists++
		r.nodes.DeepCopyInto(dst)
	default:
		return fmt.Errorf("unexpected List %T", list)
	}
	return nil
}

func BenchmarkGetNodeListOfServiceAccountAccess(b *testing.B) {
	for _, size := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("nodes_%d", size), func(b *testing.B) {
			r := &snapshotNodeListReader{}
			for i := 0; i < 1052; i++ {
				labels := map[string]string{commonconstants.EdgeNodeRoleKey: commonconstants.EdgeNodeRoleValue}
				for j := 0; j < 12; j++ {
					labels[fmt.Sprintf("label-%d", j)] = fmt.Sprintf("value-%d", j)
				}
				r.nodes.Items = append(r.nodes.Items, corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("edge-%04d", i), Labels: labels, Annotations: map[string]string{"configuration": strings.Repeat("x", 512)}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}})
			}
			for i := 0; i < size; i++ {
				r.pods.Items = append(r.pods.Items, corev1.Pod{Spec: corev1.PodSpec{NodeName: r.nodes.Items[i].Name, ServiceAccountName: "sa"}})
			}
			acc := &policyv1alpha1.ServiceAccountAccess{Spec: policyv1alpha1.AccessSpec{ServiceAccount: corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "sa"}}}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				nodes, err := getNodeListOfServiceAccountAccess(context.Background(), r, acc)
				if err != nil || len(nodes) != size {
					b.Fatalf("nodes=%d error=%v", len(nodes), err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(r.nodeLists)/float64(b.N), "node_lists/op")
		})
	}
}

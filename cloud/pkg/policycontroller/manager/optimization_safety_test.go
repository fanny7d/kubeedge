package controller

import (
	"context"
	"errors"
	"reflect"
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
	syncv1alpha1 "github.com/kubeedge/api/apis/reliablesyncs/v1alpha1"
	"github.com/kubeedge/beehive/pkg/core/model"
	"github.com/kubeedge/kubeedge/cloud/pkg/common/messagelayer"
)

func newOptimizationSafetyFixture(t *testing.T) (*Controller, client.Client, *policyv1alpha1.ServiceAccountAccess, *recordingMessageLayer) {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, rbacv1.AddToScheme, policyv1alpha1.AddToScheme, syncv1alpha1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "sa", Namespace: "ns", UID: types.UID("sa-uid")}}
	acc := &policyv1alpha1.ServiceAccountAccess{ObjectMeta: metav1.ObjectMeta{Name: "sa", Namespace: "ns", UID: types.UID("access-uid")}, Spec: policyv1alpha1.AccessSpec{ServiceAccount: *sa.DeepCopy(), ServiceAccountUID: sa.UID}, Status: policyv1alpha1.AccessStatus{NodeList: []string{"edge"}}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "edge", UID: types.UID("node-uid"), Labels: map[string]string{"node-role.kubernetes.io/edge": ""}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionUnknown}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns"}, Spec: corev1.PodSpec{NodeName: "edge", ServiceAccountName: "sa"}}
	cli := fake.NewClientBuilder().WithScheme(s).WithObjects(sa, acc, node, pod).
		WithStatusSubresource(acc, node).
		WithIndex(&corev1.Pod{}, podServiceAccountNameField, func(o client.Object) []string { return []string{o.(*corev1.Pod).Spec.ServiceAccountName} }).
		WithIndex(&corev1.Pod{}, podNodeNameField, func(o client.Object) []string { return []string{o.(*corev1.Pod).Spec.NodeName} }).Build()
	recorder := &recordingMessageLayer{}
	return &Controller{Client: cli, Reader: cli, MessageLayer: recorder}, cli, acc, recorder
}

func reconcileSafetyAccess(t *testing.T, c *Controller, acc *policyv1alpha1.ServiceAccountAccess) controllerruntime.Result {
	t.Helper()
	res, err := c.Reconcile(context.Background(), controllerruntime.Request{NamespacedName: client.ObjectKeyFromObject(acc)})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestOptimizationAuthorizationGrantAndRevokeWhileOffline(t *testing.T) {
	c, cli, acc, recorder := newOptimizationSafetyFixture(t)
	ctx := context.Background()
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: "ns"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get"}}}}
	rb := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: "ns"}, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "reader"}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: "ns", Name: "sa"}}}
	for _, o := range []client.Object{role, rb} {
		if err := cli.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	for _, phase := range []string{"grant", "revoke"} {
		if phase == "revoke" {
			if err := cli.Delete(ctx, rb); err != nil {
				t.Fatal(err)
			}
		}
		recorder.messages = nil
		reconcileSafetyAccess(t, c, acc)
		if len(recorder.messages) != 1 || recorder.messages[0].GetOperation() != model.UpdateOperation {
			t.Fatalf("%s messages: %+v", phase, recorder.messages)
		}
		msg := recorder.messages[0]
		target, err := messagelayer.GetNodeID(msg)
		if err != nil || target != "edge" {
			t.Fatalf("%s target=%s err=%v", phase, target, err)
		}
		body := msg.GetContent().(*policyv1alpha1.ServiceAccountAccess)
		want := 1
		if phase == "revoke" {
			want = 0
		}
		if len(body.Spec.AccessRoleBinding) != want {
			t.Fatalf("%s bindings=%v", phase, body.Spec.AccessRoleBinding)
		}
		if phase == "grant" && !reflect.DeepEqual(body.Spec.AccessRoleBinding[0].Rules, role.Rules) {
			t.Fatal("grant changed RBAC rules")
		}
		current := &policyv1alpha1.ServiceAccountAccess{}
		if err := cli.Get(ctx, client.ObjectKeyFromObject(acc), current); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(current.Status.NodeList, []string{"edge"}) || current.Spec.ServiceAccountUID != types.UID("sa-uid") {
			t.Fatalf("%s lost offline membership or identity", phase)
		}
	}
}

func TestOptimizationOfflineRepairAndReconnect(t *testing.T) {
	c, cli, acc, recorder := newOptimizationSafetyFixture(t)
	ctx := context.Background()
	// Offline membership must still repair a missing current-UID ObjectSync.
	if !reconcileSafetyAccess(t, c, acc).Requeue {
		t.Fatal("missing ObjectSync did not requeue")
	}
	if len(recorder.messages) != 1 || recorder.messages[0].GetOperation() != model.InsertOperation {
		t.Fatalf("repair messages=%v", recorder.messages)
	}
	// Simulate the shared ObjectSync persisted by CloudHub while no session exists.
	os := newTestServiceAccountAccessObjectSync(acc, "edge", "0")
	if err := cli.Create(ctx, os); err != nil {
		t.Fatal(err)
	}
	for _, state := range []corev1.ConditionStatus{corev1.ConditionUnknown, corev1.ConditionFalse, corev1.ConditionTrue} {
		node := &corev1.Node{}
		if err := cli.Get(ctx, client.ObjectKey{Name: "edge"}, node); err != nil {
			t.Fatal(err)
		}
		node.Status.Conditions[0].Status = state
		if err := cli.Status().Update(ctx, node); err != nil {
			t.Fatal(err)
		}
		recorder.messages = nil
		if res := reconcileSafetyAccess(t, c, acc); res.Requeue || len(recorder.messages) != 0 {
			t.Fatalf("state=%s resent complete in-flight ObjectSync: %v", state, recorder.messages)
		}
		current := &policyv1alpha1.ServiceAccountAccess{}
		if err := cli.Get(ctx, client.ObjectKeyFromObject(acc), current); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(current.Status.NodeList, []string{"edge"}) {
			t.Fatal("readiness changed authorization membership")
		}
	}
	// Same node name, new Node UID and a missing delivery record must repair again.
	node := &corev1.Node{}
	if err := cli.Get(ctx, client.ObjectKey{Name: "edge"}, node); err != nil {
		t.Fatal(err)
	}
	if err := cli.Delete(ctx, node); err != nil {
		t.Fatal(err)
	}
	node.ResourceVersion = ""
	node.UID = types.UID("recreated-node-uid")
	if err := cli.Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := cli.Delete(ctx, os); err != nil {
		t.Fatal(err)
	}
	requests := c.mapNodeFunc(ctx, node)
	if len(requests) != 1 || requests[0].NamespacedName != client.ObjectKeyFromObject(acc) {
		t.Fatalf("recreate mapping=%v", requests)
	}
	recorder.messages = nil
	if !reconcileSafetyAccess(t, c, acc).Requeue || len(recorder.messages) != 1 {
		t.Fatal("recreated node did not get missing-record repair")
	}
}

func TestOptimizationNodeListFailureDefersRevocationUntilRecovery(t *testing.T) {
	c, cli, acc, recorder := newOptimizationSafetyFixture(t)
	ctx := context.Background()
	current := &policyv1alpha1.ServiceAccountAccess{}
	if err := cli.Get(ctx, client.ObjectKeyFromObject(acc), current); err != nil {
		t.Fatal(err)
	}
	current.Spec.AccessRoleBinding = []policyv1alpha1.AccessRoleBinding{{RoleBinding: rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "removed-binding", Namespace: "ns"}}, Rules: []rbacv1.PolicyRule{{Resources: []string{"configmaps"}, Verbs: []string{"get"}}}}}
	if err := cli.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("Node API unavailable")
	reader := &nodeListCountingReader{Reader: cli, nodeError: boom}
	c.Reader = reader
	_, err := c.Reconcile(ctx, controllerruntime.Request{NamespacedName: client.ObjectKeyFromObject(acc)})
	if !errors.Is(err, boom) || len(recorder.messages) != 0 {
		t.Fatalf("API failure should retry without delivery: %v %+v", err, recorder.messages)
	}
	preserved := &policyv1alpha1.ServiceAccountAccess{}
	if err := cli.Get(ctx, client.ObjectKeyFromObject(acc), preserved); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(current.Spec, preserved.Spec) {
		t.Fatal("partially updated authorization on failed node snapshot")
	}
	reader.nodeError = nil
	reconcileSafetyAccess(t, c, acc)
	if len(recorder.messages) != 1 || recorder.messages[0].GetOperation() != model.UpdateOperation {
		t.Fatal("revocation not delivered on retry")
	}
	if len(recorder.messages[0].GetContent().(*policyv1alpha1.ServiceAccountAccess).Spec.AccessRoleBinding) != 0 {
		t.Fatal("revocation retained removed rule")
	}
}

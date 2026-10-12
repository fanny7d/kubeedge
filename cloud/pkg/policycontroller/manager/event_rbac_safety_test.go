package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	cr "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	policy "github.com/kubeedge/api/apis/policy/v1alpha1"
	"github.com/kubeedge/beehive/pkg/core/model"
)

func TestPodEventPredicateRelevantChanges(t *testing.T) {
	base := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "pod", UID: types.UID("uid")}, Spec: corev1.PodSpec{NodeName: "edge", ServiceAccountName: "sa"}}
	for _, tc := range []struct {
		name   string
		change func(*corev1.Pod)
		want   bool
	}{
		{"unchanged", func(*corev1.Pod) {}, false},
		{"resourceVersion", func(p *corev1.Pod) { p.ResourceVersion = "2" }, false},
		{"readiness", func(p *corev1.Pod) {
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
		}, false},
		{"container restart", func(p *corev1.Pod) {
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "inference1", RestartCount: 10}}
		}, false},
		{"labels", func(p *corev1.Pod) { p.Labels = map[string]string{"app": "changed"} }, false},
		{"node", func(p *corev1.Pod) { p.Spec.NodeName = "another-edge" }, true},
		{"serviceaccount", func(p *corev1.Pod) { p.Spec.ServiceAccountName = "another-sa" }, true},
		{"deleting", func(p *corev1.Pod) { now := metav1.Now(); p.DeletionTimestamp = &now }, true},
		{"identity", func(p *corev1.Pod) { p.UID = "new-uid" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := base.DeepCopy()
			tc.change(next)
			if got := podEventPredicate().Update(event.UpdateEvent{ObjectOld: base, ObjectNew: next}); got != tc.want {
				t.Fatalf("predicate=%v want=%v", got, tc.want)
			}
		})
	}
	p := podEventPredicate()
	if !p.Create(event.CreateEvent{Object: base}) || !p.Delete(event.DeleteEvent{Object: base}) || !p.Generic(event.GenericEvent{Object: base}) {
		t.Fatal("lifecycle events were lost")
	}
	if p.Update(event.UpdateEvent{}) {
		t.Fatal("nil update accepted")
	}
}

func TestPodEventQueuesBothServiceAccounts(t *testing.T) {
	ctx := context.Background()
	q := workqueue.NewTypedRateLimitingQueue(newPolicyControllerRateLimiter())
	defer q.ShutDown()
	old := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns"}, Spec: corev1.PodSpec{NodeName: "edge", ServiceAccountName: "old-sa"}}
	next := old.DeepCopy()
	next.Spec.NodeName = "cloud"
	next.Spec.ServiceAccountName = "new-sa"
	e := event.UpdateEvent{ObjectOld: old, ObjectNew: next}
	if !podEventPredicate().Update(e) {
		t.Fatal("membership change dropped")
	}
	handler.EnqueueRequestsFromMapFunc(mapPodEvent).Update(ctx, e, q)
	var got []string
	for q.Len() > 0 {
		r, _ := q.Get()
		got = append(got, r.Namespace+"/"+r.Name)
		q.Done(r)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"ns/new-sa", "ns/old-sa"}) {
		t.Fatalf("requests=%v", got)
	}
	unscheduled := old.DeepCopy()
	unscheduled.Spec.NodeName = ""
	if len(mapPodEvent(ctx, unscheduled)) != 0 {
		t.Fatal("unscheduled pod was mapped")
	}
}

func TestPodMappingAPIOutageRetriesAndCloudOnlyDoesNotCreateAccess(t *testing.T) {
	c, cli, acc, msgs := newOptimizationSafetyFixture(t)
	ctx := context.Background()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns"}, Spec: corev1.PodSpec{NodeName: "edge", ServiceAccountName: "sa"}}
	boom := errors.New("Node API temporarily unavailable")
	r := &nodeListCountingReader{Reader: cli, nodeError: boom}
	c.Reader = r
	requests := mapPodEvent(ctx, pod)
	if len(requests) != 1 {
		t.Fatal("API failure lost pod request")
	}
	if _, err := c.Reconcile(ctx, requests[0]); !errors.Is(err, boom) {
		t.Fatalf("expected retryable failure: %v", err)
	}
	if len(msgs.messages) != 0 {
		t.Fatal("sent authorization during failure")
	}
	r.nodeError = nil
	if res := reconcileSafetyAccess(t, c, acc); !res.Requeue || len(msgs.messages) != 1 {
		t.Fatal("retry did not repair missing ObjectSync")
	}
	cloudSA := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "cloud-sa", UID: types.UID("cloud-sa-uid")}}
	cloud := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "cloud"}}
	cloudPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "cloud-pod"}, Spec: corev1.PodSpec{NodeName: "cloud", ServiceAccountName: "cloud-sa"}}
	for _, o := range []client.Object{cloudSA, cloud, cloudPod} {
		if err := cli.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	msgs.messages = nil
	if _, err := c.Reconcile(ctx, mapPodEvent(ctx, cloudPod)[0]); err != nil {
		t.Fatal(err)
	}
	if err := cli.Get(ctx, client.ObjectKey{Namespace: "ns", Name: "cloud-sa"}, &policy.ServiceAccountAccess{}); !apierrors.IsNotFound(err) {
		t.Fatalf("cloud-only access created: %v", err)
	}
	if len(msgs.messages) != 0 {
		t.Fatal("cloud-only pod granted edge access")
	}
}

func TestNodeCreateMappingFailureIsRetryable(t *testing.T) {
	c, cli, acc, msgs := newOptimizationSafetyFixture(t)
	ctx := context.Background()
	if err := cli.Delete(ctx, acc); err != nil {
		t.Fatal(err)
	}
	node := &corev1.Node{}
	if err := cli.Get(ctx, client.ObjectKey{Name: "edge"}, node); err != nil {
		t.Fatal(err)
	}
	r := &rbacFaultReader{Reader: cli, operation: "List", kind: "PodList", failure: context.DeadlineExceeded}
	c.Reader = r
	requests := c.mapNodeFunc(ctx, node)
	if len(requests) != 1 || requests[0].Namespace != "" {
		t.Fatalf("lost node retry: %v", requests)
	}
	if _, err := c.Reconcile(ctx, requests[0]); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("node retry error=%v", err)
	}
	r.failure = nil
	result, err := c.Reconcile(ctx, requests[0])
	if err != nil || !result.Requeue {
		t.Fatalf("node retry did not restore access: %v %v", result, err)
	}
	if err := cli.Get(ctx, client.ObjectKeyFromObject(acc), &policy.ServiceAccountAccess{}); err != nil {
		t.Fatal(err)
	}
	if len(msgs.messages) != 0 {
		t.Fatal("restore sent before normal access reconciliation")
	}
}

func TestServiceAccountDeletionDoesNotWaitForNodeAPI(t *testing.T) {
	c, cli, acc, msgs := newOptimizationSafetyFixture(t)
	ctx := context.Background()
	sa := &corev1.ServiceAccount{}
	if err := cli.Get(ctx, client.ObjectKeyFromObject(acc), sa); err != nil {
		t.Fatal(err)
	}
	if err := cli.Delete(ctx, sa); err != nil {
		t.Fatal(err)
	}
	c.Reader = &nodeListCountingReader{Reader: cli, nodeError: context.DeadlineExceeded}
	requests := mapServiceAccountEvent(ctx, sa)
	if len(requests) != 1 {
		t.Fatal("SA deletion not queued")
	}
	if _, err := c.Reconcile(ctx, requests[0]); err != nil {
		t.Fatal(err)
	}
	if len(msgs.messages) != 1 || msgs.messages[0].GetOperation() != model.DeleteOperation {
		t.Fatal("SA deletion did not revoke access")
	}
	if err := cli.Get(ctx, client.ObjectKeyFromObject(acc), &policy.ServiceAccountAccess{}); !apierrors.IsNotFound(err) {
		t.Fatalf("access not deleted: %v", err)
	}
}

type rbacFaultReader struct {
	client.Reader
	operation, kind string
	failure         error
}

func (r *rbacFaultReader) List(ctx context.Context, o client.ObjectList, opts ...client.ListOption) error {
	if r.operation == "List" && reflect.TypeOf(o).Elem().Name() == r.kind && r.failure != nil {
		return r.failure
	}
	return r.Reader.List(ctx, o, opts...)
}
func (r *rbacFaultReader) Get(ctx context.Context, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
	if r.operation == "Get" && reflect.TypeOf(o).Elem().Name() == r.kind && r.failure != nil {
		return r.failure
	}
	return r.Reader.Get(ctx, key, o, opts...)
}

func addReviewRBAC(t *testing.T, c *Controller, cli client.Client, acc *policy.ServiceAccountAccess, msgs *recordingMessageLayer) (*rbacv1.Role, *rbacv1.ClusterRole, *rbacv1.RoleBinding) {
	t.Helper()
	ctx := context.Background()
	rules := []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get"}}}
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "role", Namespace: "ns"}, Rules: rules}
	clusterRole := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "cluster-role"}, Rules: rules}
	subjects := []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: "ns", Name: "sa"}}
	rb := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "binding", Namespace: "ns"}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name}, Subjects: subjects}
	crb := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "cluster-binding"}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRole.Name}, Subjects: subjects}
	for _, o := range []client.Object{role, clusterRole, rb, crb} {
		if err := cli.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	reconcileSafetyAccess(t, c, acc)
	if err := cli.Create(ctx, newTestServiceAccountAccessObjectSync(acc, "edge", "0")); err != nil {
		t.Fatal(err)
	}
	msgs.messages = nil
	return role, clusterRole, rb
}

func TestRBACReadFailuresPreservePolicyAndRetry(t *testing.T) {
	for _, tc := range []struct{ op, kind string }{{"List", "ClusterRoleBindingList"}, {"List", "RoleBindingList"}, {"Get", "ClusterRole"}, {"Get", "Role"}} {
		for _, fault := range []string{"timeout", "forbidden"} {
			t.Run(tc.op+tc.kind+"/"+fault, func(t *testing.T) {
				c, cli, acc, msgs := newOptimizationSafetyFixture(t)
				ctx := context.Background()
				role, clusterRole, _ := addReviewRBAC(t, c, cli, acc, msgs)
				before := &policy.ServiceAccountAccess{}
				if err := cli.Get(ctx, client.ObjectKeyFromObject(acc), before); err != nil {
					t.Fatal(err)
				}
				role.Rules[0].Verbs = []string{"list"}
				clusterRole.Rules[0].Verbs = []string{"list"}
				for _, o := range []client.Object{role, clusterRole} {
					if err := cli.Update(ctx, o); err != nil {
						t.Fatal(err)
					}
				}
				boom := error(context.DeadlineExceeded)
				if fault == "forbidden" {
					boom = apierrors.NewForbidden(schema.GroupResource{Group: rbacv1.GroupName, Resource: tc.kind}, "test", errors.New("denied"))
				}
				r := &rbacFaultReader{Reader: cli, operation: tc.op, kind: tc.kind, failure: boom}
				c.Reader = r
				if _, err := c.Reconcile(ctx, cr.Request{NamespacedName: client.ObjectKeyFromObject(acc)}); !errors.Is(err, boom) {
					t.Fatalf("retry error=%v", err)
				}
				after := &policy.ServiceAccountAccess{}
				if err := cli.Get(ctx, client.ObjectKeyFromObject(acc), after); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) || len(msgs.messages) != 0 {
					t.Fatal("failed/partial RBAC read changed authorization")
				}
				r.failure = nil
				reconcileSafetyAccess(t, c, acc)
				if len(msgs.messages) != 1 || msgs.messages[0].GetOperation() != model.UpdateOperation {
					t.Fatal("recovered rules not delivered")
				}
				body := msgs.messages[0].GetContent().(*policy.ServiceAccountAccess)
				if !reflect.DeepEqual(body.Spec.AccessRoleBinding[0].Rules[0].Verbs, []string{"list"}) || !reflect.DeepEqual(body.Spec.AccessClusterRoleBinding[0].Rules[0].Verbs, []string{"list"}) {
					t.Fatal("recovery delivered stale rules")
				}
			})
		}
	}
}

func TestDeletedRoleRevokesWithoutDeletingBinding(t *testing.T) {
	for _, cluster := range []bool{false, true} {
		t.Run(fmt.Sprint("cluster=", cluster), func(t *testing.T) {
			c, cli, acc, msgs := newOptimizationSafetyFixture(t)
			role, crRole, _ := addReviewRBAC(t, c, cli, acc, msgs)
			var target client.Object = role
			if cluster {
				target = crRole
			}
			if err := cli.Delete(context.Background(), target); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Reconcile(context.Background(), mapRBACEvent(context.Background(), target)[0]); err != nil {
				t.Fatal(err)
			}
			if len(msgs.messages) != 1 || msgs.messages[0].GetOperation() != model.UpdateOperation {
				t.Fatal("deleted role not revoked")
			}
			body := msgs.messages[0].GetContent().(*policy.ServiceAccountAccess)
			if cluster && (len(body.Spec.AccessClusterRoleBinding) != 0 || len(body.Spec.AccessRoleBinding) != 1) {
				t.Fatal("wrong cluster role revocation")
			}
			if !cluster && (len(body.Spec.AccessRoleBinding) != 0 || len(body.Spec.AccessClusterRoleBinding) != 1) {
				t.Fatal("wrong role revocation")
			}
		})
	}
}

func TestRBACEventCoalescingAndReadFailureRetry(t *testing.T) {
	c, cli, acc, msgs := newOptimizationSafetyFixture(t)
	_, _, rb := addReviewRBAC(t, c, cli, acc, msgs)
	ctx := context.Background()
	old := rb.DeepCopy()
	rb.Subjects = nil
	if err := cli.Update(ctx, rb); err != nil {
		t.Fatal(err)
	}
	q := workqueue.NewTypedRateLimitingQueue(newPolicyControllerRateLimiter())
	defer q.ShutDown()
	h := handler.EnqueueRequestsFromMapFunc(mapRBACEvent)
	h.Update(ctx, event.UpdateEvent{ObjectOld: old, ObjectNew: rb}, q)
	h.Delete(ctx, event.DeleteEvent{Object: old}, q)
	if q.Len() != 1 {
		t.Fatalf("RBAC keys were not coalesced: %d", q.Len())
	}
	request, _ := q.Get()
	defer q.Done(request)
	r := &rbacFaultReader{Reader: cli, operation: "List", kind: "ServiceAccountAccessList", failure: context.DeadlineExceeded}
	c.Reader = r
	if _, err := c.Reconcile(ctx, request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SAA List failure lost: %v", err)
	}
	if len(msgs.messages) != 0 {
		t.Fatal("delivery during API failure")
	}
	r.failure = nil
	if _, err := c.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if len(msgs.messages) != 1 || len(msgs.messages[0].GetContent().(*policy.ServiceAccountAccess).Spec.AccessRoleBinding) != 0 {
		t.Fatal("removed binding subject did not revoke permissions")
	}
}

func BenchmarkPodStatusEventPredicate(b *testing.B) {
	for _, legacy := range []bool{true, false} {
		b.Run(fmt.Sprintf("legacy_%v", legacy), func(b *testing.B) {
			r := &snapshotNodeListReader{}
			for i := 0; i < 1052; i++ {
				r.nodes.Items = append(r.nodes.Items, corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("edge-%d", i), Labels: map[string]string{"node-role.kubernetes.io/edge": ""}}})
			}
			c := &Controller{Reader: r}
			ctx := context.Background()
			var pred predicate.Predicate = podEventPredicate()
			if legacy {
				pred = predicate.NewPredicateFuncs(func(o client.Object) bool { return c.filterObject(ctx, o) })
			}
			old := &corev1.Pod{Spec: corev1.PodSpec{NodeName: "edge-0", ServiceAccountName: "sa"}}
			next := old.DeepCopy()
			next.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "inference1", RestartCount: 1}}
			e := event.UpdateEvent{ObjectOld: old, ObjectNew: next}
			accepted := 0
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if pred.Update(e) {
					accepted++
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(r.nodeLists)/float64(b.N), "node_lists/op")
			b.ReportMetric(float64(accepted)/float64(b.N), "accepted/op")
		})
	}
}

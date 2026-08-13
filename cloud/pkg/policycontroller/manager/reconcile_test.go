package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/authentication/user"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	policyv1alpha1 "github.com/kubeedge/api/apis/policy/v1alpha1"
	reliablesyncsv1alpha1 "github.com/kubeedge/api/apis/reliablesyncs/v1alpha1"
	"github.com/kubeedge/beehive/pkg/core/model"
	"github.com/kubeedge/kubeedge/cloud/pkg/common/messagelayer"
	"github.com/kubeedge/kubeedge/cloud/pkg/synccontroller"
)

type getErrorReader struct {
	client.Reader
	err error
}

func (r getErrorReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return r.err
}

type listErrorClient struct {
	client.Client
	err error
}

func (c listErrorClient) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return c.err
}

type roleGetErrorReader struct {
	client.Reader
	err error
}

func (r roleGetErrorReader) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	switch object.(type) {
	case *rbacv1.Role, *rbacv1.ClusterRole:
		return r.err
	default:
		return r.Reader.Get(ctx, key, object, opts...)
	}
}

type recordingMessageLayer struct {
	messages []model.Message
}

func (r *recordingMessageLayer) Send(message model.Message) error {
	r.messages = append(r.messages, message)
	return nil
}

func (*recordingMessageLayer) Receive() (model.Message, error) {
	return model.Message{}, errors.New("Receive is not implemented by recordingMessageLayer")
}

func (*recordingMessageLayer) Response(model.Message) error {
	return errors.New("Response is not implemented by recordingMessageLayer")
}

func TestIntersectSlice(t *testing.T) {
	tests := []struct {
		name string
		a    []string
		b    []string
		want []string
	}{
		{
			name: "test1",
			a:    []string{"a", "b", "c"},
			b:    []string{"b", "c", "d"},
			want: []string{"b", "c"},
		},
		{
			name: "test2",
			a:    []string{"a", "b", "c"},
			b:    []string{"d", "e", "f"},
			want: []string{},
		},
		{
			name: "test3",
			a:    []string{"a", "b", "c"},
			b:    []string{"a", "b", "c"},
			want: []string{"a", "b", "c"},
		},
		{
			name: "test4",
			a:    []string{},
			b:    []string{"a", "b", "c"},
			want: []string{},
		},
		{
			name: "test5",
			a:    []string{"a", "b", "c"},
			b:    []string{},
			want: []string{},
		},
		{
			name: "test6",
			a:    []string{},
			b:    []string{},
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := intersectSlice(tt.a, tt.b); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("intersectSlice() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSubtractSlice(t *testing.T) {
	tests := []struct {
		name string
		a    []string
		b    []string
		want []string
	}{
		{
			name: "test1",
			a:    []string{"a", "b", "c"},
			b:    []string{"b", "c", "d"},
			want: []string{"a"},
		},
		{
			name: "test2",
			a:    []string{"a", "b", "c"},
			b:    []string{"d", "e", "f"},
			want: []string{"a", "b", "c"},
		},
		{
			name: "test3",
			a:    []string{"a", "b", "c"},
			b:    []string{"a", "b", "c"},
			want: []string{},
		},
		{
			name: "test4",
			a:    []string{},
			b:    []string{"a", "b", "c"},
			want: []string{},
		},
		{
			name: "test5",
			a:    []string{"a", "b", "c"},
			b:    []string{},
			want: []string{"a", "b", "c"},
		},
		{
			name: "test6",
			a:    []string{},
			b:    []string{},
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := subtractSlice(tt.b, tt.a); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("subtractSlice() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAppliesTo(t *testing.T) {
	tests := []struct {
		subjects  []rbacv1.Subject
		user      user.Info
		namespace string
		appliesTo bool
		index     int
		testCase  string
	}{
		{
			subjects: []rbacv1.Subject{
				{Kind: rbacv1.UserKind, Name: "foobar"},
			},
			user:      &user.DefaultInfo{Name: "foobar"},
			appliesTo: true,
			index:     0,
			testCase:  "single subject that matches username",
		},
		{
			subjects: []rbacv1.Subject{
				{Kind: rbacv1.UserKind, Name: "barfoo"},
				{Kind: rbacv1.UserKind, Name: "foobar"},
			},
			user:      &user.DefaultInfo{Name: "foobar"},
			appliesTo: true,
			index:     1,
			testCase:  "multiple subjects, one that matches username",
		},
		{
			subjects: []rbacv1.Subject{
				{Kind: rbacv1.UserKind, Name: "barfoo"},
				{Kind: rbacv1.UserKind, Name: "foobar"},
			},
			user:      &user.DefaultInfo{Name: "zimzam"},
			appliesTo: false,
			testCase:  "multiple subjects, none that match username",
		},
		{
			subjects: []rbacv1.Subject{
				{Kind: rbacv1.UserKind, Name: "barfoo"},
				{Kind: rbacv1.GroupKind, Name: "foobar"},
			},
			user:      &user.DefaultInfo{Name: "zimzam", Groups: []string{"foobar"}},
			appliesTo: true,
			index:     1,
			testCase:  "multiple subjects, one that match group",
		},
		{
			subjects: []rbacv1.Subject{
				{Kind: rbacv1.UserKind, Name: "barfoo"},
				{Kind: rbacv1.GroupKind, Name: "foobar"},
			},
			user:      &user.DefaultInfo{Name: "zimzam", Groups: []string{"foobar"}},
			namespace: "namespace1",
			appliesTo: true,
			index:     1,
			testCase:  "multiple subjects, one that match group, should ignore namespace",
		},
		{
			subjects: []rbacv1.Subject{
				{Kind: rbacv1.UserKind, Name: "barfoo"},
				{Kind: rbacv1.GroupKind, Name: "foobar"},
				{Kind: rbacv1.ServiceAccountKind, Namespace: "kube-system", Name: "default"},
			},
			user:      &user.DefaultInfo{Name: "system:serviceaccount:kube-system:default"},
			namespace: "default",
			appliesTo: true,
			index:     2,
			testCase:  "multiple subjects with a service account that matches",
		},
		{
			subjects: []rbacv1.Subject{
				{Kind: rbacv1.UserKind, Name: "*"},
			},
			user:      &user.DefaultInfo{Name: "foobar"},
			namespace: "default",
			appliesTo: false,
			testCase:  "* user subject name doesn't match all users",
		},
		{
			subjects: []rbacv1.Subject{
				{Kind: rbacv1.GroupKind, Name: user.AllAuthenticated},
				{Kind: rbacv1.GroupKind, Name: user.AllUnauthenticated},
			},
			user:      &user.DefaultInfo{Name: "foobar", Groups: []string{user.AllAuthenticated}},
			namespace: "default",
			appliesTo: true,
			index:     0,
			testCase:  "binding to all authenticated and unauthenticated subjects matches authenticated user",
		},
		{
			subjects: []rbacv1.Subject{
				{Kind: rbacv1.GroupKind, Name: user.AllAuthenticated},
				{Kind: rbacv1.GroupKind, Name: user.AllUnauthenticated},
			},
			user:      &user.DefaultInfo{Name: "system:anonymous", Groups: []string{user.AllUnauthenticated}},
			namespace: "default",
			appliesTo: true,
			index:     1,
			testCase:  "binding to all authenticated and unauthenticated subjects matches anonymous user",
		},
	}

	for _, tc := range tests {
		gotIndex, got := appliesTo(tc.user, tc.subjects, tc.namespace)
		if got != tc.appliesTo {
			t.Errorf("case %q want appliesTo=%t, got appliesTo=%t", tc.testCase, tc.appliesTo, got)
		}
		if gotIndex != tc.index {
			t.Errorf("case %q want index %d, got %d", tc.testCase, tc.index, gotIndex)
		}
	}
}

func newServiceAccount() *v1.ServiceAccount {
	return &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sa1",
			Namespace: "ns1",
		},
	}
}

func TestNewSaAccessObject(t *testing.T) {
	tests := []struct {
		name   string
		sa     *v1.ServiceAccount
		result *policyv1alpha1.ServiceAccountAccess
	}{
		{
			name: "test NewSaAccessObject",
			sa:   newServiceAccount(),
			result: &policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sa1",
					Namespace: "ns1",
				},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount: *newServiceAccount(),
				},
			},
		},
	}
	for _, tc := range tests {
		got := newSaAccessObject(*tc.sa)
		if !reflect.DeepEqual(got, tc.result) {
			t.Errorf("case %q want=%v, got=%v", tc.name, tc.result, got)
		}
	}
}

var podStr1 = `{
  "apiVersion": "v1",
  "kind": "Pod",
  "metadata": {
    "name": "pod1",
    "namespace": "my-namespace"
  },
  "spec": {
    "serviceAccountName": "sa1",
    "nodeName": "my-node",
    "containers": [
      {
        "name": "my-container",
        "image": "my-image",
        "ports": [
          {
            "containerPort": 80,
            "protocol": "TCP"
          }
        ]
      }
    ]
  }
}`

var podDelStr1 = `{
	"apiVersion": "v1",
	"kind": "Pod",
	"metadata": {
	  "name": "podDel",
	  "namespace": "my-namespace",
	  "deletionTimestamp": "2022-01-01T00:00:00Z"
	},
	"spec": {
	  "serviceAccountName": "sa1",
	  "nodeName": "my-node",
	  "containers": [
		{
		  "name": "my-container",
		  "image": "my-image",
		  "ports": [
			{
			  "containerPort": 80,
			  "protocol": "TCP"
			}
		  ]
		}
	  ]
	}
  }`

var podStr2 = `{
  "apiVersion": "v1",
  "kind": "Pod",
  "metadata": {
    "name": "pod2",
    "namespace": "my-namespace"
  },
  "spec": {
    "serviceAccountName": "sa1",
    "nodeName": "my-node-2",
    "containers": [
      {
        "name": "my-container-2",
        "image": "my-image",
        "ports": [
          {
            "containerPort": 80,
            "protocol": "TCP"
          }
        ]
      }
    ]
  }
}`

var podStrWithoutNodeName = `{
  "apiVersion": "v1",
  "kind": "Pod",
  "metadata": {
    "name": "podwnn1",
    "namespace": "my-namespace"
  },
  "spec": {
    "serviceAccountName": "sa1",
    "containers": [
      {
        "name": "my-container",
        "image": "my-image",
        "ports": [
          {
            "containerPort": 80,
            "protocol": "TCP"
          }
        ]
      }
    ]
  }
}`

var podStrWithoutSa = `{
  "apiVersion": "v1",
  "kind": "Pod",
  "metadata": {
    "name": "podwnn1",
    "namespace": "my-namespace"
  },
  "spec": {
    "nodeName": "my-node",
    "containers": [
      {
        "name": "my-container",
        "image": "my-image",
        "ports": [
          {
            "containerPort": 80,
            "protocol": "TCP"
          }
        ]
      }
    ]
  }
}`

var saStr1 = `{
    "apiVersion": "v1",
    "kind": "ServiceAccount",
    "metadata": {
        "name": "sa1",
        "namespace": "my-namespace",
        "resourceVersion": "999"
    }
}`

var saStr2 = `{
    "apiVersion": "v1",
    "kind": "ServiceAccount",
    "metadata": {
        "name": "sa2",
        "namespace": "my-namespace",
        "resourceVersion": "999"
    }
}`

var saNsStr = `{
    "apiVersion": "v1",
    "kind": "ServiceAccount",
    "metadata": {
        "name": "sa1",
        "namespace": "my-namespace2"
    }
}`

var roleStr1 = `{
    "apiVersion": "rbac.authorization.k8s.io/v1",
    "kind": "Role",
    "metadata": {
        "name": "role1",
        "namespace": "my-namespace",
        "resourceVersion": "999"
    },
    "rules": [
        {
            "apiGroups": [""],
            "resources": ["pods"],
            "verbs": ["get", "list", "watch"]
        },
        {
            "apiGroups": ["apps"],
            "resources": ["deployments"],
            "verbs": ["get", "list", "watch"]
        }
    ]
}`

var roleNsStr1 = `{
    "apiVersion": "rbac.authorization.k8s.io/v1",
    "kind": "Role",
    "metadata": {
        "name": "role1",
        "namespace": "my-namespace2",
        "resourceVersion": "999"
    },
    "rules": [
        {
            "apiGroups": [""],
            "resources": ["pods"],
            "verbs": ["get", "list", "watch"]
        },
        {
            "apiGroups": ["apps"],
            "resources": ["deployments"],
            "verbs": ["get", "list", "watch"]
        }
    ]
}`

var roleStr2 = `{
    "apiVersion": "rbac.authorization.k8s.io/v1",
    "kind": "Role",
    "metadata": {
        "name": "role2",
        "namespace": "my-namespace",
        "resourceVersion": "999"
    },
    "rules": [
        {
            "apiGroups": [""],
            "resources": ["pods"],
            "verbs": ["get", "list", "watch"]
        },
        {
            "apiGroups": ["apps"],
            "resources": ["configmaps"],
            "verbs": ["get", "list", "watch"]
        }
    ]
}`

var rbStr1 = `{
    "apiVersion": "rbac.authorization.k8s.io/v1",
    "kind": "RoleBinding",
    "metadata": {
        "name": "rb1",
        "namespace": "my-namespace",
        "resourceVersion": "999"
    },
    "roleRef": {
        "apiGroup": "rbac.authorization.k8s.io",
        "kind": "Role",
        "name": "role1"
    },
    "subjects": [
        {
            "kind": "ServiceAccount",
            "name": "sa1",
            "namespace": "my-namespace"
        }
    ]
}`

var rbStr2 = `{
    "apiVersion": "rbac.authorization.k8s.io/v1",
    "kind": "RoleBinding",
    "metadata": {
        "name": "rb2",
        "namespace": "my-namespace",
        "resourceVersion": "999"
    },
    "roleRef": {
        "apiGroup": "rbac.authorization.k8s.io",
        "kind": "Role",
        "name": "role2"
    },
    "subjects": [
        {
            "kind": "ServiceAccount",
            "name": "sa1",
            "namespace": "my-namespace"
        }
    ]
}`

var rbWithCrStr = `{
    "apiVersion": "rbac.authorization.k8s.io/v1",
    "kind": "RoleBinding",
    "metadata": {
        "name": "rbWithCr",
        "namespace": "my-namespace",
        "resourceVersion": "999"
    },
    "roleRef": {
        "apiGroup": "rbac.authorization.k8s.io",
        "kind": "ClusterRole",
        "name": "cr1"
    },
    "subjects": [
        {
            "kind": "ServiceAccount",
            "name": "sa1",
            "namespace": "my-namespace"
        }
    ]
}`

var crStr1 = `{
    "apiVersion": "rbac.authorization.k8s.io/v1",
    "kind": "ClusterRole",
    "metadata": {
        "name": "cr1",
        "resourceVersion": "999"
    },
    "rules": [
        {
            "apiGroups": [""],
            "resources": ["pods"],
            "verbs": ["get", "list", "watch"]
        },
        {
            "apiGroups": ["apps"],
            "resources": ["deployments"],
            "verbs": ["get", "list", "watch"]
        }
    ]
}`

var crbStr1 = `{
    "apiVersion": "rbac.authorization.k8s.io/v1",
    "kind": "ClusterRoleBinding",
    "metadata": {
        "name": "crb1",
        "resourceVersion": "999"
    },
    "roleRef": {
        "apiGroup": "rbac.authorization.k8s.io",
        "kind": "ClusterRole",
        "name": "cr1"
    },
    "subjects": [
        {
            "kind": "ServiceAccount",
            "name": "sa1",
            "namespace": "my-namespace"
        }
    ]
}`

var crbStr2 = `{
    "apiVersion": "rbac.authorization.k8s.io/v1",
    "kind": "ClusterRoleBinding",
    "metadata": {
        "name": "crb2",
        "resourceVersion": "999"
    },
    "roleRef": {
        "apiGroup": "rbac.authorization.k8s.io",
        "kind": "ClusterRole",
        "name": "cr1"
    },
    "subjects": [
        {
            "kind": "ServiceAccount",
            "name": "sa1",
            "namespace": "my-namespace"
        }
    ]
}`

func TestFilterResource(t *testing.T) {
	var sa1 v1.ServiceAccount
	err := json.Unmarshal([]byte(saStr1), &sa1)
	if err != nil {
		t.Errorf("Failed to unmarshal sa1: %v", err)
	}
	var sa2 v1.ServiceAccount
	err = json.Unmarshal([]byte(saStr2), &sa2)
	if err != nil {
		t.Errorf("Failed to unmarshal sa2: %v", err)
	}
	var saNs v1.ServiceAccount
	err = json.Unmarshal([]byte(saNsStr), &saNs)
	if err != nil {
		t.Errorf("Failed to unmarshal sa2: %v", err)
	}
	var role1 rbacv1.Role
	err = json.Unmarshal([]byte(roleStr1), &role1)
	if err != nil {
		t.Errorf("Failed to unmarshal role1: %v", err)
	}
	var roleNs rbacv1.Role
	err = json.Unmarshal([]byte(roleNsStr1), &roleNs)
	if err != nil {
		t.Errorf("Failed to unmarshal roleNs: %v", err)
	}
	var role2 rbacv1.Role
	err = json.Unmarshal([]byte(roleStr2), &role2)
	if err != nil {
		t.Errorf("Failed to unmarshal role2: %v", err)
	}
	var rb1 rbacv1.RoleBinding
	err = json.Unmarshal([]byte(rbStr1), &rb1)
	if err != nil {
		t.Errorf("Failed to unmarshal rb1: %v", err)
	}
	var rb2 rbacv1.RoleBinding
	err = json.Unmarshal([]byte(rbStr2), &rb2)
	if err != nil {
		t.Errorf("Failed to unmarshal rb2: %v", err)
	}
	var rbWithCr rbacv1.RoleBinding
	err = json.Unmarshal([]byte(rbWithCrStr), &rbWithCr)
	if err != nil {
		t.Errorf("Failed to unmarshal rbWithCr: %v", err)
	}
	var cr1 rbacv1.ClusterRole
	err = json.Unmarshal([]byte(crStr1), &cr1)
	if err != nil {
		t.Errorf("Failed to unmarshal cr1: %v", err)
	}
	var crb1 rbacv1.ClusterRoleBinding
	err = json.Unmarshal([]byte(crbStr1), &crb1)
	if err != nil {
		t.Errorf("Failed to unmarshal crb1: %v", err)
	}
	var crb2 rbacv1.ClusterRoleBinding
	err = json.Unmarshal([]byte(crbStr2), &crb2)
	if err != nil {
		t.Errorf("Failed to unmarshal crb2: %v", err)
	}
	var pod1 v1.Pod
	err = json.Unmarshal([]byte(podStr1), &pod1)
	if err != nil {
		t.Errorf("Failed to unmarshal pod1: %v", err)
	}
	var podNoNodeName v1.Pod
	err = json.Unmarshal([]byte(podStrWithoutNodeName), &podNoNodeName)
	if err != nil {
		t.Errorf("Failed to unmarshal podNoNodeName: %v", err)
	}
	var podNoSa v1.Pod
	err = json.Unmarshal([]byte(podStrWithoutSa), &podNoSa)
	if err != nil {
		t.Errorf("Failed to unmarshal podNoSa: %v", err)
	}
	nodeList := &v1.NodeList{
		Items: []v1.Node{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-node",
					Labels: map[string]string{
						"node-role.kubernetes.io/edge": "",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-2",
					Labels: map[string]string{
						"node-role.kubernetes.io/edge": "",
					},
				},
			},
		},
	}
	var tests = []struct {
		name            string
		input           []client.Object
		rbacObj         client.Object
		obj             client.Object
		reconcileResult []controllerruntime.Request
		rbacResult      bool
		objResult       bool
	}{
		{
			name: "filter role or serviceaccount success",
			input: []client.Object{&policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount: sa1,
					AccessRoleBinding: []policyv1alpha1.AccessRoleBinding{
						{RoleBinding: rb1}, {RoleBinding: rb2}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{
						{ClusterRoleBinding: crb1}}},
			}, &crb1, &rb1, &rb2},
			rbacObj:    &role1,
			obj:        &sa1,
			rbacResult: true,
			objResult:  true,
			reconcileResult: []controllerruntime.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      "sa1",
						Namespace: "my-namespace",
					},
				},
			},
		},
		{
			name: "filter role or serviceaccount failed",
			input: []client.Object{&policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount: sa1,
					AccessRoleBinding: []policyv1alpha1.AccessRoleBinding{
						{RoleBinding: rb1}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{
						{ClusterRoleBinding: crb1}}},
			}, &crb1, &rb1},
			rbacObj:         &role2,
			obj:             &sa2,
			rbacResult:      false,
			objResult:       false,
			reconcileResult: []controllerruntime.Request{},
		},
		{
			name: "filter role failed with nil role",
			input: []client.Object{&policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount: sa1,
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{
						{ClusterRoleBinding: crb1}}},
			}, &crb1},
			rbacObj:         &role2,
			rbacResult:      false,
			reconcileResult: []controllerruntime.Request{},
		},
		{
			name: "filter role or serviceaccount failed with different namespace",
			input: []client.Object{&policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount: sa1,
					AccessRoleBinding: []policyv1alpha1.AccessRoleBinding{
						{RoleBinding: rb1},
					},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{
						{ClusterRoleBinding: crb1}}},
			}, &crb1, &rb1},
			rbacObj:         &roleNs,
			obj:             &saNs,
			rbacResult:      false,
			objResult:       false,
			reconcileResult: []controllerruntime.Request{},
		},
		{
			name: "filter role failed with nil rolebinding and clusterrolebinding",
			input: []client.Object{&policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec:       policyv1alpha1.AccessSpec{ServiceAccount: sa1},
			}},
			rbacObj:         &role2,
			rbacResult:      false,
			reconcileResult: []controllerruntime.Request{},
		},
		{
			name: "filter rolebinding success",
			input: []client.Object{&policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount: sa1,
					AccessRoleBinding: []policyv1alpha1.AccessRoleBinding{
						{RoleBinding: rb1}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{
						{ClusterRoleBinding: crb1}}},
			}, &crb1, &rb1},
			rbacObj:    &rb1,
			rbacResult: true,
			reconcileResult: []controllerruntime.Request{
				{NamespacedName: types.NamespacedName{Name: "sa1", Namespace: "my-namespace"}},
			},
		},
		{
			name: "filter rolebinding failed",
			input: []client.Object{&policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount: sa1,
					AccessRoleBinding: []policyv1alpha1.AccessRoleBinding{
						{RoleBinding: rb1}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{
						{ClusterRoleBinding: crb1}}},
			}, &crb1, &rb1},
			rbacObj:    &rb2,
			rbacResult: true,
			reconcileResult: []controllerruntime.Request{
				{NamespacedName: types.NamespacedName{Name: "sa1", Namespace: "my-namespace"}},
			},
		},
		{
			name: "filter rolebinding failed with nil rolebinding",
			input: []client.Object{&policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount: sa1,
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{
						{ClusterRoleBinding: crb1}}},
			}, &crb1},
			rbacObj:    &rb2,
			rbacResult: true,
			reconcileResult: []controllerruntime.Request{
				{NamespacedName: types.NamespacedName{Name: "sa1", Namespace: "my-namespace"}},
			},
		},
		{
			name: "filter clusterrolebinding success",
			input: []client.Object{&policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount: sa1,
					AccessRoleBinding: []policyv1alpha1.AccessRoleBinding{
						{RoleBinding: rb1}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{
						{ClusterRoleBinding: crb1}}},
			}, &crb1, &rb1},
			rbacObj:    &crb1,
			rbacResult: true,
			reconcileResult: []controllerruntime.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      "sa1",
						Namespace: "my-namespace",
					},
				},
			},
		},
		{
			name: "filter clusterrole success",
			input: []client.Object{&policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount: sa1,
					AccessRoleBinding: []policyv1alpha1.AccessRoleBinding{
						{RoleBinding: rb1},
					},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{
						{ClusterRoleBinding: crb1}}},
			}, &crb1, &rb1},
			rbacObj:    &cr1,
			rbacResult: true,
			reconcileResult: []controllerruntime.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      "sa1",
						Namespace: "my-namespace",
					},
				},
			},
		},
		{
			name: "filter rolebinding bind cluster role success",
			input: []client.Object{&policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount: sa1,
					AccessRoleBinding: []policyv1alpha1.AccessRoleBinding{
						{RoleBinding: rbWithCr}}},
			}, &rbWithCr},
			rbacObj:    &cr1,
			rbacResult: true,
			reconcileResult: []controllerruntime.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      "sa1",
						Namespace: "my-namespace",
					},
				},
			},
		},
		{
			name: "filter pod success",
			input: []client.Object{&policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sa1",
					Namespace: "my-namespace",
				},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount: sa1,
				},
			}},
			obj:       &pod1,
			objResult: true,
		},
		{
			name: "filter pod failed for without service account",
			input: []client.Object{&policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sa1",
					Namespace: "my-namespace",
				},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount: sa1,
				},
			}},
			obj:       &podNoSa,
			objResult: false,
		},
		{
			name: "filter pod failed for without node name",
			input: []client.Object{&policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sa1",
					Namespace: "my-namespace",
				},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount: sa1,
				},
			}},
			obj:       &podNoNodeName,
			objResult: false,
		},
	}
	var accessScheme = runtime.NewScheme()
	if err := policyv1alpha1.AddToScheme(accessScheme); err != nil {
		t.Errorf("Failed to add access scheme: %v", err)
	}
	if err := v1.AddToScheme(accessScheme); err != nil {
		t.Errorf("Failed to add v1 scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(accessScheme); err != nil {
		t.Errorf("Failed to add rbacv1 scheme: %v", err)
	}
	for _, tc := range tests {
		fakeClient := fake.NewClientBuilder().WithScheme(accessScheme).WithObjects(tc.input...).WithLists(nodeList).Build()
		ctr := &Controller{
			Client: fakeClient,
			Reader: fakeClient,
		}
		if tc.rbacObj != nil {
			got := ctr.filterResource(context.Background(), tc.rbacObj)
			if !reflect.DeepEqual(got, tc.rbacResult) {
				t.Errorf("case %q want=%v, got=%v", tc.name, tc.rbacResult, got)
			}
			got2 := ctr.mapRolesFunc(context.Background(), tc.rbacObj)
			if !equality.Semantic.DeepEqual(got2, tc.reconcileResult) {
				t.Errorf("case %q want=%v, got=%v", tc.name, tc.reconcileResult, got2)
			}
		}
		if tc.obj != nil {
			got1 := ctr.filterObject(context.Background(), tc.obj)
			if !reflect.DeepEqual(got1, tc.objResult) {
				t.Errorf("case %q want=%v, got=%v", tc.name, tc.objResult, got1)
			}
		}
	}
}

func TestMapObjectFunc(t *testing.T) {
	var pod1 v1.Pod
	err := json.Unmarshal([]byte(podStr1), &pod1)
	if err != nil {
		t.Errorf("Failed to unmarshal pod1: %v", err)
	}
	var podDel v1.Pod
	err = json.Unmarshal([]byte(podDelStr1), &podDel)
	if err != nil {
		t.Errorf("Failed to unmarshal podDel: %v", err)
	}
	var rb2 rbacv1.RoleBinding
	err = json.Unmarshal([]byte(rbStr2), &rb2)
	if err != nil {
		t.Errorf("Failed to unmarshal rb2: %v", err)
	}
	var crb1 rbacv1.ClusterRoleBinding
	err = json.Unmarshal([]byte(crbStr1), &crb1)
	if err != nil {
		t.Errorf("Failed to unmarshal crb1: %v", err)
	}
	var sa1 v1.ServiceAccount
	err = json.Unmarshal([]byte(saStr1), &sa1)
	if err != nil {
		t.Errorf("Failed to unmarshal sa1: %v", err)
	}
	var sa2 v1.ServiceAccount
	err = json.Unmarshal([]byte(saStr2), &sa2)
	if err != nil {
		t.Errorf("Failed to unmarshal sa2: %v", err)
	}
	var cr1 rbacv1.ClusterRole
	err = json.Unmarshal([]byte(crStr1), &cr1)
	if err != nil {
		t.Errorf("Failed to unmarshal cr1: %v", err)
	}
	var rb1 rbacv1.RoleBinding
	err = json.Unmarshal([]byte(rbStr1), &rb1)
	if err != nil {
		t.Errorf("Failed to unmarshal rb1: %v", err)
	}
	var role1 rbacv1.Role
	err = json.Unmarshal([]byte(roleStr1), &role1)
	if err != nil {
		t.Errorf("Failed to unmarshal role1: %v", err)
	}
	var tests = []struct {
		name            string
		input           *policyv1alpha1.ServiceAccountAccess
		obj             client.Object
		reconcileResult []controllerruntime.Request
		output          *[]policyv1alpha1.ServiceAccountAccess
	}{
		{
			name: "match pod success and won't reconcile",
			input: &policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount:           sa1,
					AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: rb1}, {RoleBinding: rb2}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: crb1}},
				},
			},
			obj: &pod1,
			reconcileResult: []controllerruntime.Request{
				{NamespacedName: types.NamespacedName{Name: "sa1", Namespace: "my-namespace"}},
			},
			output: &[]policyv1alpha1.ServiceAccountAccess{{
				ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount:           sa1,
					AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: rb1}, {RoleBinding: rb2}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: crb1}},
				},
			}},
		},
		{
			name: "match deleting pod success and reconcile",
			input: &policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount:           sa1,
					AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: rb1}, {RoleBinding: rb2}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: crb1}},
				},
			},
			obj: &podDel,
			reconcileResult: []controllerruntime.Request{
				{NamespacedName: types.NamespacedName{Name: "sa1", Namespace: "my-namespace"}},
			},
			output: &[]policyv1alpha1.ServiceAccountAccess{{
				ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount:           sa1,
					AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: rb1}, {RoleBinding: rb2}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: crb1}},
				},
			}},
		},
		{
			name: "match pod not exist in access list",
			input: &policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sa2",
					Namespace: "my-namespace",
				},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount: sa2,
					AccessRoleBinding: []policyv1alpha1.AccessRoleBinding{
						{
							RoleBinding: rb2,
						},
					},
				},
			},
			obj:             &pod1,
			reconcileResult: []controllerruntime.Request{},
			output: &[]policyv1alpha1.ServiceAccountAccess{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sa2",
						Namespace: "my-namespace",
					},
					Spec: policyv1alpha1.AccessSpec{
						ServiceAccount: sa2,
						AccessRoleBinding: []policyv1alpha1.AccessRoleBinding{
							{
								RoleBinding: rb2,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sa1",
						Namespace: "my-namespace",
					},
					Spec: policyv1alpha1.AccessSpec{
						ServiceAccount: v1.ServiceAccount{
							ObjectMeta: metav1.ObjectMeta{
								Name:      "sa1",
								Namespace: "my-namespace",
							},
						},
					},
				},
			},
		},
		{
			name: "match serviceaccount success",
			input: &policyv1alpha1.ServiceAccountAccess{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sa1",
					Namespace: "my-namespace",
				},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount: sa1,
					AccessRoleBinding: []policyv1alpha1.AccessRoleBinding{
						{
							RoleBinding: rb1,
						},
						{
							RoleBinding: rb2,
						},
					},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{
						{
							ClusterRoleBinding: crb1,
						},
					},
				},
			},
			obj: &sa1,
			reconcileResult: []controllerruntime.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      "sa1",
						Namespace: "my-namespace",
					},
				},
			},
			output: &[]policyv1alpha1.ServiceAccountAccess{{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sa1",
					Namespace: "my-namespace",
				},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount: sa1,
					AccessRoleBinding: []policyv1alpha1.AccessRoleBinding{
						{
							RoleBinding: rb1,
						},
						{
							RoleBinding: rb2,
						},
					},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{
						{
							ClusterRoleBinding: crb1,
						},
					},
				},
			}},
		},
	}
	var accessScheme = runtime.NewScheme()
	if err := policyv1alpha1.AddToScheme(accessScheme); err != nil {
		t.Errorf("Failed to add access scheme: %v", err)
	}
	for _, tc := range tests {
		fakeClient := fake.NewClientBuilder().WithScheme(accessScheme).WithObjects(tc.input).Build()
		ctr := &Controller{
			Client: fakeClient,
			Reader: fakeClient,
		}
		got := ctr.mapObjectFunc(context.Background(), tc.obj)
		if !equality.Semantic.DeepEqual(got, tc.reconcileResult) {
			t.Errorf("case %q, mapObjectFunc() = %v, want %v", tc.name, got, tc.reconcileResult)
		}
		sort.Slice(*tc.output, func(i, j int) bool {
			return (*tc.output)[i].Name < (*tc.output)[j].Name
		})
		accList := &policyv1alpha1.ServiceAccountAccessList{}
		if err := ctr.Client.List(context.Background(), accList, &client.ListOptions{Namespace: tc.obj.GetNamespace()}); err != nil {
			t.Errorf("Failed to list access: %v", err)
		}
		sort.Slice(accList.Items, func(i, j int) bool {
			return accList.Items[i].Name < accList.Items[j].Name
		})
		for i := range accList.Items {
			if accList.Items[i].Name != (*tc.output)[i].Name {
				t.Errorf("case %q, got %v, want %v", tc.name, accList.Items[i].Name, (*tc.output)[i].Name)
			}
			if accList.Items[i].Namespace != (*tc.output)[i].Namespace {
				t.Errorf("case %q, got %v, want %v", tc.name, accList.Items[i].Namespace, (*tc.output)[i].Namespace)
			}
			if !equality.Semantic.DeepEqual(accList.Items[i].Spec, (*tc.output)[i].Spec) {
				t.Errorf("case %q, got %v, want %v", tc.name, accList.Items[i].Spec, (*tc.output)[i].Spec)
			}
		}
	}
}

func TestGetNodeListOfServiceAccountAccess(t *testing.T) {
	// Create a sample ServiceAccountAccess object
	saa := &policyv1alpha1.ServiceAccountAccess{
		Spec: policyv1alpha1.AccessSpec{
			ServiceAccount: v1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sa",
					Namespace: "test-ns",
				},
			},
		},
	}

	nodeList := &v1.NodeList{
		Items: []v1.Node{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-1",
					Labels: map[string]string{
						"node-role.kubernetes.io/edge": "",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-2",
					Labels: map[string]string{
						"node-role.kubernetes.io/edge": "",
					},
				},
			},
		},
	}

	// Create a sample PodList object with two pods on different nodes
	podList := &v1.PodList{
		Items: []v1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-1",
					Namespace: "test-ns",
				},
				Spec: v1.PodSpec{
					NodeName:           "node-1",
					ServiceAccountName: "test-sa",
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-2",
					Namespace: "test-ns",
				},
				Spec: v1.PodSpec{
					NodeName:           "node-2",
					ServiceAccountName: "test-sa",
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-3",
					Namespace: "test-ns",
				},
				Spec: v1.PodSpec{
					NodeName:           "node-2",
					ServiceAccountName: "test-sa",
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-4",
					Namespace: "test-ns",
				},
				Spec: v1.PodSpec{
					NodeName:           "node-4",
					ServiceAccountName: "test-sa2",
				},
			},
		},
	}
	pdStrategyTypeIndexer := func(obj client.Object) []string {
		pd, ok := obj.(*v1.Pod)
		if !ok {
			panic(fmt.Errorf("indexer function for type %T's spec.strategy.type field received"+
				" object of type %T, this should never happen", v1.Pod{}, obj))
		}
		serviceAccountName := ""
		if pd != nil {
			serviceAccountName = pd.Spec.ServiceAccountName
		}
		return []string{serviceAccountName}
	}
	var v1Scheme = runtime.NewScheme()
	if err := v1.AddToScheme(v1Scheme); err != nil {
		t.Errorf("Failed to add access scheme: %v", err)
	}
	withScheme := fake.NewClientBuilder().WithScheme(v1Scheme).WithIndex(&v1.Pod{}, "spec.serviceAccountName", pdStrategyTypeIndexer)
	fakeClient := withScheme.Build()
	got, err := getNodeListOfServiceAccountAccess(context.Background(), fakeClient, saa)
	if err != nil {
		t.Errorf("fakeClient get node list error = %v", err)
	}
	if !equality.Semantic.DeepEqual(got, []string{}) {
		t.Errorf("testcase 1 got %v, want %v", got, []string{})
	}
	fakeClient2 := withScheme.WithObjects(&podList.Items[0]).WithLists(nodeList).Build()
	got2, err := getNodeListOfServiceAccountAccess(context.Background(), fakeClient2, saa)
	if err != nil {
		t.Errorf("fakeClient2 get node list error = %v", err)
	}
	if !equality.Semantic.DeepEqual(got2, []string{"node-1"}) {
		t.Errorf("testcase 2 got %v, want %v", got2, []string{"node-1"})
	}
	fakeClient3 := withScheme.WithObjects(&podList.Items[1]).WithObjects(&podList.Items[2]).Build()
	got3, err := getNodeListOfServiceAccountAccess(context.Background(), fakeClient3, saa)
	if err != nil {
		t.Errorf("fakeClient3 get node list error = %v", err)
	}
	if !equality.Semantic.DeepEqual(got3, []string{"node-1", "node-2"}) {
		t.Errorf("testcase 3 got %v, want %v", got3, []string{"node-1", "node-2"})
	}
}

func newTestServiceAccountAccessObjectSync(acc *policyv1alpha1.ServiceAccountAccess, node, resourceVersion string) *reliablesyncsv1alpha1.ObjectSync {
	return &reliablesyncsv1alpha1.ObjectSync{
		ObjectMeta: metav1.ObjectMeta{
			Name:      synccontroller.BuildObjectSyncName(node, string(acc.UID)),
			Namespace: acc.Namespace,
		},
		Spec: reliablesyncsv1alpha1.ObjectSyncSpec{
			ObjectAPIVersion: policyv1alpha1.SchemeGroupVersion.String(),
			ObjectKind:       serviceAccountAccessKind,
			ObjectName:       acc.Name,
		},
		Status: reliablesyncsv1alpha1.ObjectSyncStatus{ObjectResourceVersion: resourceVersion},
	}
}

func TestPolicyControllerRateLimiter(t *testing.T) {
	limiter := newPolicyControllerRateLimiter()
	request := controllerruntime.Request{NamespacedName: client.ObjectKey{Namespace: "test-namespace", Name: "default"}}
	want := []time.Duration{
		5 * time.Second,
		10 * time.Second,
		20 * time.Second,
		40 * time.Second,
		80 * time.Second,
		160 * time.Second,
		objectSyncRepairMaximumBackoff,
		objectSyncRepairMaximumBackoff,
	}
	for i, expected := range want {
		if got := limiter.When(request); got != expected {
			t.Fatalf("retry %d delay = %v, want %v", i+1, got, expected)
		}
	}
	limiter.Forget(request)
	if got := limiter.When(request); got != objectSyncRepairInitialBackoff {
		t.Fatalf("delay after Forget = %v, want %v", got, objectSyncRepairInitialBackoff)
	}
}

func TestGetObjectSyncRepairTargets(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := reliablesyncsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add reliablesync scheme: %v", err)
	}
	acc := &policyv1alpha1.ServiceAccountAccess{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "test-namespace",
			UID:       types.UID("current-access-uid"),
		},
	}
	present := newTestServiceAccountAccessObjectSync(acc, "present-node", "")
	stale := newTestServiceAccountAccessObjectSync(acc, "stale-node", "1")
	oldUID := newTestServiceAccountAccessObjectSync(acc, "old-uid-node", "1")
	oldUID.Name = synccontroller.BuildObjectSyncName("old-uid-node", "old-access-uid")
	deleting := newTestServiceAccountAccessObjectSync(acc, "deleting-node", "1")
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"test.kubeedge.io/hold"}
	legacy := newTestServiceAccountAccessObjectSync(acc, "legacy-node", "0")
	legacy.Spec.ObjectAPIVersion = ""
	legacy.Spec.ObjectKind = ""

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(present, stale, oldUID, deleting, legacy).Build()
	controller := &Controller{Client: fakeClient, Reader: fakeClient}
	got, err := controller.getObjectSyncRepairTargets(context.Background(), acc,
		[]string{"present-node", "stale-node", "missing-node", "old-uid-node", "deleting-node", "legacy-node"})
	if err != nil {
		t.Fatalf("getObjectSyncRepairTargets returned error: %v", err)
	}
	want := objectSyncRepairTargets{
		nodes:               []string{"missing-node", "old-uid-node", "deleting-node", "legacy-node"},
		missingNodes:        []string{"missing-node", "old-uid-node", "deleting-node"},
		incompleteSpecNodes: []string{"legacy-node"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("getObjectSyncRepairTargets = %#v, want %#v", got, want)
	}

	emptyUID := acc.DeepCopy()
	emptyUID.UID = ""
	if _, err := controller.getObjectSyncRepairTargets(context.Background(), emptyUID, []string{"present-node"}); err == nil {
		t.Fatal("expected an error for an empty ServiceAccountAccess UID")
	}

	readerErr := errors.New("api reader unavailable")
	controller.Reader = getErrorReader{Reader: fakeClient, err: readerErr}
	if _, err := controller.getObjectSyncRepairTargets(context.Background(), acc, []string{"present-node"}); err != nil {
		t.Fatalf("cached ObjectSync lookup unexpectedly used APIReader: %v", err)
	}
	if _, err := controller.getObjectSyncRepairTargets(context.Background(), acc, []string{"missing-node"}); !errors.Is(err, readerErr) {
		t.Fatalf("getObjectSyncRepairTargets error = %v, want wrapped %v", err, readerErr)
	}
}

func TestObjectSyncDeletePredicateAndMapping(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := policyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add policy scheme: %v", err)
	}
	access := &policyv1alpha1.ServiceAccountAccess{ObjectMeta: metav1.ObjectMeta{
		Name: "default", Namespace: "test-namespace", UID: types.UID("access-uid"),
	}}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(access).Build()
	controller := &Controller{Client: fakeClient, Reader: fakeClient}
	objectSync := &reliablesyncsv1alpha1.ObjectSync{
		ObjectMeta: metav1.ObjectMeta{Name: "edge-node.access-uid", Namespace: "test-namespace"},
		Spec: reliablesyncsv1alpha1.ObjectSyncSpec{
			ObjectKind: serviceAccountAccessKind,
			ObjectName: "default",
		},
	}
	p := objectSyncDeletePredicate()
	if p.Create(event.CreateEvent{Object: objectSync}) {
		t.Fatal("ObjectSync create event must not trigger reconciliation")
	}
	if p.Update(event.UpdateEvent{ObjectOld: objectSync, ObjectNew: objectSync.DeepCopy()}) {
		t.Fatal("ObjectSync update event must not trigger reconciliation")
	}
	if !p.Delete(event.DeleteEvent{Object: objectSync}) {
		t.Fatal("ServiceAccountAccess ObjectSync delete event must trigger reconciliation")
	}

	want := []controllerruntime.Request{{NamespacedName: client.ObjectKey{Namespace: "test-namespace", Name: "default"}}}
	if got := controller.mapObjectSyncFunc(context.Background(), objectSync); !reflect.DeepEqual(got, want) {
		t.Fatalf("mapObjectSyncFunc = %v, want %v", got, want)
	}
	legacy := objectSync.DeepCopy()
	legacy.Spec.ObjectAPIVersion = ""
	legacy.Spec.ObjectKind = ""
	if !p.Delete(event.DeleteEvent{Object: legacy}) {
		t.Fatal("legacy ServiceAccountAccess ObjectSync delete event must trigger reconciliation")
	}
	if got := controller.mapObjectSyncFunc(context.Background(), legacy); !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy mapObjectSyncFunc = %v, want %v", got, want)
	}
	oldLegacy := legacy.DeepCopy()
	oldLegacy.Name = "edge-node.old-access-uid"
	if got := controller.mapObjectSyncFunc(context.Background(), oldLegacy); len(got) != 0 {
		t.Fatalf("legacy ObjectSync with an old UID returned requests: %v", got)
	}
	unrelatedLegacy := legacy.DeepCopy()
	unrelatedLegacy.Spec.ObjectName = "workload"
	if got := controller.mapObjectSyncFunc(context.Background(), unrelatedLegacy); len(got) != 0 {
		t.Fatalf("unrelated legacy ObjectSync returned requests: %v", got)
	}
	nonAccess := objectSync.DeepCopy()
	nonAccess.Spec.ObjectKind = "Pod"
	if p.Delete(event.DeleteEvent{Object: nonAccess}) {
		t.Fatal("non-ServiceAccountAccess ObjectSync delete event must be ignored")
	}
	if got := controller.mapObjectSyncFunc(context.Background(), nonAccess); len(got) != 0 {
		t.Fatalf("mapObjectSyncFunc returned requests for non-access ObjectSync: %v", got)
	}
	missingName := legacy.DeepCopy()
	missingName.Spec.ObjectName = ""
	if p.Delete(event.DeleteEvent{Object: missingName}) {
		t.Fatal("legacy ObjectSync without an object name cannot be mapped")
	}
	missingUID := legacy.DeepCopy()
	missingUID.Name = "edge-node"
	if p.Delete(event.DeleteEvent{Object: missingUID}) {
		t.Fatal("legacy ObjectSync without a source UID cannot be mapped")
	}

	readerErr := errors.New("cache unavailable")
	retryController := &Controller{Reader: getErrorReader{Reader: fakeClient, err: readerErr}}
	retryRequests := retryController.mapObjectSyncFunc(context.Background(), legacy)
	if len(retryRequests) != 1 {
		t.Fatalf("legacy cache error mapping = %v, want one retry request", retryRequests)
	}
	objectName, objectUID, ok := legacyObjectSyncFromRetryRequest(retryRequests[0])
	if !ok || objectName != access.Name || objectUID != string(access.UID) {
		t.Fatalf("legacy retry request decoded as %q/%q/%v", objectName, objectUID, ok)
	}
	if result, err := retryController.Reconcile(context.Background(), retryRequests[0]); !errors.Is(err, readerErr) || result != (controllerruntime.Result{}) {
		t.Fatalf("legacy retry result/error = %v/%v, want empty/%v", result, err, readerErr)
	}
}

func TestEdgeNodeCreatePredicateAndMapping(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}
	if err := policyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add policy scheme: %v", err)
	}
	pods := []client.Object{
		&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-a", Namespace: "ns-a"}, Spec: v1.PodSpec{NodeName: "edge-node", ServiceAccountName: "default"}},
		&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-b", Namespace: "ns-a"}, Spec: v1.PodSpec{NodeName: "edge-node", ServiceAccountName: "default"}},
		&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-c", Namespace: "ns-b"}, Spec: v1.PodSpec{NodeName: "edge-node", ServiceAccountName: "agent"}},
		&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "other-node", Namespace: "ns-c"}, Spec: v1.PodSpec{NodeName: "other-node", ServiceAccountName: "default"}},
	}
	edgeNode := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "edge-node", Labels: map[string]string{"node-role.kubernetes.io/edge": ""}}}
	objects := append(append([]client.Object(nil), pods...), edgeNode)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithIndex(&v1.Pod{}, podNodeNameField, func(object client.Object) []string {
		return []string{object.(*v1.Pod).Spec.NodeName}
	}).Build()
	controller := &Controller{Client: fakeClient, Reader: fakeClient}
	requests := controller.mapNodeFunc(context.Background(), edgeNode)
	want := []controllerruntime.Request{
		{NamespacedName: client.ObjectKey{Namespace: "ns-a", Name: "default"}},
		{NamespacedName: client.ObjectKey{Namespace: "ns-b", Name: "agent"}},
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("mapNodeFunc = %v, want %v", requests, want)
	}

	p := edgeNodeCreatePredicate()
	if !p.Create(event.CreateEvent{Object: edgeNode}) {
		t.Fatal("edge node create event must trigger reconciliation")
	}
	nonEdgeNode := edgeNode.DeepCopy()
	nonEdgeNode.Labels = nil
	if p.Create(event.CreateEvent{Object: nonEdgeNode}) {
		t.Fatal("non-edge node create event must be ignored")
	}
	if p.Update(event.UpdateEvent{ObjectOld: edgeNode, ObjectNew: edgeNode.DeepCopy()}) {
		t.Fatal("edge node heartbeat update must be ignored")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: nonEdgeNode, ObjectNew: edgeNode}) {
		t.Fatal("edge-role label addition must trigger reconciliation")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: edgeNode, ObjectNew: nonEdgeNode}) {
		t.Fatal("edge-role label removal must trigger reconciliation")
	}
	if p.Delete(event.DeleteEvent{Object: edgeNode}) {
		t.Fatal("node delete event must be handled through ObjectSync deletion, not the Node watch")
	}

	podPredicate := controller.podEventPredicate(context.Background())
	oldPod := pods[0].(*v1.Pod)
	if !podPredicate.Create(event.CreateEvent{Object: oldPod}) {
		t.Fatal("edge Pod create event must trigger reconciliation")
	}
	statusOnly := oldPod.DeepCopy()
	statusOnly.Status.Phase = v1.PodRunning
	if podPredicate.Update(event.UpdateEvent{ObjectOld: oldPod, ObjectNew: statusOnly}) {
		t.Fatal("Pod status-only update must not trigger reconciliation")
	}
	deletingPod := oldPod.DeepCopy()
	now := metav1.Now()
	deletingPod.DeletionTimestamp = &now
	if !podPredicate.Update(event.UpdateEvent{ObjectOld: oldPod, ObjectNew: deletingPod}) {
		t.Fatal("Pod deletion transition must trigger reconciliation")
	}
	if podPredicate.Generic(event.GenericEvent{Object: oldPod}) {
		t.Fatal("Pod generic event must not trigger reconciliation")
	}

	listErr := errors.New("pod cache unavailable")
	failingClient := listErrorClient{Client: fakeClient, err: listErr}
	retryController := &Controller{Client: failingClient, Reader: failingClient}
	retryRequests := retryController.mapNodeFunc(context.Background(), edgeNode)
	if len(retryRequests) != 1 {
		t.Fatalf("Node list error mapping = %v, want one retry request", retryRequests)
	}
	if nodeName, ok := nodeNameFromRetryRequest(retryRequests[0]); !ok || nodeName != edgeNode.Name {
		t.Fatalf("Node retry request decoded as %q/%v", nodeName, ok)
	}
	if result, err := retryController.Reconcile(context.Background(), retryRequests[0]); !errors.Is(err, listErr) || result != (controllerruntime.Result{}) {
		t.Fatalf("Node retry result/error = %v/%v, want empty/%v", result, err, listErr)
	}
	retryController.Reader = fakeClient
	if result, err := retryController.Reconcile(context.Background(), retryRequests[0]); err != nil || result != (controllerruntime.Result{}) {
		t.Fatalf("recovered Node retry result/error = %v/%v, want empty/nil", result, err)
	}
}

func TestServiceAccountAccessRepairEventOrderings(t *testing.T) {
	const (
		namespace  = "test-namespace"
		nodeName   = "edge-node"
		accessName = "default"
	)
	newScheme := func(t *testing.T) *runtime.Scheme {
		t.Helper()
		scheme := runtime.NewScheme()
		for name, add := range map[string]func(*runtime.Scheme) error{
			"core":         v1.AddToScheme,
			"rbac":         rbacv1.AddToScheme,
			"policy":       policyv1alpha1.AddToScheme,
			"reliablesync": reliablesyncsv1alpha1.AddToScheme,
		} {
			if err := add(scheme); err != nil {
				t.Fatalf("failed to add %s scheme: %v", name, err)
			}
		}
		return scheme
	}
	newObjects := func() (*v1.ServiceAccount, *v1.Pod, *v1.Node, *policyv1alpha1.ServiceAccountAccess) {
		serviceAccount := &v1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
			Name: accessName, Namespace: namespace, UID: types.UID("service-account-uid"),
		}}
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: namespace},
			Spec: v1.PodSpec{
				NodeName:           nodeName,
				ServiceAccountName: accessName,
			},
		}
		node := &v1.Node{ObjectMeta: metav1.ObjectMeta{
			Name: nodeName, Labels: map[string]string{"node-role.kubernetes.io/edge": ""},
		}}
		access := &policyv1alpha1.ServiceAccountAccess{
			ObjectMeta: metav1.ObjectMeta{
				Name: accessName, Namespace: namespace, UID: types.UID("service-account-access-uid"),
			},
			Spec: policyv1alpha1.AccessSpec{
				ServiceAccount:    *serviceAccount.DeepCopy(),
				ServiceAccountUID: serviceAccount.UID,
			},
			Status: policyv1alpha1.AccessStatus{NodeList: []string{nodeName}},
		}
		return serviceAccount, pod, node, access
	}
	newClient := func(t *testing.T, scheme *runtime.Scheme, objects ...client.Object) client.Client {
		t.Helper()
		return fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(objects...).
			WithIndex(&v1.Pod{}, podServiceAccountNameField, func(object client.Object) []string {
				return []string{object.(*v1.Pod).Spec.ServiceAccountName}
			}).
			WithIndex(&v1.Pod{}, podNodeNameField, func(object client.Object) []string {
				return []string{object.(*v1.Pod).Spec.NodeName}
			}).
			WithStatusSubresource(&policyv1alpha1.ServiceAccountAccess{}).
			Build()
	}
	assertMessage := func(t *testing.T, recorder *recordingMessageLayer, operation string) {
		t.Helper()
		if len(recorder.messages) != 1 {
			t.Fatalf("message count = %d, want 1", len(recorder.messages))
		}
		message := recorder.messages[0]
		if message.GetOperation() != operation {
			t.Fatalf("message operation = %s, want %s", message.GetOperation(), operation)
		}
		target, err := messagelayer.GetNodeID(message)
		if err != nil {
			t.Fatalf("failed to parse message target: %v", err)
		}
		if target != nodeName {
			t.Fatalf("message target = %s, want %s", target, nodeName)
		}
	}

	t.Run("terminating ServiceAccount is never restored", func(t *testing.T) {
		serviceAccount, pod, node, access := newObjects()
		now := metav1.Now()
		serviceAccount.DeletionTimestamp = &now
		serviceAccount.Finalizers = []string{"test.kubeedge.io/hold"}
		fakeClient := newClient(t, newScheme(t), serviceAccount, pod, node, access)
		recorder := &recordingMessageLayer{}
		controller := &Controller{Client: fakeClient, Reader: fakeClient, MessageLayer: recorder}
		request := controllerruntime.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: accessName}}

		result, err := controller.Reconcile(context.Background(), request)
		if err != nil {
			t.Fatalf("terminating ServiceAccount reconcile returned error: %v", err)
		}
		if result != (controllerruntime.Result{}) {
			t.Fatalf("terminating ServiceAccount reconcile result = %v, want empty", result)
		}
		assertMessage(t, recorder, model.DeleteOperation)
		if err := fakeClient.Get(context.Background(), request.NamespacedName, &policyv1alpha1.ServiceAccountAccess{}); !apierror.IsNotFound(err) {
			t.Fatalf("terminating ServiceAccount access delete error = %v, want NotFound", err)
		}

		recorder.messages = nil
		result, err = controller.Reconcile(context.Background(), request)
		if err != nil {
			t.Fatalf("terminating ServiceAccount ensure returned error: %v", err)
		}
		if result != (controllerruntime.Result{}) || len(recorder.messages) != 0 {
			t.Fatalf("terminating ServiceAccount ensure result/messages = %v/%d, want empty/0", result, len(recorder.messages))
		}
		if err := fakeClient.Get(context.Background(), request.NamespacedName, &policyv1alpha1.ServiceAccountAccess{}); !apierror.IsNotFound(err) {
			t.Fatalf("terminating ServiceAccount restored access error = %v, want NotFound", err)
		}
	})

	t.Run("ObjectSync delete before Node create restores deleted access", func(t *testing.T) {
		serviceAccount, pod, node, access := newObjects()
		fakeClient := newClient(t, newScheme(t), serviceAccount, pod, access)
		recorder := &recordingMessageLayer{}
		controller := &Controller{Client: fakeClient, Reader: fakeClient, MessageLayer: recorder}
		request := controllerruntime.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: accessName}}

		if _, err := controller.Reconcile(context.Background(), request); err != nil {
			t.Fatalf("reconcile without Node returned error: %v", err)
		}
		assertMessage(t, recorder, model.DeleteOperation)
		if err := fakeClient.Get(context.Background(), request.NamespacedName, &policyv1alpha1.ServiceAccountAccess{}); !apierror.IsNotFound(err) {
			t.Fatalf("ServiceAccountAccess after Node deletion error = %v, want NotFound", err)
		}

		if err := fakeClient.Create(context.Background(), node); err != nil {
			t.Fatalf("failed to recreate Node: %v", err)
		}
		requests := controller.mapNodeFunc(context.Background(), node)
		if len(requests) != 1 || requests[0] != request {
			t.Fatalf("Node create mapping = %v, want %v", requests, []controllerruntime.Request{request})
		}
		recorder.messages = nil
		result, err := controller.Reconcile(context.Background(), requests[0])
		if err != nil {
			t.Fatalf("missing access ensure returned error: %v", err)
		}
		if !result.Requeue {
			t.Fatalf("missing access ensure result = %v, want immediate requeue", result)
		}
		if len(recorder.messages) != 0 {
			t.Fatalf("ensure sent %d messages before normal reconciliation, want 0", len(recorder.messages))
		}

		result, err = controller.Reconcile(context.Background(), request)
		if err != nil {
			t.Fatalf("reconcile restored access returned error: %v", err)
		}
		if result != (controllerruntime.Result{}) {
			t.Fatalf("restored access result = %v, want empty", result)
		}
		assertMessage(t, recorder, model.UpdateOperation)
		restored := &policyv1alpha1.ServiceAccountAccess{}
		if err := fakeClient.Get(context.Background(), request.NamespacedName, restored); err != nil {
			t.Fatalf("failed to get restored access: %v", err)
		}
		if !reflect.DeepEqual(restored.Status.NodeList, []string{nodeName}) {
			t.Fatalf("restored nodeList = %v, want %v", restored.Status.NodeList, []string{nodeName})
		}
	})

	t.Run("Node create before ObjectSync delete repairs exact missing record", func(t *testing.T) {
		serviceAccount, pod, node, access := newObjects()
		objectSync := newTestServiceAccountAccessObjectSync(access, nodeName, "1")
		fakeClient := newClient(t, newScheme(t), serviceAccount, pod, node, access, objectSync)
		recorder := &recordingMessageLayer{}
		controller := &Controller{Client: fakeClient, Reader: fakeClient, MessageLayer: recorder}
		request := controllerruntime.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: accessName}}

		result, err := controller.Reconcile(context.Background(), request)
		if err != nil {
			t.Fatalf("stable reconcile returned error: %v", err)
		}
		if result != (controllerruntime.Result{}) || len(recorder.messages) != 0 {
			t.Fatalf("stable reconcile result/messages = %v/%d, want empty/0", result, len(recorder.messages))
		}

		deletedSnapshot := objectSync.DeepCopy()
		if err := fakeClient.Delete(context.Background(), objectSync); err != nil {
			t.Fatalf("failed to delete ObjectSync: %v", err)
		}
		requests := controller.mapObjectSyncFunc(context.Background(), deletedSnapshot)
		if len(requests) != 1 || requests[0] != request {
			t.Fatalf("ObjectSync delete mapping = %v, want %v", requests, []controllerruntime.Request{request})
		}
		result, err = controller.Reconcile(context.Background(), requests[0])
		if err != nil {
			t.Fatalf("missing ObjectSync reconcile returned error: %v", err)
		}
		if !result.Requeue {
			t.Fatalf("missing ObjectSync result = %v, want rate-limited requeue", result)
		}
		assertMessage(t, recorder, model.InsertOperation)
	})

	t.Run("legacy ObjectSync spec is backfilled once", func(t *testing.T) {
		for _, resourceVersion := range []string{"0", "1"} {
			t.Run("object-resource-version-"+resourceVersion, func(t *testing.T) {
				serviceAccount, pod, node, access := newObjects()
				objectSync := newTestServiceAccountAccessObjectSync(access, nodeName, resourceVersion)
				objectSync.Spec.ObjectAPIVersion = ""
				objectSync.Spec.ObjectKind = ""
				fakeClient := newClient(t, newScheme(t), serviceAccount, pod, node, access, objectSync)
				recorder := &recordingMessageLayer{}
				controller := &Controller{Client: fakeClient, Reader: fakeClient, MessageLayer: recorder}
				request := controllerruntime.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: accessName}}

				result, err := controller.Reconcile(context.Background(), request)
				if err != nil {
					t.Fatalf("legacy ObjectSync reconcile returned error: %v", err)
				}
				if !result.Requeue {
					t.Fatalf("legacy ObjectSync result = %v, want rate-limited requeue", result)
				}
				assertMessage(t, recorder, model.InsertOperation)

				objectSync.Spec.ObjectAPIVersion = policyv1alpha1.SchemeGroupVersion.String()
				objectSync.Spec.ObjectKind = serviceAccountAccessKind
				if err := fakeClient.Update(context.Background(), objectSync); err != nil {
					t.Fatalf("failed to simulate CloudHub ObjectSync backfill: %v", err)
				}
				recorder.messages = nil
				result, err = controller.Reconcile(context.Background(), request)
				if err != nil {
					t.Fatalf("backfilled ObjectSync reconcile returned error: %v", err)
				}
				if result != (controllerruntime.Result{}) {
					t.Fatalf("backfilled ObjectSync result = %v, want empty", result)
				}
				if len(recorder.messages) != 0 {
					t.Fatalf("backfilled ObjectSync reconcile sent %d messages, want 0", len(recorder.messages))
				}
			})
		}
	})
}

func TestRuleReadErrorDoesNotOverwriteAccess(t *testing.T) {
	const namespace = "test-namespace"
	scheme := runtime.NewScheme()
	for name, add := range map[string]func(*runtime.Scheme) error{
		"core":   v1.AddToScheme,
		"rbac":   rbacv1.AddToScheme,
		"policy": policyv1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("failed to add %s scheme: %v", name, err)
		}
	}
	serviceAccount := &v1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: "default", Namespace: namespace, UID: types.UID("service-account-uid"),
	}}
	clusterRole := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "reader"}}
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "reader"},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     clusterRole.Name,
		},
		Subjects: []rbacv1.Subject{{
			Kind: rbacv1.ServiceAccountKind, Name: serviceAccount.Name, Namespace: namespace,
		}},
	}
	access := &policyv1alpha1.ServiceAccountAccess{
		ObjectMeta: metav1.ObjectMeta{Name: serviceAccount.Name, Namespace: namespace, UID: types.UID("access-uid")},
		Spec: policyv1alpha1.AccessSpec{
			ServiceAccount:    *serviceAccount.DeepCopy(),
			ServiceAccountUID: serviceAccount.UID,
			AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{
				ClusterRoleBinding: *clusterRoleBinding.DeepCopy(),
				Rules:              []rbacv1.PolicyRule{{Verbs: []string{"get"}, Resources: []string{"pods"}}},
			}},
		},
	}
	originalSpec := access.Spec.DeepCopy()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(serviceAccount, clusterRole, clusterRoleBinding, access).
		WithStatusSubresource(&policyv1alpha1.ServiceAccountAccess{}).
		Build()
	readerErr := errors.New("clusterrole get forbidden")
	recorder := &recordingMessageLayer{}
	controller := &Controller{
		Client:       fakeClient,
		Reader:       roleGetErrorReader{Reader: fakeClient, err: readerErr},
		MessageLayer: recorder,
	}
	request := controllerruntime.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: serviceAccount.Name}}
	result, err := controller.Reconcile(context.Background(), request)
	if !errors.Is(err, readerErr) || result != (controllerruntime.Result{}) {
		t.Fatalf("rule read result/error = %v/%v, want empty/%v", result, err, readerErr)
	}
	persisted := &policyv1alpha1.ServiceAccountAccess{}
	if err := fakeClient.Get(context.Background(), request.NamespacedName, persisted); err != nil {
		t.Fatalf("failed to get persisted access: %v", err)
	}
	if !reflect.DeepEqual(&persisted.Spec, originalSpec) {
		t.Fatalf("rule read error changed access spec: got %#v, want %#v", persisted.Spec, *originalSpec)
	}
	if len(recorder.messages) != 0 {
		t.Fatalf("rule read error sent %d messages, want 0", len(recorder.messages))
	}
}

func TestSyncRules(t *testing.T) {
	var pod1 v1.Pod
	err := json.Unmarshal([]byte(podStr1), &pod1)
	if err != nil {
		t.Errorf("Failed to unmarshal pod1: %v", err)
	}
	var pod2 v1.Pod
	err = json.Unmarshal([]byte(podStr2), &pod2)
	if err != nil {
		t.Errorf("Failed to unmarshal pod2: %v", err)
	}
	var podWithoutNodeName v1.Pod
	err = json.Unmarshal([]byte(podStrWithoutNodeName), &podWithoutNodeName)
	if err != nil {
		t.Errorf("Failed to unmarshal podWithoutNodeName: %v", err)
	}
	var rb2 rbacv1.RoleBinding
	err = json.Unmarshal([]byte(rbStr2), &rb2)
	if err != nil {
		t.Errorf("Failed to unmarshal rb2: %v", err)
	}
	var crb1 rbacv1.ClusterRoleBinding
	err = json.Unmarshal([]byte(crbStr1), &crb1)
	if err != nil {
		t.Errorf("Failed to unmarshal crb1: %v", err)
	}
	var sa1 v1.ServiceAccount
	err = json.Unmarshal([]byte(saStr1), &sa1)
	if err != nil {
		t.Errorf("Failed to unmarshal sa1: %v", err)
	}
	var sa2 v1.ServiceAccount
	err = json.Unmarshal([]byte(saStr2), &sa2)
	if err != nil {
		t.Errorf("Failed to unmarshal sa2: %v", err)
	}
	var cr1 rbacv1.ClusterRole
	err = json.Unmarshal([]byte(crStr1), &cr1)
	if err != nil {
		t.Errorf("Failed to unmarshal cr1: %v", err)
	}
	var rb1 rbacv1.RoleBinding
	err = json.Unmarshal([]byte(rbStr1), &rb1)
	if err != nil {
		t.Errorf("Failed to unmarshal rb1: %v", err)
	}
	var role1 rbacv1.Role
	err = json.Unmarshal([]byte(roleStr1), &role1)
	if err != nil {
		t.Errorf("Failed to unmarshal role1: %v", err)
	}
	var role2 rbacv1.Role
	err = json.Unmarshal([]byte(roleStr2), &role2)
	if err != nil {
		t.Errorf("Failed to unmarshal role1: %v", err)
	}
	nodeList := &v1.NodeList{
		Items: []v1.Node{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-node",
					Labels: map[string]string{
						"node-role.kubernetes.io/edge": "",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-node-2",
					Labels: map[string]string{
						"node-role.kubernetes.io/edge": "",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-node-3",
					Labels: map[string]string{
						"node-role.kubernetes.io/edge": "",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-node-4",
				},
			},
		},
	}
	var nodeStatus1 = policyv1alpha1.AccessStatus{NodeList: []string{"my-node"}}
	var nodeStatus2 = policyv1alpha1.AccessStatus{NodeList: []string{"my-node", "my-node-2"}}
	var nodeStatus3 = policyv1alpha1.AccessStatus{NodeList: []string{"my-node-2"}}
	var nodeStatus4 = policyv1alpha1.AccessStatus{NodeList: []string{"my-node-2", "my-node-3"}}
	var saaUID = types.UID("service-account-access-uid")
	var saa1 = policyv1alpha1.ServiceAccountAccess{
		ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace", UID: saaUID},
		Spec: policyv1alpha1.AccessSpec{
			ServiceAccount:           sa1,
			AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: rb1, Rules: role1.Rules}},
			AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: crb1, Rules: cr1.Rules}},
		},
		Status: nodeStatus1,
	}
	var saa2 = policyv1alpha1.ServiceAccountAccess{
		ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace", UID: saaUID},
		Spec: policyv1alpha1.AccessSpec{
			ServiceAccount:           sa1,
			AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: rb1, Rules: role1.Rules}},
			AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: crb1, Rules: cr1.Rules}},
		},
		Status: nodeStatus2,
	}
	var saa3 = policyv1alpha1.ServiceAccountAccess{
		ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace", UID: saaUID},
		Spec: policyv1alpha1.AccessSpec{
			ServiceAccount:           sa1,
			AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: rb1, Rules: role1.Rules}},
			AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: crb1, Rules: cr1.Rules}},
		},
		Status: nodeStatus3,
	}
	var saa4 = policyv1alpha1.ServiceAccountAccess{
		ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace", UID: saaUID},
		Spec: policyv1alpha1.AccessSpec{
			ServiceAccount:           sa1,
			AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: rb1, Rules: role1.Rules}},
			AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: crb1, Rules: cr1.Rules}},
		},
		Status: nodeStatus4,
	}
	var saa5 = policyv1alpha1.ServiceAccountAccess{
		ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace", UID: saaUID},
		Spec: policyv1alpha1.AccessSpec{
			ServiceAccount:           sa1,
			AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: rb1, Rules: role1.Rules}},
			AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: crb1, Rules: cr1.Rules}},
		},
		Status: nodeStatus1,
	}
	var saaDiffName = policyv1alpha1.ServiceAccountAccess{
		ObjectMeta: metav1.ObjectMeta{Name: "sa2", Namespace: "my-namespace", UID: types.UID("different-service-account-access-uid")},
		Spec: policyv1alpha1.AccessSpec{
			ServiceAccount: sa2,
		},
		Status: nodeStatus2,
	}
	var saaDeletion = policyv1alpha1.ServiceAccountAccess{
		ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace", UID: saaUID, DeletionTimestamp: &metav1.Time{Time: time.Now()}, Finalizers: []string{"test"}},
		Spec: policyv1alpha1.AccessSpec{
			ServiceAccount:           sa1,
			AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: rb1, Rules: role1.Rules}},
			AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: crb1, Rules: cr1.Rules}},
		},
		Status: nodeStatus1,
	}
	var tests = []struct {
		name                   string
		input                  *policyv1alpha1.ServiceAccountAccess
		obj                    []client.Object
		missingObjectSyncNodes []string
		reconcileResult        controllerruntime.Result
		output                 *policyv1alpha1.ServiceAccountAccess
		msgOpr                 []string
		msgNodes               []string
	}{
		{
			name:                   "rolebinding update sends only Update when ObjectSync is missing",
			input:                  saa1.DeepCopy(),
			missingObjectSyncNodes: []string{"my-node"},
			obj: []client.Object{saa1.DeepCopy(), pod1.DeepCopy(), sa1.DeepCopy(), rb1.DeepCopy(), crb1.DeepCopy(),
				cr1.DeepCopy(), role1.DeepCopy(), rb2.DeepCopy(), role2.DeepCopy()},
			reconcileResult: controllerruntime.Result{},
			output: &policyv1alpha1.ServiceAccountAccess{ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount:           *sa1.DeepCopy(),
					AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: *rb1.DeepCopy(), Rules: role1.Rules}, {RoleBinding: *rb2.DeepCopy(), Rules: role2.Rules}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: *crb1.DeepCopy(), Rules: cr1.Rules}},
				},
				Status: policyv1alpha1.AccessStatus{NodeList: []string{"my-node"}},
			},
			msgOpr:   []string{model.UpdateOperation},
			msgNodes: []string{"my-node"},
		},
		{
			name:  "rolebinding updated and inserted new node",
			input: saa1.DeepCopy(),
			obj: []client.Object{saa1.DeepCopy(), pod1.DeepCopy(), pod2.DeepCopy(), sa1.DeepCopy(), rb1.DeepCopy(),
				crb1.DeepCopy(), cr1.DeepCopy(), role1.DeepCopy(), rb2.DeepCopy(), role2.DeepCopy()},
			reconcileResult: controllerruntime.Result{},
			output: &policyv1alpha1.ServiceAccountAccess{ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount:           *sa1.DeepCopy(),
					AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: *rb1.DeepCopy(), Rules: role1.Rules}, {RoleBinding: *rb2.DeepCopy(), Rules: role2.Rules}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: *crb1.DeepCopy(), Rules: cr1.Rules}},
				},
				Status: policyv1alpha1.AccessStatus{NodeList: []string{"my-node", "my-node-2"}},
			},
			msgOpr: []string{model.UpdateOperation, model.UpdateOperation},
		},
		{
			name:  "rolebinding updated and inserted/deleted new node",
			input: saa3.DeepCopy(),
			obj: []client.Object{saa3.DeepCopy(), pod1.DeepCopy(), sa1.DeepCopy(), rb1.DeepCopy(), crb1.DeepCopy(),
				cr1.DeepCopy(), role1.DeepCopy(), rb2.DeepCopy(), role2.DeepCopy()},
			reconcileResult: controllerruntime.Result{},
			output: &policyv1alpha1.ServiceAccountAccess{ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount:           *sa1.DeepCopy(),
					AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: *rb1.DeepCopy(), Rules: role1.Rules}, {RoleBinding: *rb2.DeepCopy(), Rules: role2.Rules}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: *crb1.DeepCopy(), Rules: cr1.Rules}},
				},
				Status: policyv1alpha1.AccessStatus{NodeList: []string{"my-node"}},
			},
			msgOpr:   []string{model.DeleteOperation, model.UpdateOperation},
			msgNodes: []string{"my-node", "my-node-2"},
		},
		{
			name:  "rolebinding updated and inserted/deleted/updated new node",
			input: saa4.DeepCopy(),
			obj: []client.Object{saa4.DeepCopy(), pod1.DeepCopy(), pod2.DeepCopy(), sa1.DeepCopy(), rb1.DeepCopy(),
				crb1.DeepCopy(), cr1.DeepCopy(), role1.DeepCopy(), rb2.DeepCopy(), role2.DeepCopy()},
			reconcileResult: controllerruntime.Result{},
			output: &policyv1alpha1.ServiceAccountAccess{ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount:           *sa1.DeepCopy(),
					AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: *rb1.DeepCopy(), Rules: role1.Rules}, {RoleBinding: *rb2.DeepCopy(), Rules: role2.Rules}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: *crb1.DeepCopy(), Rules: cr1.Rules}},
				},
				Status: policyv1alpha1.AccessStatus{NodeList: []string{"my-node", "my-node-2"}},
			},
			msgOpr:   []string{model.DeleteOperation, model.UpdateOperation, model.UpdateOperation},
			msgNodes: []string{"my-node", "my-node-2", "my-node-3"},
		},
		{
			name:            "rolebinding updated and inserted new node with none old node",
			input:           saa5.DeepCopy(),
			obj:             []client.Object{saa5.DeepCopy(), &pod1, &pod2, &sa1, &rb1, &crb1, &cr1, &role1, &rb2, &role2},
			reconcileResult: controllerruntime.Result{},
			output: &policyv1alpha1.ServiceAccountAccess{ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount:           *sa1.DeepCopy(),
					AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: *rb1.DeepCopy(), Rules: role1.Rules}, {RoleBinding: *rb2.DeepCopy(), Rules: role2.Rules}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: *crb1.DeepCopy(), Rules: cr1.Rules}},
				},
				Status: policyv1alpha1.AccessStatus{NodeList: []string{"my-node", "my-node-2"}},
			},
			msgOpr: []string{model.UpdateOperation, model.UpdateOperation},
		},
		{
			name:  "rolebinding updated and deleted old node only",
			input: saa4.DeepCopy(),
			obj: []client.Object{saa4.DeepCopy(), podWithoutNodeName.DeepCopy(), sa1.DeepCopy(), rb1.DeepCopy(),
				crb1.DeepCopy(), cr1.DeepCopy(), role1.DeepCopy(), rb2.DeepCopy(), role2.DeepCopy()},
			reconcileResult: controllerruntime.Result{},
			output: &policyv1alpha1.ServiceAccountAccess{ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount:           *sa1.DeepCopy(),
					AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: *rb1.DeepCopy(), Rules: role1.Rules}, {RoleBinding: *rb2.DeepCopy(), Rules: role2.Rules}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: *crb1.DeepCopy(), Rules: cr1.Rules}},
				},
				Status: policyv1alpha1.AccessStatus{NodeList: []string{}},
			},
			msgOpr:   []string{model.DeleteOperation, model.DeleteOperation},
			msgNodes: []string{"my-node-2", "my-node-3"},
		},
		{
			name:  "rolebinding updated and updated/deleted node",
			input: saa2.DeepCopy(),
			obj: []client.Object{saa2.DeepCopy(), pod1.DeepCopy(), sa1.DeepCopy(), rb1.DeepCopy(),
				crb1.DeepCopy(), cr1.DeepCopy(), role1.DeepCopy(), rb2.DeepCopy(), role2.DeepCopy()},
			reconcileResult: controllerruntime.Result{},
			output: &policyv1alpha1.ServiceAccountAccess{ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount:           *sa1.DeepCopy(),
					AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: *rb1.DeepCopy(), Rules: role1.Rules}, {RoleBinding: *rb2.DeepCopy(), Rules: role2.Rules}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: *crb1.DeepCopy(), Rules: cr1.Rules}},
				},
				Status: policyv1alpha1.AccessStatus{NodeList: []string{"my-node"}},
			},
			msgOpr: []string{model.DeleteOperation, model.UpdateOperation},
		},
		{
			name:  "rolebinding updated and none nodes",
			input: saa5.DeepCopy(),
			obj: []client.Object{saa5.DeepCopy(), podWithoutNodeName.DeepCopy(), sa1.DeepCopy(), rb1.DeepCopy(),
				crb1.DeepCopy(), cr1.DeepCopy(), role1.DeepCopy(), rb2.DeepCopy(), role2.DeepCopy()},
			reconcileResult: controllerruntime.Result{},
			output: &policyv1alpha1.ServiceAccountAccess{ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount:           *sa1.DeepCopy(),
					AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: *rb1.DeepCopy(), Rules: role1.Rules}, {RoleBinding: *rb2.DeepCopy(), Rules: role2.Rules}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: *crb1.DeepCopy(), Rules: cr1.Rules}},
				},
				Status: policyv1alpha1.AccessStatus{NodeList: []string{}},
			},
			msgOpr:   []string{model.DeleteOperation},
			msgNodes: []string{"my-node"},
		},
		{
			name:  "service account not found",
			input: saa5.DeepCopy(),
			obj: []client.Object{saa5.DeepCopy(), podWithoutNodeName.DeepCopy(), sa2.DeepCopy(), rb1.DeepCopy(),
				crb1.DeepCopy(), cr1.DeepCopy(), role1.DeepCopy(), rb2.DeepCopy(), role2.DeepCopy()},
			reconcileResult: controllerruntime.Result{},
			output: &policyv1alpha1.ServiceAccountAccess{
				Status: policyv1alpha1.AccessStatus{NodeList: []string{"my-node"}},
			},
			msgOpr: []string{model.DeleteOperation},
		},
		{
			name:  "insert only",
			input: saa1.DeepCopy(),
			obj: []client.Object{saa1.DeepCopy(), pod1.DeepCopy(), pod2.DeepCopy(), sa1.DeepCopy(), rb1.DeepCopy(),
				crb1.DeepCopy(), cr1.DeepCopy(), role1.DeepCopy()},
			reconcileResult: controllerruntime.Result{Requeue: true},
			output: &policyv1alpha1.ServiceAccountAccess{ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount:           *sa1.DeepCopy(),
					AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: *rb1.DeepCopy(), Rules: role1.Rules}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: *crb1.DeepCopy(), Rules: cr1.Rules}},
				},
				Status: policyv1alpha1.AccessStatus{NodeList: []string{"my-node", "my-node-2"}},
			},
			msgOpr: []string{model.InsertOperation},
		},
		{
			name:            "stable node list and ObjectSync do not resend",
			input:           saa1.DeepCopy(),
			obj:             []client.Object{saa1.DeepCopy(), pod1.DeepCopy(), sa1.DeepCopy(), rb1.DeepCopy(), crb1.DeepCopy(), cr1.DeepCopy(), role1.DeepCopy()},
			reconcileResult: controllerruntime.Result{},
			output:          saa1.DeepCopy(),
			msgOpr:          []string{},
		},
		{
			name:                   "missing ObjectSync is repaired when node list is unchanged",
			input:                  saa1.DeepCopy(),
			obj:                    []client.Object{saa1.DeepCopy(), pod1.DeepCopy(), sa1.DeepCopy(), rb1.DeepCopy(), crb1.DeepCopy(), cr1.DeepCopy(), role1.DeepCopy()},
			missingObjectSyncNodes: []string{"my-node"},
			reconcileResult:        controllerruntime.Result{Requeue: true},
			output:                 saa1.DeepCopy(),
			msgOpr:                 []string{model.InsertOperation},
			msgNodes:               []string{"my-node"},
		},
		{
			name:                   "new node and retained node missing ObjectSync are each inserted once",
			input:                  saa1.DeepCopy(),
			obj:                    []client.Object{saa1.DeepCopy(), pod1.DeepCopy(), pod2.DeepCopy(), sa1.DeepCopy(), rb1.DeepCopy(), crb1.DeepCopy(), cr1.DeepCopy(), role1.DeepCopy()},
			missingObjectSyncNodes: []string{"my-node"},
			reconcileResult:        controllerruntime.Result{Requeue: true},
			output: &policyv1alpha1.ServiceAccountAccess{ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount:           *sa1.DeepCopy(),
					AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: *rb1.DeepCopy(), Rules: role1.Rules}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: *crb1.DeepCopy(), Rules: cr1.Rules}},
				},
				Status: policyv1alpha1.AccessStatus{NodeList: []string{"my-node", "my-node-2"}},
			},
			msgOpr:   []string{model.InsertOperation, model.InsertOperation},
			msgNodes: []string{"my-node", "my-node-2"},
		},
		{
			name:  "delete only updates status",
			input: saa2.DeepCopy(),
			obj: []client.Object{saa2.DeepCopy(), pod1.DeepCopy(), sa1.DeepCopy(), rb1.DeepCopy(),
				crb1.DeepCopy(), cr1.DeepCopy(), role1.DeepCopy()},
			reconcileResult: controllerruntime.Result{},
			output: &policyv1alpha1.ServiceAccountAccess{ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount:           *sa1.DeepCopy(),
					AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: *rb1.DeepCopy(), Rules: role1.Rules}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: *crb1.DeepCopy(), Rules: cr1.Rules}},
				},
				Status: policyv1alpha1.AccessStatus{NodeList: []string{"my-node"}},
			},
			msgOpr: []string{model.DeleteOperation},
		},
		{
			name:  "reconcile failed cause serviceaccountaccess not found",
			input: saaDiffName.DeepCopy(),
			obj: []client.Object{saa1.DeepCopy(), pod1.DeepCopy(), pod2.DeepCopy(), sa1.DeepCopy(), rb1.DeepCopy(),
				crb1.DeepCopy(), cr1.DeepCopy(), role1.DeepCopy()},
			reconcileResult: controllerruntime.Result{},
			output:          &policyv1alpha1.ServiceAccountAccess{},
			msgOpr:          []string{},
		},
		{
			name:  "reconcile failed cause deletionTimestamp not nil",
			input: saaDeletion.DeepCopy(),
			obj: []client.Object{saaDeletion.DeepCopy(), pod1.DeepCopy(), sa1.DeepCopy(), rb1.DeepCopy(), crb1.DeepCopy(),
				cr1.DeepCopy(), role1.DeepCopy()},
			reconcileResult: controllerruntime.Result{},
			output: &policyv1alpha1.ServiceAccountAccess{ObjectMeta: metav1.ObjectMeta{Name: "sa1", Namespace: "my-namespace"},
				Spec: policyv1alpha1.AccessSpec{
					ServiceAccount:           *sa1.DeepCopy(),
					AccessRoleBinding:        []policyv1alpha1.AccessRoleBinding{{RoleBinding: *rb1.DeepCopy(), Rules: role1.Rules}},
					AccessClusterRoleBinding: []policyv1alpha1.AccessClusterRoleBinding{{ClusterRoleBinding: *crb1.DeepCopy(), Rules: cr1.Rules}},
				},
				Status: policyv1alpha1.AccessStatus{NodeList: []string{"my-node"}},
			},
			msgOpr: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var accessScheme = runtime.NewScheme()
			if err := policyv1alpha1.AddToScheme(accessScheme); err != nil {
				t.Errorf("Failed to add policyv1alpha1 scheme: %v", err)
			}
			if err := v1.AddToScheme(accessScheme); err != nil {
				t.Errorf("Failed to add v1 scheme: %v", err)
			}
			if err := rbacv1.AddToScheme(accessScheme); err != nil {
				t.Errorf("Failed to add rbacv1 scheme: %v", err)
			}
			if err := reliablesyncsv1alpha1.AddToScheme(accessScheme); err != nil {
				t.Errorf("Failed to add reliablesyncsv1alpha1 scheme: %v", err)
			}
			pdStrategyTypeIndexer := func(obj client.Object) []string {
				serviceAccountName := "sa1"
				return []string{serviceAccountName}
			}
			objects := append([]client.Object(nil), tt.obj...)
			missingObjectSyncNodes := make(map[string]struct{}, len(tt.missingObjectSyncNodes))
			for _, node := range tt.missingObjectSyncNodes {
				missingObjectSyncNodes[node] = struct{}{}
			}
			for _, node := range tt.input.Status.NodeList {
				if _, missing := missingObjectSyncNodes[node]; missing {
					continue
				}
				objects = append(objects, newTestServiceAccountAccessObjectSync(tt.input, node, "1"))
			}
			fakeClient := fake.NewClientBuilder().WithScheme(accessScheme).WithObjects(objects...).WithLists(nodeList).WithIndex(&v1.Pod{}, podServiceAccountNameField, pdStrategyTypeIndexer).WithStatusSubresource(tt.input).Build()
			messageLayer := &recordingMessageLayer{}
			ctr := &Controller{
				Client:       fakeClient,
				Reader:       fakeClient,
				MessageLayer: messageLayer,
			}
			inputObj := tt.input.DeepCopy()
			rst, err := ctr.Reconcile(context.Background(), controllerruntime.Request{NamespacedName: types.NamespacedName{Name: inputObj.Name, Namespace: inputObj.Namespace}})
			if err != nil {
				t.Errorf("TestCase %q Failed to syncRules: %v", tt.name, err)
			}
			var oprs []string
			var messageNodes []string
			for _, message := range messageLayer.messages {
				oprs = append(oprs, message.GetOperation())
				node, nodeErr := messagelayer.GetNodeID(message)
				if nodeErr != nil {
					t.Errorf("TestCase %q failed to parse message node: %v", tt.name, nodeErr)
				} else {
					messageNodes = append(messageNodes, node)
				}
			}
			sort.Strings(oprs)
			sort.Strings(tt.msgOpr)
			if !equality.Semantic.DeepEqual(oprs, tt.msgOpr) {
				t.Errorf("TestCase %q message operation got %v, want %v", tt.name, oprs, tt.msgOpr)
			}
			if tt.msgNodes != nil {
				sort.Strings(messageNodes)
				sort.Strings(tt.msgNodes)
				if !equality.Semantic.DeepEqual(messageNodes, tt.msgNodes) {
					t.Errorf("TestCase %q message nodes got %v, want %v", tt.name, messageNodes, tt.msgNodes)
				}
			}
			if !equality.Semantic.DeepEqual(rst, tt.reconcileResult) {
				t.Errorf("TestCase %q Expected: %v, got: %v", tt.name, tt.reconcileResult, rst)
			}
			saa := &policyv1alpha1.ServiceAccountAccess{}
			err = fakeClient.Get(context.Background(), types.NamespacedName{Name: tt.input.Name, Namespace: tt.input.Namespace}, saa)
			if err != nil && apierror.IsNotFound(err) {
				return
			} else if err != nil {
				t.Errorf("TestCase %q Failed to get saa: %v", tt.name, err)
			}
			if !equality.Semantic.DeepEqual((*saa).Spec.ServiceAccount, (*(tt.output)).Spec.ServiceAccount) {
				t.Errorf("TestCase %q Expected spec serviceaccount: %+v, got: %+v", tt.name, tt.output.Spec.ServiceAccount, saa.Spec.ServiceAccount)
			}
			if !equality.Semantic.DeepEqual((*saa).Spec.AccessClusterRoleBinding, (*(tt.output)).Spec.AccessClusterRoleBinding) {
				t.Errorf("TestCase %q Expected spec crb: %+v, got: %+v", tt.name, tt.output.Spec.AccessClusterRoleBinding, saa.Spec.AccessClusterRoleBinding)
			}
			if !equality.Semantic.DeepEqual((*saa).Spec.AccessRoleBinding, (*(tt.output)).Spec.AccessRoleBinding) {
				t.Errorf("TestCase %q Expected spec rb: %+v, got: %+v", tt.name, tt.output.Spec.AccessRoleBinding, saa.Spec.AccessRoleBinding)
			}
			sort.Strings(saa.Status.NodeList)
			sort.Strings(tt.output.Status.NodeList)
			if !equality.Semantic.DeepEqual(saa.Status.NodeList, tt.output.Status.NodeList) {
				t.Errorf("TestCase %q Expected status: %v, got: %v", tt.name, tt.output.Status.NodeList, saa.Status.NodeList)
			}
		})
	}
}

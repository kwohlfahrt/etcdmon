package etcdmon

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"testing"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/klog/v2/ktesting"
)

type EventType int

type fakeEtcd struct {
	additions []uint64
	removals  []uint64
	members   []*etcdserverpb.Member
}

func (*fakeEtcd) Close() error {
	return nil
}

func genId(url string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(url))
	return h.Sum64()
}

func (f *fakeEtcd) MemberAdd(ctx context.Context, urls []string) (*clientv3.MemberAddResponse, error) {
	id := genId(urls[0])
	member := etcdserverpb.Member{
		ID:       id,
		PeerURLs: urls,
	}

	resp := clientv3.MemberAddResponse{Member: &member}
	f.additions = append(f.additions, id)
	f.members = append(f.members, &member)

	return &resp, nil
}

func (f *fakeEtcd) MemberList(ctx context.Context) (*clientv3.MemberListResponse, error) {
	return &clientv3.MemberListResponse{Members: f.members}, nil
}

func (f *fakeEtcd) MemberRemove(ctx context.Context, id uint64) (*clientv3.MemberRemoveResponse, error) {
	f.removals = append(f.removals, id)
	return &clientv3.MemberRemoveResponse{Members: f.members}, nil
}

func (*fakeEtcd) IsHttps() bool {
	return false
}

func (f *fakeEtcd) SetEndpoints(endpoints ...string) {
}

func (*fakeEtcd) Start(ctx context.Context, endpoints ...string) error {
	return nil
}

type fixture struct {
	t          *testing.T
	kubeclient *fake.Clientset
	etcdclient *fakeEtcd
}

func newFixture(t *testing.T, objects []runtime.Object, members []runtime.Object) *fixture {
	f := &fixture{
		t:          t,
		kubeclient: fake.NewSimpleClientset(objects...),
		etcdclient: &fakeEtcd{additions: []uint64{}, removals: []uint64{}, members: []*etcdserverpb.Member{}},
	}

	return f
}

func (f *fixture) newController(ctx context.Context, objects []runtime.Object, members []runtime.Object) *Controller {
	c := NewController(f.kubeclient, f.etcdclient, "default", "app=etcd,component=control-plane")

	for _, o := range objects {
		switch o := o.(type) {
		case *corev1.Pod:
			c.pods.Informer().GetIndexer().Add(o)
		}
	}

	for _, o := range members {
		switch o := o.(type) {
		case *corev1.Pod:
			url := c.podUrl(o)
			member := etcdserverpb.Member{Name: o.Name, ID: genId(url), PeerURLs: []string{url}}
			f.etcdclient.members = append(f.etcdclient.members, &member)
		}
	}

	c.factory.Start(ctx.Done())
	c.etcd.Start(ctx)

	return c
}

func TestSync(t *testing.T) {
	pods := make([]runtime.Object, 0, 4)
	for i := 0; i < 4; i++ {
		pods = append(pods, &corev1.Pod{
			ObjectMeta: v1.ObjectMeta{
				Name: fmt.Sprintf("foo-%d", i), Namespace: "default",
				Labels: map[string]string{"app": "etcd", "component": "control-plane"},
			},
			Status: corev1.PodStatus{PodIP: fmt.Sprintf("192.0.2.%d", i+1)},
		})
	}

	testCases := []struct {
		nMembers   int
		nPods      int
		nAdditions int
		nRemovals  int
	}{
		{nMembers: 3, nPods: 3, nAdditions: 0, nRemovals: 0},
		{nMembers: 3, nPods: 2, nAdditions: 0, nRemovals: 1},
		{nMembers: 2, nPods: 3, nAdditions: 1, nRemovals: 0},
		// Don't go below quorum
		{nMembers: 3, nPods: 1, nAdditions: 0, nRemovals: 0},
		// TODO: Add pods in learner mode, to make this safer
		{nMembers: 1, nPods: 3, nAdditions: 2, nRemovals: 0},
	}
	for _, testCase := range testCases {
		t.Run(fmt.Sprintf("%d pods, %d members", testCase.nPods, testCase.nMembers), func(t *testing.T) {
			f := newFixture(t, pods[0:testCase.nPods], pods[0:testCase.nMembers])
			_, ctx := ktesting.NewTestContext(t)

			c := f.newController(ctx, pods[0:testCase.nPods], pods[0:testCase.nMembers])
			err := c.reconcileEtcd(ctx)
			if err != nil {
				t.Fatal(err)
			}

			if len(f.etcdclient.additions) != testCase.nAdditions {
				t.Errorf("Got %d etcd additions, expected %d", len(f.etcdclient.additions), testCase.nAdditions)
			}
			if len(f.etcdclient.removals) != testCase.nRemovals {
				t.Errorf("Got %d etcd additions, expected %d", len(f.etcdclient.additions), testCase.nRemovals)
			}
		})
	}
}

func TestEndpoints(t *testing.T) {
	pods := make([]runtime.Object, 0, 4)
	for i := 0; i < 4; i++ {
		status := corev1.ConditionTrue
		if i == 3 {
			status = corev1.ConditionFalse
		}
		pods = append(pods, &corev1.Pod{
			ObjectMeta: v1.ObjectMeta{
				Name: fmt.Sprintf("foo-%d", i), Namespace: "default",
				Labels: map[string]string{"app": "etcd", "component": "control-plane"},
			},
			Status: corev1.PodStatus{
				PodIP:      fmt.Sprintf("192.0.2.%d", i+1),
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}},
			},
		})
	}

	f := newFixture(t, pods, pods)
	_, ctx := ktesting.NewTestContext(t)

	c := f.newController(ctx, pods, pods)
	endpoints, err := c.EtcdEndpoints()
	if err != nil {
		t.Fatal(err)
	}

	if len(endpoints) != 3 {
		t.Errorf("Got %d endpoints, expected %d", len(endpoints), 3)
	}

	for _, endpoint := range endpoints {
		parts := strings.SplitN(endpoint, ":", 2)
		if parts[0] != "http" {
			t.Errorf("Expected HTTP endpoint, got %s", endpoint)
		}
	}
}

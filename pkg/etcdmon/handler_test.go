package etcdmon

import (
	"context"
	"testing"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/klog/v2/ktesting"
)

type fakeEtcd struct {
}

func (*fakeEtcd) Close() error {
	return nil
}

func (*fakeEtcd) MemberAdd(ctx context.Context, urls []string) (*clientv3.MemberAddResponse, error) {
	return nil, nil
}

func (*fakeEtcd) MemberList(ctx context.Context) (*clientv3.MemberListResponse, error) {
	return &clientv3.MemberListResponse{Members: []*etcdserverpb.Member{}}, nil
}

func (*fakeEtcd) MemberRemove(ctx context.Context, id uint64) (*clientv3.MemberRemoveResponse, error) {
	return nil, nil
}

func (*fakeEtcd) SetEndpoints(endpoints ...string) {
}

func (*fakeEtcd) Start(ctx context.Context, endpoints ...string) error {
	return nil
}

type fixture struct {
	t          *testing.T
	kubeclient *fake.Clientset
	etcdclient *fakeEtcd
}

func newFixture(t *testing.T, objects []runtime.Object) *fixture {
	return &fixture{
		t:          t,
		kubeclient: fake.NewSimpleClientset(objects...),
	}
}

func (f *fixture) newController(ctx context.Context, objects []runtime.Object) *Controller {
	c := NewController(f.kubeclient, f.etcdclient, "default", "app=etcd,component=control-plane")
	for _, o := range objects {
		switch o := o.(type) {
		case *corev1.Pod:
			c.pods.Informer().GetIndexer().Add(o)
		}
	}
	return c
}

func TestSync(t *testing.T) {
	f := newFixture(t, []runtime.Object{})
	_, ctx := ktesting.NewTestContext(t)

	c := f.newController(ctx, []runtime.Object{})
	err := c.reconcileEtcd(ctx)

	if err != nil {
		t.Fatal(err)
	}
}

package etcdmon

import (
	"context"
	"testing"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	return &clientv3.MemberAddResponse{Member: &etcdserverpb.Member{}}, nil
}

func (*fakeEtcd) MemberList(ctx context.Context) (*clientv3.MemberListResponse, error) {
	return &clientv3.MemberListResponse{Members: []*etcdserverpb.Member{}}, nil
}

func (*fakeEtcd) MemberRemove(ctx context.Context, id uint64) (*clientv3.MemberRemoveResponse, error) {
	return &clientv3.MemberRemoveResponse{Members: []*etcdserverpb.Member{}}, nil
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

func (f *fixture) newController(ctx context.Context, objects []runtime.Object, members []runtime.Object) *Controller {
	c := NewController(f.kubeclient, f.etcdclient, "default", "app=etcd,component=control-plane")

	for _, o := range objects {
		switch o := o.(type) {
		case *corev1.Pod:
			c.pods.Informer().GetIndexer().Add(o)
		}
	}

	c.factory.Start(ctx.Done())
	c.etcd.Start(ctx)

	return c
}

func TestSync(t *testing.T) {
	pods := []runtime.Object{
		&corev1.Pod{
			ObjectMeta: v1.ObjectMeta{
				Name: "foo", Namespace: "default",
				Labels: map[string]string{"app": "etcd", "component": "control-plane"},
			},
			Status: corev1.PodStatus{PodIP: "192.0.2.1"},
		},
	}

	f := newFixture(t, pods)
	_, ctx := ktesting.NewTestContext(t)

	c := f.newController(ctx, pods, pods)
	err := c.reconcileEtcd(ctx)

	if err != nil {
		t.Fatal(err)
	}
}

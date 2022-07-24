package main

import (
	"context"
	"sync"
	"time"

	flag "github.com/spf13/pflag"
	"k8s.io/klog/v2"
)

func init() {
	klog.InitFlags(nil)
}

func main() {
	flag.Parse()

	k8sClient, err := NewK8s()
	nodes := make(map[string]struct{})
	nodeQuery, err := k8sClient.ListNodes()
	if err != nil {
		panic(err.Error())
	}
	klog.Infof("There are %d control-plane nodes in the cluster\n", len(nodeQuery.Items))
	for _, node := range nodeQuery.Items {
		nodes[node.ObjectMeta.Name] = struct{}{}
	}

	wg := new(sync.WaitGroup)
	wg.Add(1)
	go func() {
		defer wg.Done()
		k8sClient.WatchNodes()
	}()

	endpoints, err := k8sClient.GetEtcdEndpoints()
	if err != nil {
		panic(err.Error())
	}

	etcdClient, err := NewEtcd(endpoints)
	if err != nil {
		panic(err.Error())
	}
	defer etcdClient.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := etcdClient.Sync(ctx); err != nil {
		panic(err.Error())
	}
	cancel()

	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	members, err := etcdClient.MemberList(ctx)
	if err != nil {
		panic(err.Error())
	}
	cancel()
	klog.Infof("There are %d members in the etcd cluster\n", len(members.Members))

	orphanMembers := make(map[string]uint64)
	for _, member := range members.Members {
		if _, ok := nodes[member.Name]; ok {
			klog.Infof("Found node for etcd member %s\n", member.Name)
			delete(nodes, member.Name)
		} else {
			orphanMembers[member.Name] = member.ID
		}
	}

	for k := range nodes {
		klog.Warningf("Did not find etcd member for node %s\n", k)
	}
	for k, id := range orphanMembers {
		klog.Warningf("Did not find node for etcd member %s (%d)\n", k, id)
	}
	if len(orphanMembers) > len(members.Members)/2 {
		klog.Errorf("%d out of %d members are missing nodes, which is more than quorum\n", len(orphanMembers), len(members.Members))
	}
	for k, id := range orphanMembers {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		klog.Infof("Removing orphan etcd member %s (%d)\n", k, id)
		_, err := etcdClient.MemberRemove(ctx, id)
		if err != nil {
			panic(err.Error())
		}
	}

	wg.Wait()
}

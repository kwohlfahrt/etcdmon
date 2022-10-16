package main

import (
	"context"
	"time"

	flag "github.com/spf13/pflag"
	"k8s.io/klog/v2"
)

func init() {
	klog.InitFlags(nil)
}

// TODO: Use k8s Informers to manage the list & watch logic
func reconcile(k8s *K8sClient, etcd *EtcdClient) error {
	nodes := make(map[string]struct{})
	nodeQuery, err := k8s.ListNodes()
	if err != nil {
		return err
	}

	klog.Infof("There are %d control-plane nodes in the cluster\n", len(nodeQuery.Items))
	for _, node := range nodeQuery.Items {
		nodes[node.ObjectMeta.Name] = struct{}{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	members, err := etcd.MemberList(ctx)
	if err != nil {
		return err
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
		return nil
	}
	for k, id := range orphanMembers {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		klog.Infof("Removing orphan etcd member %s (%d)\n", k, id)
		_, err := etcd.MemberRemove(ctx, id)
		if err != nil {
			return err
		}
	}

	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return etcd.Sync(ctx)
}

func main() {
	flag.Parse()

	k8sClient, err := NewK8s()
	if err != nil {
		panic(err.Error())
	}
	endpoints, err := k8sClient.GetEtcdEndpoints()
	if err != nil {
		panic(err.Error())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	// TODO: Use own certs, don't piggyback off etcd's
	etcdCerts := CertPaths{
		caCert:     "/etc/kubernetes/pki/etcd/ca.crt",
		clientCert: "/etc/kubernetes/pki/etcd/server.crt",
		clientKey:  "/etc/kubernetes/pki/etcd/server.key",
	}
	etcdClient, err := NewEtcd(endpoints, etcdCerts, ctx)
	if err != nil {
		panic(err.Error())
	}
	defer etcdClient.Close()
	etcdClient.Sync(ctx)
	cancel()

	ch := make(chan struct{})
	go k8sClient.WatchNodes(ch)
	for range ch {
		if err := reconcile(k8sClient, etcdClient); err != nil {
			panic(err.Error())
		}
	}
}

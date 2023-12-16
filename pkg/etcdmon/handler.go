package etcdmon

import (
	"context"
	"fmt"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

func (c *Controller) syncEtcd(etcd *EtcdClient, baseCtx context.Context) error {
	ctx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
	defer cancel()
	return etcd.Sync(ctx)
}

func (c *Controller) reconcileEtcd(etcd *EtcdClient, baseCtx context.Context) error {
	nodes := make(map[string]struct{})
	for _, node := range c.informer.GetIndexer().List() {
		nodes[node.(*v1.Node).ObjectMeta.Name] = struct{}{}
	}
	klog.Infof("There are %d control-plane nodes in the cluster\n", len(nodes))

	ctx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
	members, err := etcd.MemberList(ctx)
	cancel()
	if err != nil {
		return err
	}
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

	for nodeName := range nodes {
		// TODO: Check if we would exceed quorum by adding too many nodes
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		klog.V(2).Infof("Adding etcd member for new node %s\n", nodeName)
		member, err := etcd.MemberAdd(ctx, nodeName)
		if err != nil {
			return err
		}
		klog.Infof("Added etcd member for new node %s (%x)\n", nodeName, member.Member.ID)
	}
	if len(orphanMembers) > len(members.Members)/2 {
		klog.Errorf("%d out of %d members are missing nodes, which is more than quorum\n", len(orphanMembers), len(members.Members))
		return nil
	}
	for k, id := range orphanMembers {
		klog.Infof("Removing orphan etcd member %s (%x)\n", k, id)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := etcd.MemberRemove(ctx, id)
		cancel()
		if err != nil {
			return err
		}
	}

	return c.syncEtcd(etcd, baseCtx)
}

func (c *Controller) EtcdEndpoints() []string {
	nodes := c.informer.GetIndexer().List()

	endpoints := []string{}
	for _, node := range nodes {
		endpoint := fmt.Sprintf("https://%s:2379", node.(*v1.Node).ObjectMeta.Name)
		endpoints = append(endpoints, endpoint)
	}
	klog.Infof("Found etcd endpoints %s from node names\n", strings.Join(endpoints, ","))
	return endpoints
}

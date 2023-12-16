package etcdmon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
)

func (c *Controller) reconcileEtcd(baseCtx context.Context) error {
	pods := make(map[string]struct{})
	podList, err := c.pods.Lister().List(labels.Everything())
	if err != nil {
		return err
	}

	for _, pod := range podList {
		pods[pod.Name] = struct{}{}
	}
	klog.Infof("There are %d pods in the cluster\n", len(pods))

	ctx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
	members, err := c.etcd.MemberList(ctx)
	cancel()
	if err != nil {
		return err
	}
	klog.Infof("There are %d members in the etcd cluster\n", len(members.Members))

	orphanMembers := make(map[string]uint64)
	for _, member := range members.Members {
		if _, ok := pods[member.Name]; ok {
			klog.Infof("Found node for etcd member %s\n", member.Name)
			delete(pods, member.Name)
		} else {
			orphanMembers[member.Name] = member.ID
		}
	}

	for nodeName := range pods {
		// TODO: Check if we would exceed quorum by adding too many nodes
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		klog.V(2).Infof("Adding etcd member for new node %s\n", nodeName)
		member, err := c.etcd.MemberAdd(ctx, []string{nodeName})
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
		_, err := c.etcd.MemberRemove(ctx, id)
		cancel()
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *Controller) EtcdEndpoints() ([]string, error) {
	pods, err := c.pods.Lister().List(labels.Everything())
	if err != nil {
		return nil, err
	}

	endpoints := []string{}
	for _, pod := range pods {
		endpoint := fmt.Sprintf("https://%s:2379", pod.Name)
		endpoints = append(endpoints, endpoint)
	}
	klog.Infof("Found etcd endpoints %s from node names\n", strings.Join(endpoints, ","))
	return endpoints, nil
}

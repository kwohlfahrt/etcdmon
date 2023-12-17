package etcdmon

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
)

func (c *Controller) reconcileEtcd(baseCtx context.Context) error {
	pods := make(map[string]*corev1.Pod)
	podList, err := c.pods.Lister().List(labels.Everything())
	if err != nil {
		return err
	}

	for _, pod := range podList {
		pods[pod.Name] = pod
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
			klog.Infof("Found pod for etcd member %s\n", member.Name)
			delete(pods, member.Name)
		} else {
			orphanMembers[member.Name] = member.ID
		}
	}

	endpoints, err := c.EtcdEndpoints()
	if err != nil {
		return err
	}
	c.etcd.SetEndpoints(endpoints...)

	for podName, pod := range pods {
		// TODO: Check if we would exceed quorum by adding too many nodes
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		klog.V(2).Infof("Adding etcd member for new pod %s\n", podName)
		member, err := c.etcd.MemberAdd(ctx, []string{c.podUrl(pod)})
		if err != nil {
			return err
		}
		klog.Infof("Added etcd member for new node %s (%x)\n", podName, member.Member.ID)
	}
	if len(orphanMembers) > len(members.Members)/2 {
		klog.Errorf("%d out of %d members are missing pods, which is more than quorum\n", len(orphanMembers), len(members.Members))
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

	endpoints := make([]string, 0, len(pods))

	for _, pod := range pods {
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				endpoints = append(endpoints, c.podUrl(pod))
				break
			}
		}
	}
	klog.Infof("Found etcd endpoints %s from pod names\n", strings.Join(endpoints, ","))
	return endpoints, nil
}

func (c *Controller) podUrl(pod *corev1.Pod) string {
	scheme := "https"
	if !c.etcd.IsHttps() {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s:2379", scheme, pod.Status.PodIP)
}

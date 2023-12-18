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

func (c *Controller) updateState(pods map[string](*corev1.Pod)) {
	c.state.pods = make(map[string]struct{}, len(pods))
	for k := range pods {
		c.state.pods[k] = struct{}{}
	}

	c.state.time = time.Now()
}

func (c *Controller) checkState(pods map[string](*corev1.Pod)) error {
	unseen := make(map[string]struct{}, len(c.state.pods))
	for k := range c.state.pods {
		unseen[k] = struct{}{}
	}

	for k := range pods {
		if _, ok := unseen[k]; !ok {
			c.updateState(pods)
			return ErrNotStabilized
		}
		delete(unseen, k)
	}
	if len(unseen) > 0 {
		c.updateState(pods)
		return ErrNotStabilized
	}
	return nil
}

func (c *Controller) reconcileEtcd(baseCtx context.Context) error {
	podList, err := c.pods.Lister().List(labels.Everything())
	if err != nil {
		return err
	}

	pods := make(map[string]*corev1.Pod, len(podList))
	for _, pod := range podList {
		// TODO: On k8s 1.29, check for `PodReadyToStartContainers` instead
		if pod.Status.PodIP != "" {
			klog.V(2).Infof("Adding pod %s", pod.Name)
			pods[pod.Name] = pod
		} else {
			klog.V(2).Infof("Skipping pod %s because it has no IP", pod.Name)
		}
	}
	klog.Infof("There are %d pods in the cluster\n", len(pods))

	if err := c.checkState(pods); err != nil {
		klog.Infof("Pods have changed since previous observation. Requeueing for timeout.")
		return ErrNotStabilized
	}
	if time.Now().Before(c.state.time.Add(c.timeout)) {
		klog.Infof("Timeout not yet reached, skipping update.")
		return nil
	}

	endpoints, err := c.EtcdEndpoints()
	if err != nil {
		return err
	}
	c.etcd.SetEndpoints(endpoints...)

	ctx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
	members, err := c.etcd.MemberList(ctx)
	cancel()
	if err != nil {
		return err
	}
	klog.Infof("There are %d members in the etcd cluster\n", len(members.Members))

	orphanMembers := make(map[string]uint64)
	pendingMembers := make(map[string]struct{})
	for _, member := range members.Members {
		if member.Name == "" {
			urls := strings.Join(member.PeerURLs, ",")
			klog.Infof("Found pending etcd member %s\n", urls)
			pendingMembers[urls] = struct{}{}
		} else if _, ok := pods[member.Name]; ok {
			klog.Infof("Found pod for etcd member %s\n", member.Name)
			delete(pods, member.Name)
		} else {
			orphanMembers[member.Name] = member.ID
		}
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

	for podName, pod := range pods {
		url := c.podUrl(pod, true)
		if _, ok := pendingMembers[url]; ok {
			klog.Infof("Not adding new etcd member for pod %s, already pending\n", podName)
			continue
		}

		// TODO: Check if we would exceed quorum by adding too many nodes
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		klog.V(3).Infof("Adding etcd member for new pod %s\n", podName)

		member, err := c.etcd.MemberAdd(ctx, []string{url})
		if err != nil {
			return err
		}
		klog.Infof("Added etcd member for new pod %s (%x)\n", podName, member.Member.ID)
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
				endpoints = append(endpoints, c.podUrl(pod, false))
				break
			}
		}
	}
	klog.Infof("Found etcd endpoints %s from pod names\n", strings.Join(endpoints, ","))
	return endpoints, nil
}

func (c *Controller) podUrl(pod *corev1.Pod, peer bool) string {
	scheme := "https"
	if !c.etcd.IsHttps() {
		scheme = "http"
	}
	port := 2379
	if peer {
		port = 2380
	}
	return fmt.Sprintf("%s://%s:%d", scheme, pod.Status.PodIP, port)
}

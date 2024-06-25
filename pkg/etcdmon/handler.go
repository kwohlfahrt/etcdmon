package etcdmon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	etcdserver "go.etcd.io/etcd/server/v3/etcdserver"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
)

func (c *Controller) reconcileEtcd(baseCtx context.Context) error {
	pods, err := c.pods.Lister().List(labels.Everything())
	if err != nil {
		return err
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

	if err = c.state.reconcile(pods, members.Members); err != nil {
		return err
	}

	if err = c.state.checkQuorum(); err != nil {
		klog.Error(err)
		return nil
	}

	ctx, cancel = context.WithTimeout(baseCtx, 5*time.Second)
	defer cancel()
	var syncErrors []error
	for podIP := range c.state.NewPods {
		_, err := c.etcd.MemberAdd(ctx, []string{c.podUrl(podIP, true)})
		if err != nil {
			syncErrors = append(syncErrors, err)
		}
	}

	for memberId, state := range c.state.Etcd {
		switch state.State {
		case New:
			klog.Infof("Waiting for member 0x%x to register", memberId)
		case Learner:
			klog.Infof("Trying to promote learner 0x%x", memberId)
			_, err := c.etcd.MemberPromote(ctx, memberId)
			if errors.Is(err, etcdserver.ErrLearnerNotReady) {
				syncErrors = append(syncErrors, ErrNotStabilized)
			} else if err != nil {
				klog.Error(err)
				syncErrors = append(syncErrors, err)
			}
		case Gone:
			klog.Infof("Removing stale learner 0x%x", memberId)
			_, err := c.etcd.MemberRemove(ctx, memberId)
			if err != nil {
				klog.Error(err)
				syncErrors = append(syncErrors, err)
			}
		case Healthy:
			klog.V(2).Infof("Skipping healthy member 0x%x", memberId)
		case Dead:
			if time.Now().Before(state.Transition.Add(c.timeout)) {
				klog.Infof("Not removing dead member 0x%x, dead for less than timeout %fs", memberId, c.timeout.Seconds())
				syncErrors = append(syncErrors, ErrNotStabilized)
			} else {
				klog.Infof("Removing dead member 0x%x", memberId)
				_, err := c.etcd.MemberRemove(ctx, memberId)
				if err != nil {
					klog.Error(err)
					syncErrors = append(syncErrors, err)
				}
			}
		}
	}

	return errors.Join(syncErrors...)
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
				endpoints = append(endpoints, c.podUrl(pod.Status.PodIP, false))
				break
			}
		}
	}
	klog.Infof("Found etcd endpoints %s from pod names\n", strings.Join(endpoints, ","))
	return endpoints, nil
}

func (c *Controller) podUrl(podIP string, peer bool) string {
	scheme := "https"
	if !c.etcd.IsHttps() {
		scheme = "http"
	}
	port := 2379
	if peer {
		port = 2380
	}
	return fmt.Sprintf("%s://%s:%d", scheme, podIP, port)
}

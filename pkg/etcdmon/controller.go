package etcdmon

// This package container the runtime & queueing boilerplate. The business logic
// lives in `handler.go`, and the etcd interface in `etcd.go`

import (
	"context"
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
)

type Controller struct {
	queue     workqueue.RateLimitingInterface
	informer  cache.SharedIndexInformer
	etcdCerts CertPaths
}

func NewController(client *kubernetes.Clientset, namespace string, selector string, certs CertPaths) *Controller {
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	podWatcher := cache.NewFilteredListWatchFromClient(
		client.CoreV1().RESTClient(), "pods", namespace,
		func(options *metav1.ListOptions) { options.LabelSelector = selector },
	)
	informer := cache.NewSharedIndexInformer(podWatcher, &v1.Pod{}, 0, cache.Indexers{})
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(obj)
			if err == nil {
				klog.Infof("Adding pod %s", key)
				queue.Add(key)
			}
		},
		DeleteFunc: func(obj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(obj)
			if err == nil {
				klog.Infof("Deleting pod %s", key)
				queue.Add(key)
			}
		},
	})

	return &Controller{informer: informer, queue: queue, etcdCerts: certs}
}

func (c *Controller) Run(workers int, ctx context.Context) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.Info("starting etcd monitor controller")
	go c.informer.Run(ctx.Done())

	if !cache.WaitForNamedCacheSync("etcd-monitor", ctx.Done(), c.informer.HasSynced) {
		runtime.HandleError(fmt.Errorf("timed out waiting for cache sync"))
		return
	}

	etcdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	etcd, err := NewEtcd(c.EtcdEndpoints(), c.etcdCerts, etcdCtx)
	cancel()
	if err != nil {
		runtime.HandleError(err)
		return
	}
	defer etcd.Close()
	// TODO: Don't unnecessarily sync the initial pods

	c.reconcileEtcd(etcd, ctx)
	for i := 0; i < workers; i++ {
		go wait.Until(func() {
			key, quit := c.queue.Get()
			if quit {
				return
			}
			defer c.queue.Done(key)
			err := c.processItem(key.(string), etcd, ctx)
			c.handleErr(err, key)
		}, 0, ctx.Done())
	}

	<-ctx.Done()
	klog.Info("stopping etcd monitor controller")
}

func (c *Controller) processItem(key string, etcd *EtcdClient, baseCtx context.Context) error {
	_, _, err := c.informer.GetIndexer().GetByKey(key)
	if err != nil {
		klog.Errorf("Failed to fetch object with key %s from store: %v", key, err)
		return err
	}

	return c.reconcileEtcd(etcd, baseCtx)
}

func (c *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		c.queue.Forget(key)
		return
	}

	if c.queue.NumRequeues(key) < 5 {
		klog.Warningf("Error syncing node %v: %v", key, err)
		c.queue.AddRateLimited(key)
		return
	}

	c.queue.Forget(key)
	runtime.HandleError(err)
	klog.Errorf("Giving up reconciling node %v: %v", key, err)
}

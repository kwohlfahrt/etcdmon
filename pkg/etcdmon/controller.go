package etcdmon

// This package container the runtime & queueing boilerplate. The business logic
// lives in `handler.go`, and the etcd interface in `etcd.go`

import (
	"context"
	"fmt"
	"time"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1informers "k8s.io/client-go/informers/core/v1"
)

type Controller struct {
	queue   workqueue.RateLimitingInterface
	pods    corev1informers.PodInformer
	factory informers.SharedInformerFactory

	etcd EtcdClient
}

func NewController(client kubernetes.Interface, etcd EtcdClient, namespace string, selector string) *Controller {
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	factory := informers.NewSharedInformerFactoryWithOptions(
		client,
		0*time.Second,
		informers.WithNamespace(namespace),
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) { opts.LabelSelector = selector }),
	)
	pods := factory.Core().V1().Pods()
	pods.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
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

	return &Controller{pods: pods, factory: factory, queue: queue, etcd: etcd}
}

func (c *Controller) Run(ctx context.Context, etcdCerts CertPaths, workers int) error {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.Info("starting etcd monitor controller")
	c.factory.Start(ctx.Done())

	for v, ok := range c.factory.WaitForCacheSync(ctx.Done()) {
		if !ok {
			return fmt.Errorf("cache failed to sync: %v", v)
		} else {
			klog.V(2).Infof("synced cache: %v", v)
		}
	}

	endpoints, err := c.EtcdEndpoints()
	if err != nil {
		return err
	}
	if err := c.etcd.Start(ctx, endpoints...); err != nil {
		return err
	}
	defer c.etcd.Close()

	for i := 0; i < workers; i++ {
		go wait.Until(func() {
			key, quit := c.queue.Get()
			if quit {
				return
			}
			defer c.queue.Done(key)
			err := c.processItem(ctx, key.(string))
			c.handleErr(err, key)
		}, 0, ctx.Done())
	}

	<-ctx.Done()
	klog.Info("stopping etcd monitor controller")
	return nil
}

func (c *Controller) processItem(ctx context.Context, key string) error {
	_, _, err := c.pods.Informer().GetIndexer().GetByKey(key)
	if err != nil {
		klog.Errorf("Failed to fetch object with key %s from store: %v", key, err)
		return err
	}

	return c.reconcileEtcd(ctx)
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
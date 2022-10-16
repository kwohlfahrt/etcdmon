package main

import (
	"context"
	"fmt"
	"strings"
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
	informer    cache.SharedIndexInformer
	queue       workqueue.RateLimitingInterface
	podInformer cache.SharedIndexInformer
}

// NewController creates a new Controller.
func NewController(client *kubernetes.Clientset) *Controller {
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	watcher := cache.NewFilteredListWatchFromClient(client.CoreV1().RESTClient(), "nodes", "", func(options *metav1.ListOptions) {
		options.LabelSelector = "node-role.kubernetes.io/control-plane="
	})
	informer := cache.NewSharedIndexInformer(watcher, &v1.Node{}, 0, cache.Indexers{})
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err == nil {
				klog.Infof("Deleting node %s", key)
				queue.Add(key)
			}
		},
	})

	podWatcher := cache.NewFilteredListWatchFromClient(client.CoreV1().RESTClient(), "pods", "kube-system", func(options *metav1.ListOptions) {
		options.LabelSelector = "component=etcd"
	})
	podInformer := cache.NewSharedIndexInformer(podWatcher, &v1.Pod{}, 0, cache.Indexers{})
	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(obj)
			if err == nil {
				klog.Infof("Adding pod %s", key)
				queue.Add(key)
			}
		},
	})

	return &Controller{
		informer:    informer,
		podInformer: podInformer,
		queue:       queue,
	}
}

func (c *Controller) Run(workers int, ctx context.Context) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.Info("starting etcd monitor controller")
	go c.informer.Run(ctx.Done())
	go c.podInformer.Run(ctx.Done())

	if !cache.WaitForNamedCacheSync("etcd-monitor", ctx.Done(), c.informer.HasSynced, c.podInformer.HasSynced) {
		runtime.HandleError(fmt.Errorf("timed out waiting for cache sync"))
		return
	}

	etcdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	// TODO: Use own certs, don't piggyback off etcd's
	etcdCerts := CertPaths{
		caCert:     "/etc/kubernetes/pki/etcd/ca.crt",
		clientCert: "/etc/kubernetes/pki/etcd/server.crt",
		clientKey:  "/etc/kubernetes/pki/etcd/server.key",
	}
	etcd, err := NewEtcd(c.EtcdEndpoints(), etcdCerts, etcdCtx)
	cancel()
	if err != nil {
		runtime.HandleError(err)
		return
	}
	defer etcd.Close()

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
	// TODO: Implement a better way of figuring out what informer the key belongs to
	_, exists, err := c.podInformer.GetIndexer().GetByKey(key)
	if err != nil {
		klog.Errorf("Failed to fetch object with key %s from store: %v", key, err)
		return err
	}

	if exists {
		// Process new pod
		return c.syncEtcd(etcd, baseCtx)
	}

	// Otherwise, was a deleted node (or pod which went stale)
	obj, exists, err := c.informer.GetIndexer().GetByKey(key)
	if err != nil {
		klog.Errorf("Failed to fetch object with key %s from store: %v", key, err)
		return err
	}

	klog.Infof("Processing node: %s, present? %t %v", key, exists, obj)
	return c.reconcileEtcd(etcd, baseCtx)
}

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

	for k := range nodes {
		klog.Warningf("Did not find etcd member for node %s\n", k)
	}
	if len(orphanMembers) > len(members.Members)/2 {
		klog.Errorf("%d out of %d members are missing nodes, which is more than quorum\n", len(orphanMembers), len(members.Members))
		return nil
	}
	for k, id := range orphanMembers {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		klog.Infof("Removing orphan etcd member %s (%x)\n", k, id)
		_, err := etcd.MemberRemove(ctx, id)
		if err != nil {
			return err
		}
	}

	return c.syncEtcd(etcd, baseCtx)
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

func (c *Controller) EtcdEndpoints() []string {
	pods := c.podInformer.GetIndexer().List()

	endpoints := []string{}
	for _, pod := range pods {
		endpoint, ok := pod.(*v1.Pod).ObjectMeta.Annotations["kubeadm.kubernetes.io/etcd.advertise-client-urls"]
		if !ok {
			continue
		}
		endpoints = append(endpoints, endpoint)
	}
	klog.Infof("Found etcd endpoints %s from pod annotations\n", strings.Join(endpoints, ","))
	return endpoints
}

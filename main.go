package main

import (
	"context"
	"fmt"
	"time"

	flag "github.com/spf13/pflag"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
)

func init() {
	klog.InitFlags(nil)
}

type Controller struct {
	indexer  cache.Indexer
	queue    workqueue.RateLimitingInterface
	informer cache.Controller
}

// NewController creates a new Controller.
func NewController(client *kubernetes.Clientset) *Controller {
	watcher := cache.NewListWatchFromClient(client.CoreV1().RESTClient(), "nodes", "", fields.Everything())
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	indexer, informer := cache.NewIndexerInformer(watcher, &v1.Node{}, 0, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(key)
			}
		},
		UpdateFunc: func(old interface{}, new interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(new)
			if err == nil {
				queue.Add(key)
			}
		},
		DeleteFunc: func(obj interface{}) {
			// IndexerInformer uses a delta queue, therefore for deletes we have to use this
			// key function.
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(key)
			}
		},
	}, cache.Indexers{})

	return &Controller{
		informer: informer,
		indexer:  indexer,
		queue:    queue,
	}
}

func (c *Controller) Run(workers int, stop chan struct{}) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.Info("starting etcd monitor controller")
	go c.informer.Run(stop)

	if !cache.WaitForCacheSync(stop, c.informer.HasSynced) {
		runtime.HandleError(fmt.Errorf("timed out waiting for cache sync"))
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(func() {
			for c.processNextItem() {
			}
		}, time.Second, stop)
	}

	<-stop
	klog.Info("stopping etcd monitor controller")
}

func (c *Controller) processNextItem() bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	err := c.removeEtcd(key.(string))
	c.handleErr(err, key)
	return true
}

func (c *Controller) removeEtcd(key string) error {
	obj, exists, err := c.indexer.GetByKey(key)
	if err != nil {
		klog.Errorf("Failed to fetch object with key %s from store: %v", key, err)
		return err
	}

	if !exists {
		fmt.Printf("Node does not exist anymore: %s\n", key)
	} else {
		fmt.Printf("Update for node: %s\n", obj.(*v1.Node).GetName())
	}

	return nil
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

/*
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
	if err := etcd.Sync(ctx); err != nil {
		cancel()
		return err
	}
	cancel()

	return nil
}
*/

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

	controller := NewController(k8sClient.clientset)

	stop := make(chan struct{})
	defer close(stop)
	go controller.Run(1, stop)

	select {}
}

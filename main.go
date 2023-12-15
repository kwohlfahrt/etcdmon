package main

import (
	"context"

	flag "github.com/spf13/pflag"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

var namespace = flag.String("namespace", "kube-system", "The namespace to watch for pods in (default: kube-system)")
var selector = flag.String("selector", "tier=control-plane,component=etcd", "The label selector for etcd pods")

func init() {
	klog.InitFlags(nil)
}

func main() {
	flag.Parse()

	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	controller := NewController(clientset, *namespace, *selector)
	controller.Run(1, context.Background())
}

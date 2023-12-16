package main

import (
	"context"

	"github.com/kwohlfahrt/etcdmon/pkg/etcdmon"

	flag "github.com/spf13/pflag"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

var namespace = flag.String("namespace", "kube-system", "The namespace to watch for pods in (default: kube-system)")
var selector = flag.String("selector", "tier=control-plane,component=etcd", "The label selector for etcd pods")
var caCert = flag.String("ca-cert", "", "Path to the etcd CA certificate")
var clientCert = flag.String("client-cert", "", "Path to the etcd client certificate")
var clientKey = flag.String("client-key", "", "Path to the etcd client key")

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

	certs := etcdmon.CertPaths{
		CaCert:     *caCert,
		ClientCert: *clientCert,
		ClientKey:  *clientKey,
	}

	controller := etcdmon.NewController(clientset, *namespace, *selector, certs)
	controller.Run(1, context.Background())
}

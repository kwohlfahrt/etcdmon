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
var caCert = flag.String("ca-cert", "/etc/kubernetes/pki/etcd/ca.crt", "Path to the etcd CA certificate")
var clientCert = flag.String("client-cert", "/etc/kubernetes/pki/etcd/server.crt", "Path to the etcd client certificate")
var clientKey = flag.String("client-key", "/etc/kubernetes/pki/etcd/server.key", "Path to the etcd client key")

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

	certs := CertPaths{
		caCert:     *caCert,
		clientCert: *clientCert,
		clientKey:  *clientKey,
	}

	controller := NewController(clientset, *namespace, *selector, certs)
	controller.Run(1, context.Background())
}

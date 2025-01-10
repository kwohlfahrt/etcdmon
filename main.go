package main

import (
	"context"
	"flag"
	"time"

	"github.com/kwohlfahrt/etcdmon/pkg/etcdmon"

	"github.com/spf13/pflag"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

var namespace = pflag.String("namespace", "kube-system", "The namespace to watch for pods in (default: kube-system)")
var selector = pflag.String("selector", "tier=control-plane,component=etcd", "The label selector for etcd pods")
var caCert = pflag.String("ca-cert", "", "Path to the etcd CA certificate")
var clientCert = pflag.String("client-cert", "", "Path to the etcd client certificate")
var clientKey = pflag.String("client-key", "", "Path to the etcd client key")
var timeout = pflag.Duration("timeout", time.Minute*1, "Amount of time to wait for pod count to stabilize before changing etcd members")

func init() {
	klog.InitFlags(nil)
	pflag.CommandLine.AddGoFlag(flag.CommandLine.Lookup("v"))
}

func main() {
	pflag.Parse()

	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	certs, err := etcdmon.LoadCerts(context.Background(), etcdmon.CertPaths{
		CaCert:     *caCert,
		ClientCert: *clientCert,
		ClientKey:  *clientKey,
	})
	if err != nil {
		panic(err.Error())
	}

	etcd := etcdmon.NewEtcd(certs)
	controller := etcdmon.NewController(clientset, etcd, *namespace, *selector, *timeout)
	controller.Run(context.Background(), 1)
}

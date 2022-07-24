package main

import (
	"context"
	"strings"
	"time"
	"crypto/tls"
	"crypto/x509"
	"os"

	flag "github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	etcd "go.etcd.io/etcd/client/v3"
)

var ip = flag.IntP("flagname", "f", 1234, "help message")

func init() {
	klog.InitFlags(nil)
}

func main() {
	flag.Parse()

	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	// create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	nodes := make(map[string]struct{})
	nodeQuery, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{LabelSelector: "node-role.kubernetes.io/control-plane="})
	if err != nil {
		panic(err.Error())
	}
	klog.Infof("There are %d control-plane nodes in the cluster\n", len(nodeQuery.Items))
	for _, node := range nodeQuery.Items {
		nodes[node.ObjectMeta.Name] = struct{}{}
	}

	// This is how kubeadm bootstraps its etcd endpoints
	pods, err := clientset.CoreV1().Pods(metav1.NamespaceSystem).List(context.TODO(), metav1.ListOptions{LabelSelector: "component=etcd,tier=control-plane"})
	if err != nil {
		panic(err.Error())
	}
	endpoints := []string{}
	for _, pod := range pods.Items {
		endpoint, ok := pod.ObjectMeta.Annotations["kubeadm.kubernetes.io/etcd.advertise-client-urls"]
		if !ok {
			continue
		}
		endpoints = append(endpoints, endpoint)
	}
	klog.Infof("Found etcd endpoints %s from pod annotations\n", strings.Join(endpoints, ","))

	etcdCa := x509.NewCertPool()
	certPath := "/etc/kubernetes/pki/etcd/ca.crt"
	etcdCaCert, err := os.ReadFile(certPath)
	if err != nil {
		panic(err.Error())
	}
	if ok := etcdCa.AppendCertsFromPEM(etcdCaCert); !ok {
		klog.Fatalf("Unable to parse cert from %s", certPath)
	}

	// TODO: Use service keys, don't piggyback off the server cert
	etcdClientCert, err := tls.LoadX509KeyPair("/etc/kubernetes/pki/etcd/server.crt", "/etc/kubernetes/pki/etcd/server.key")
	if err != nil {
		panic(err.Error())
	}
	etcdTls := tls.Config{
		Certificates: []tls.Certificate{etcdClientCert},
		RootCAs: etcdCa,
	}
	etcdClient, err := etcd.New(etcd.Config{
		Endpoints: endpoints,
		DialTimeout: 2 * time.Second,
		TLS: &etcdTls,
	})
	if err != nil {
		panic(err.Error())
	}
	defer etcdClient.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5 * time.Second)
	if err := etcdClient.Sync(ctx); err != nil {
		panic(err.Error())
	}
	cancel()
	klog.Infof("Found etcd endpoints %s from etcd cluster\n", strings.Join(etcdClient.Endpoints(), ","))

	cluster := etcd.NewCluster(etcdClient)
	ctx, cancel = context.WithTimeout(context.Background(), 5 * time.Second)
	membersResponse, err := cluster.MemberList(ctx)
	cancel()
	if err != nil {
		panic(err.Error())
	}
	klog.Infof("There are %d members in the etcd cluster\n", len(membersResponse.Members))

	members := make(map[string]uint64)
	for _, member := range membersResponse.Members {
		if _, ok := nodes[member.Name]; ok {
			klog.Infof("Found node for etcd member %s\n", member.Name)
			delete(nodes, member.Name)
		} else {
			members[member.Name] = member.ID
		}
	}

	for k := range nodes {
		klog.Warningf("Did not find etcd member for node %s\n", k)
	}
	for k := range members {
		klog.Warningf("Did not find node for etcd member %s\n", k)
	}
}

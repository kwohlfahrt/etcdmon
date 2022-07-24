package main

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"k8s.io/klog/v2"
)

type K8sClient struct {
	clientset *kubernetes.Clientset
}

func NewK8s() (*K8sClient, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return &K8sClient{clientset: clientset}, nil
}

func (c K8sClient) WatchNodes() {
	watcher, err := c.clientset.CoreV1().Nodes().Watch(context.TODO(), metav1.ListOptions{LabelSelector: "node-role.kubernetes.io/control-plane="})
	if err != nil {
		panic(err.Error())
	}
	klog.Info("Watching for node changes\n")
	for event := range watcher.ResultChan() {
		node := event.Object.(*corev1.Node)
		klog.Infof("Event %s for node %s\n", event.Type, node.ObjectMeta.Name)
	}
}

func (c K8sClient) ListNodes() (*corev1.NodeList, error) {
	return c.clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{LabelSelector: "node-role.kubernetes.io/control-plane="})
}

func (c K8sClient) GetEtcdEndpoints() ([]string, error) {
	pods, err := c.clientset.CoreV1().Pods(metav1.NamespaceSystem).List(context.TODO(), metav1.ListOptions{LabelSelector: "component=etcd,tier=control-plane"})
	if err != nil {
		return nil, err
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
	return endpoints, nil
}

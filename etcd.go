package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"strings"
	"time"

	etcd "go.etcd.io/etcd/client/v3"

	"k8s.io/klog/v2"
)

type EtcdClient struct {
	client  *etcd.Client
	cluster etcd.Cluster
}

type CertPaths struct {
	caCert     string
	clientCert string
	clientKey  string
}

func NewEtcd(endpoints []string, certs CertPaths, ctx context.Context) (*EtcdClient, error) {
	ca := x509.NewCertPool()
	caCert, err := os.ReadFile(certs.caCert)
	if err != nil {
		return nil, err
	}
	if ok := ca.AppendCertsFromPEM(caCert); !ok {
		klog.Fatalf("Unable to parse cert from %s", certs.caCert)
	}

	clientCert, err := tls.LoadX509KeyPair(certs.clientCert, certs.clientKey)
	if err != nil {
		return nil, err
	}
	etcdTls := tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      ca,
	}
	client, err := etcd.New(etcd.Config{
		Endpoints:   endpoints,
		DialTimeout: 2 * time.Second,
		TLS:         &etcdTls,
	})
	if err != nil {
		return nil, err
	}

	cluster := etcd.NewCluster(client)

	return &EtcdClient{client: client, cluster: cluster}, nil
}

func (c EtcdClient) Close() {
	c.client.Close()
}

// Update the list of etcd endpoints the client uses
func (c EtcdClient) Sync(ctx context.Context) error {
	if err := c.client.Sync(ctx); err != nil {
		return err
	}
	klog.Infof("Found etcd endpoints %s from etcd cluster\n", strings.Join(c.client.Endpoints(), ","))
	return nil
}

func (c EtcdClient) MemberList(ctx context.Context) (*etcd.MemberListResponse, error) {
	return c.cluster.MemberList(ctx)
}

func (c EtcdClient) MemberRemove(ctx context.Context, id uint64) (*etcd.MemberRemoveResponse, error) {
	return c.cluster.MemberRemove(ctx, id)
}

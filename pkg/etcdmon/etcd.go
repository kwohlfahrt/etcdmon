package etcdmon

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"

	etcd "go.etcd.io/etcd/client/v3"

	"k8s.io/klog/v2"
)

type EtcdClient struct {
	client *etcd.Client
}

type CertPaths struct {
	CaCert     string
	ClientCert string
	ClientKey  string
}

func NewEtcd(endpoints []string, certs CertPaths, ctx context.Context) (*EtcdClient, error) {
	var ca *x509.CertPool = nil
	if certs.CaCert != "" {
		ca = x509.NewCertPool()
		caCert, err := os.ReadFile(certs.CaCert)
		if err != nil {
			return nil, err
		}
		if ok := ca.AppendCertsFromPEM(caCert); !ok {
			klog.Fatalf("Unable to parse cert from %s", certs.CaCert)
		}
	}

	var clientCerts []tls.Certificate = nil
	if certs.ClientCert != "" {
		clientCert, err := tls.LoadX509KeyPair(certs.ClientCert, certs.ClientKey)
		if err != nil {
			return nil, err
		}
		clientCerts = []tls.Certificate{clientCert}
	}

	etcdTls := tls.Config{RootCAs: ca, Certificates: clientCerts}
	client, err := etcd.New(etcd.Config{
		Endpoints:   endpoints,
		DialTimeout: 2 * time.Second,
		TLS:         &etcdTls,
	})
	if err != nil {
		return nil, err
	}

	return &EtcdClient{client: client}, nil
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
	return c.client.Cluster.MemberList(ctx)
}

func (c EtcdClient) MemberRemove(ctx context.Context, id uint64) (*etcd.MemberRemoveResponse, error) {
	return c.client.Cluster.MemberRemove(ctx, id)
}

func (c EtcdClient) MemberAdd(ctx context.Context, name string) (*etcd.MemberAddResponse, error) {
	return c.client.Cluster.MemberAdd(ctx, []string{fmt.Sprintf("https://%s:2380", name)})
}

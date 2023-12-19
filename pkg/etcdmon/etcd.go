package etcdmon

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"k8s.io/klog/v2"
)

type EtcdClient interface {
	Start(ctx context.Context, endpoints ...string) error
	MemberList(ctx context.Context) (*clientv3.MemberListResponse, error)
	MemberAdd(ctx context.Context, urls []string) (*clientv3.MemberAddResponse, error)
	MemberRemove(ctx context.Context, id uint64) (*clientv3.MemberRemoveResponse, error)
	SetEndpoints(endpoints ...string)
	IsHttps() bool
	Close() error
}

type CertPaths struct {
	CaCert     string
	ClientCert string
	ClientKey  string
}

type client struct {
	config clientv3.Config
	client *clientv3.Client
}

func NewEtcd(certs CertPaths) (EtcdClient, error) {
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

	var tlsConfig *tls.Config
	if ca != nil || clientCerts != nil {
		tlsConfig = &tls.Config{RootCAs: ca, Certificates: clientCerts}
	}

	etcd := &client{
		config: clientv3.Config{DialTimeout: 2 * time.Second, TLS: tlsConfig},
	}
	return etcd, nil
}

func (c *client) Start(ctx context.Context, endpoints ...string) error {
	c.config.Endpoints = endpoints
	c.config.Context = ctx
	x, err := clientv3.New(c.config)
	if err != nil {
		return err
	}
	c.client = x
	return nil
}

func (c *client) IsHttps() bool {
	return c.config.TLS != nil
}

func (c *client) Close() error {
	return c.client.Close()
}

func (c *client) MemberAdd(ctx context.Context, urls []string) (*clientv3.MemberAddResponse, error) {
	return c.client.MemberAdd(ctx, urls)
}

func (c *client) MemberList(ctx context.Context) (*clientv3.MemberListResponse, error) {
	return c.client.MemberList(ctx)
}

func (c *client) MemberRemove(ctx context.Context, id uint64) (*clientv3.MemberRemoveResponse, error) {
	return c.client.MemberRemove(ctx, id)
}

func (c *client) SetEndpoints(endpoints ...string) {
	c.client.SetEndpoints(endpoints...)
}

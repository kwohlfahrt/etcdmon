package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/envfuncs"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

var testenv env.Environment

func TestMain(m *testing.M) {
	testenv = env.New()
	kindClusterName := envconf.RandomName("etcdmon", 16)
	testenv.Setup(
		envfuncs.CreateKindClusterWithConfig(kindClusterName, "kindest/node:v1.24.2", "fixtures/kind.yaml"),
	)
	testenv.Finish(
		envfuncs.DestroyKindCluster(kindClusterName),
	)
	os.Exit(testenv.Run(m))
}

func TestKubernetes(t *testing.T) {
	f1 := features.New("count node").
		WithLabel("type", "node-count").
		Assess("nodes", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client := cfg.Client()
			var nodes corev1.NodeList
			err := wait.For(conditions.New(client.Resources()).ResourceListN(
				&nodes, 3, resources.WithLabelSelector(labels.FormatLabels(map[string]string{"node-role.kubernetes.io/control-plane": ""})),
			), wait.WithTimeout(time.Minute*1))
			if err != nil {
				t.Fatal(err)
			}

			var pods corev1.PodList
			err = wait.For(conditions.New(client.Resources()).ResourceListN(
				&pods, 3, resources.WithLabelSelector(labels.FormatLabels(map[string]string{"component": "etcd", "tier": "control-plane"})),
			), wait.WithTimeout(time.Minute*1))
			if err != nil {
				t.Fatal(err)
			}

			// Configured in kind.yaml
			etcdEndpoints := make([]string, 0, 3)
			for p := 5001; p <= 5003; p++ {
				etcdEndpoints = append(etcdEndpoints, fmt.Sprintf("127.0.0.1:%d", p))
			}
			etcdCerts := CertPaths{
				caCert: "./fixtures/ca.crt",
				clientCert: "./fixtures/ca.crt",
				clientKey: "./fixtures/ca.key",
			}
			etcdCtx, cancel := context.WithTimeout(ctx, 5 * time.Second)
			etcd, err := NewEtcd(etcdEndpoints, etcdCerts, etcdCtx)
			if err != nil {
				t.Fatal(err)
			}
			members, err := etcd.MemberList(etcdCtx)
			if err != nil {
				t.Fatal(err)
			}
			cancel()
			if len(members.Members) != 3 {
				t.Fatalf("Wrong number of etcd members: %d != 3", len(members.Members))
			}

			deletedNode := nodes.Items[0]
			deletedPods := make([]corev1.Pod, 0, 1)
			survivingPods := make([]corev1.Pod, 0, len(pods.Items) - 1)
			for _, pod := range pods.Items {
				if pod.Spec.NodeName == deletedNode.ObjectMeta.Name {
					deletedPods = append(deletedPods, pod)
				} else {
					survivingPods = append(survivingPods, pod)
				}
			}
			if len(deletedPods) != 1 {
				t.Fatalf("Wrong number of pods on deleted node: %d != 1", len(deletedPods))
			}

			err = cfg.Client().Resources().Delete(context.TODO(), &nodes.Items[0])
			if err != nil {
				t.Fatal(err)
			}
			err = wait.For(conditions.New(client.Resources()).ResourceDeleted(&deletedNode), wait.WithTimeout(time.Minute*1))
			if err != nil {
				t.Fatal(err)
			}
			err = wait.For(conditions.New(client.Resources()).ResourceDeleted(&deletedPods[0]), wait.WithTimeout(time.Minute*1))
			if err != nil {
				t.Fatal(err)
			}

			etcdCtx, cancel = context.WithTimeout(ctx, 5 * time.Second)
			members, err = etcd.MemberList(etcdCtx)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			if len(members.Members) != 2 {
				t.Fatalf("Wrong number of etcd members: %d != 2", len(members.Members))
			}

			return ctx
		}).Feature()

	testenv.Test(t, f1)
}

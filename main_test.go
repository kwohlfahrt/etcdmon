package main

import (
	"context"
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
			var pods corev1.PodList
			err := wait.For(conditions.New(client.Resources()).ResourceListN(
				&pods, 3, resources.WithLabelSelector(labels.FormatLabels(map[string]string{"component": "etcd", "tier": "control-plane"})),
			), wait.WithTimeout(time.Minute*1))
			if err != nil {
				t.Fatal(err)
			}

			var nodes corev1.NodeList
			err = wait.For(conditions.New(client.Resources()).ResourceListN(
				&nodes, 3, resources.WithLabelSelector(labels.FormatLabels(map[string]string{"node-role.kubernetes.io/control-plane": ""})),
			), wait.WithTimeout(time.Minute*1))
			if err != nil {
				t.Fatal(err)
			}

			err = cfg.Client().Resources().Delete(context.TODO(), &nodes.Items[0])
			if err != nil {
				t.Fatal(err)
			}

			err = wait.For(conditions.New(client.Resources()).ResourceDeleted(&nodes.Items[0]), wait.WithTimeout(time.Minute*1))
			if err != nil {
				t.Fatal(err)
			}

			return ctx
		}).Feature()

	testenv.Test(t, f1)
}

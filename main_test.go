package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

const nNodes = 4

var testenv env.Environment

func TestMain(m *testing.M) {
	cfg := envconf.NewWithKubeConfig("./kubeconfig.yaml")
	testenv = env.NewWithConfig(cfg)
	os.Exit(testenv.Run(m))
}

func TestKubernetes(t *testing.T) {
	var etcd *EtcdClient
	var nodes corev1.NodeList

	sync := features.New("node sync").
		WithSetup("etcd client", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client := cfg.Client()
			err := wait.For(conditions.New(client.Resources()).ResourceListN(
				&nodes, nNodes, resources.WithLabelSelector(labels.FormatLabels(map[string]string{"node-role.kubernetes.io/control-plane": ""})),
			), wait.WithTimeout(3*time.Minute))
			if err != nil {
				t.Fatal(err)
			}

			// Configured in kind.yaml
			etcdEndpoints := make([]string, 0, nNodes)
			for p := 5001; p < 5001+nNodes; p++ {
				etcdEndpoints = append(etcdEndpoints, fmt.Sprintf("127.0.0.1:%d", p))
			}
			etcdCerts := CertPaths{
				caCert:     "./fixtures/ca.crt",
				clientCert: "./fixtures/ca.crt",
				clientKey:  "./fixtures/ca.key",
			}
			etcdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			etcd, err = NewEtcd(etcdEndpoints, etcdCerts, etcdCtx)
			if err != nil {
				t.Fatal(err)
			}
			members, err := etcd.MemberList(etcdCtx)
			if err != nil {
				t.Fatal(err)
			}
			cancel()
			if len(members.Members) != nNodes {
				t.Fatalf("Wrong number of initial etcd members: %d != %d", len(members.Members), nNodes)
			}
			return ctx
		}).
		WithSetup("delete node", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client := cfg.Client()
			deletedNode := nodes.Items[0]
			err := cfg.Client().Resources().Delete(context.TODO(), &deletedNode)
			if err != nil {
				t.Fatal(err)
			}

			// Try twice, to give the load-balancer a chance to detect the downed node
			for i := 0; i < 2; i++ {
				err = wait.For(conditions.New(client.Resources()).ResourceDeleted(&deletedNode), wait.WithTimeout(time.Minute*1))
				if err == nil {
					break
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			return ctx
		}).
		WithSetup("install etcdmon", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client := cfg.Client()

			name := "etcdmon"
			labels := map[string]string{"app": name}
			serviceAccount := corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
			clusterRole := rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Rules: []rbacv1.PolicyRule{{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list", "watch"},
				}, {
					APIGroups: []string{""},
					Resources: []string{"nodes"},
					Verbs:     []string{"get", "list", "watch"},
				}},
			}
			clusterRoleBinding := rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "etcdmon"},
				Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "etcdmon", Namespace: "default"}},
			}
			var replicas int32 = 1
			hostPathDirectory := corev1.HostPathDirectory
			deployment := appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: labels},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{MatchLabels: labels},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: labels},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:            name,
								Image:           "etcdmon:latest",
								ImagePullPolicy: "Never",
								VolumeMounts: []corev1.VolumeMount{{
									Name:      "etcd-certs",
									MountPath: "/etc/kubernetes/pki/etcd",
									ReadOnly:  true,
								}},
							}},
							ServiceAccountName: name,
							Tolerations: []corev1.Toleration{
								{Key: "node-role.kubernetes.io/control-plane", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
								{Key: "node-role.kubernetes.io/master", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
							},
							NodeSelector: map[string]string{"node-role.kubernetes.io/control-plane": ""},
							Volumes: []corev1.Volume{{
								Name: "etcd-certs",
								VolumeSource: corev1.VolumeSource{
									HostPath: &corev1.HostPathVolumeSource{
										Path: "/etc/kubernetes/pki/etcd",
										Type: &hostPathDirectory,
									},
								},
							}},
						},
					},
				},
			}

			resources := []k8s.Object{&serviceAccount, &clusterRole, &clusterRoleBinding, &deployment}

			for _, r := range resources {
				if err := client.Resources().Create(ctx, r); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			// Without retries, timeouts are exceeded (even if the timeout is
			// longer). Probably due to etcd member being removed.
			for i := 0; i < 2; i++ {
				err = wait.For(
					conditions.New(client.Resources()).DeploymentConditionMatch(
						&deployment, appsv1.DeploymentAvailable, corev1.ConditionTrue,
					),
					wait.WithTimeout(time.Second*15),
				)
				if err == nil {
					break
				}
			}
			if err != nil {
				t.Fatal(err)
			}

			return ctx
		}).
		Assess("etcd removed on startup", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			etcdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			members, err := etcd.MemberList(etcdCtx)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			if len(members.Members) != nNodes-1 {
				t.Fatalf("Wrong number of final etcd members: %d != %d", len(members.Members), nNodes-1)
			}
			return ctx
		}).Feature()

	deletion := features.New("node deletion").
		WithSetup("delete node", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client := cfg.Client()
			deletedNode := nodes.Items[1]
			err := cfg.Client().Resources().Delete(context.TODO(), &deletedNode)
			if err != nil {
				t.Fatal(err)
			}

			// Try twice, to give the load-balancer a chance to detect the downed node
			for i := 0; i < 2; i++ {
				err = wait.For(conditions.New(client.Resources()).ResourceDeleted(&deletedNode), wait.WithTimeout(time.Minute*1))
				if err == nil {
					break
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			return ctx
		}).
		Assess("etcd removed on node deletion", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			etcdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			members, err := etcd.MemberList(etcdCtx)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			if len(members.Members) != nNodes-2 {
				t.Fatalf("Wrong number of final etcd members: %d != %d", len(members.Members), nNodes-2)
			}
			return ctx
		}).Feature()

	testenv.Test(t, sync, deletion)
}

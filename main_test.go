package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

const nNodes = 4

var testenv env.Environment

type contextKey string

func TestMain(m *testing.M) {
	cfg := envconf.NewWithKubeConfig("./kubeconfig.yaml")
	testenv = env.NewWithConfig(cfg)
	testenv.Setup(func(ctx context.Context, c *envconf.Config) (context.Context, error) {
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
		defer cancel()
		etcd, err := NewEtcd(etcdEndpoints, etcdCerts, etcdCtx)
		if err != nil {
			return nil, err
		}
		return context.WithValue(ctx, contextKey("etcd"), etcd), nil
	})

	os.Exit(testenv.Run(m))
}

func deleteNode(ctx context.Context, t *testing.T, client klient.Client, nodeName string) context.Context {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}

	err := client.Resources().Delete(ctx, node)
	if err != nil {
		t.Fatal(err)
	}

	// Try twice, to give the load-balancer a chance to detect the downed node
	for i := 0; i < 2; i++ {
		err = wait.For(conditions.New(client.Resources()).ResourceDeleted(node), wait.WithTimeout(time.Second*10))
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func waitForEtcdMembers(ctx context.Context, t *testing.T, client *EtcdClient, count int) context.Context {
	var members clientv3.MemberListResponse

	for {
		members, err := client.MemberList(ctx)
		if errors.Is(err, context.DeadlineExceeded) {
			break
		} else if err != nil {
			t.Fatal(err)
		}

		if len(members.Members) == count {
			return ctx
		}
	}

	t.Fatalf("Wrong number of final etcd members: %d != %d", len(members.Members), count)
	return ctx
}

func TestKubernetes(t *testing.T) {
	sync := features.New("node sync").
		WithSetup("delete node", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return deleteNode(ctx, t, cfg.Client(), "kind-control-plane")
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
							Tolerations:        []corev1.Toleration{{Key: "node-role.kubernetes.io/control-plane", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}},
							NodeSelector:       map[string]string{"node-role.kubernetes.io/control-plane": ""},
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
			// Without retries, timeouts are exceeded, with the pod stuck in
			// ContainerCreating. Possibly due to etcd member being removed.
			for i := 0; i < 4; i++ {
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
			etcd := ctx.Value(contextKey("etcd")).(*EtcdClient)
			etcdCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			return waitForEtcdMembers(etcdCtx, t, etcd, nNodes-1)
		}).Feature()

	deletion := features.New("node deletion").
		WithSetup("delete node", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			return deleteNode(ctx, t, cfg.Client(), "kind-control-plane2")
		}).
		Assess("etcd removed on node deletion", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			etcd := ctx.Value(contextKey("etcd")).(*EtcdClient)
			etcdCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			return waitForEtcdMembers(etcdCtx, t, etcd, nNodes-2)
		}).Feature()

	addition := features.New("node addition").
		WithSetup("label node as control-plane", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			nodes := corev1.NodeList{}
			cfg.Client().Resources().List(ctx, &nodes)
			for _, node := range nodes.Items {
				if _, ok := node.Labels["node-role.kubernetes.io/control-plane"]; !ok {
					cfg.Client().Resources().Label(&node, map[string]string{"node-role.kubernetes.io/control-plane": ""})
					if err := cfg.Client().Resources().Update(ctx, &node); err != nil {
						t.Fatal(err)
					}
					break
				}
			}

			return ctx
		}).
		Assess("etcd member added for control-plane node", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// Kind doesn't support adding nodes dynamically, so we can't start
			// the new etcd member.  We can only verify that the operator adds a
			// new member.  https://github.com/kubernetes-sigs/kind/issues/452
			etcd := ctx.Value(contextKey("etcd")).(*EtcdClient)
			etcdCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			return waitForEtcdMembers(etcdCtx, t, etcd, nNodes-1)
		}).Feature()

	testenv.Test(t, sync, deletion, addition)
}

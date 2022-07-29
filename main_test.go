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
			), wait.WithTimeout(time.Minute*3))
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

			name := "etcdmon"
			labels := map[string]string{"app": name}
			serviceAccount := corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
			clusterRole := rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Rules: []rbacv1.PolicyRule{{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list"},
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
								Name:  name,
								Image: "localhost:5000/etcdmon",
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
				if err = client.Resources().Create(ctx, r); err != nil {
					t.Fatal(err)
				}
			}
			err = wait.For(conditions.New(client.Resources()).DeploymentConditionMatch(
				&deployment, appsv1.DeploymentAvailable, corev1.ConditionTrue), wait.WithTimeout(time.Minute*1),
			)
			if err != nil {
				t.Fatal(err)
			}

			// Configured in kind.yaml
			etcdEndpoints := make([]string, 0, 3)
			for p := 5001; p <= 5003; p++ {
				etcdEndpoints = append(etcdEndpoints, fmt.Sprintf("127.0.0.1:%d", p))
			}
			etcdCerts := CertPaths{
				caCert:     "./fixtures/ca.crt",
				clientCert: "./fixtures/ca.crt",
				clientKey:  "./fixtures/ca.key",
			}
			etcdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
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
			survivingPods := make([]corev1.Pod, 0, len(pods.Items)-1)
			for _, pod := range pods.Items {
				if pod.Spec.NodeName == deletedNode.ObjectMeta.Name {
					deletedPods = append(deletedPods, pod)
				} else {
					survivingPods = append(survivingPods, pod)
				}
			}
			if len(deletedPods) != 1 {
				t.Fatalf("Wrong number of etcd pods on deleted node: %d != 1", len(deletedPods))
			}

			err = cfg.Client().Resources().Delete(context.TODO(), &nodes.Items[0])
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
			err = wait.For(conditions.New(client.Resources()).ResourceDeleted(&deletedPods[0]), wait.WithTimeout(time.Minute*5))
			if err != nil {
				t.Fatal(err)
			}

			etcdCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
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

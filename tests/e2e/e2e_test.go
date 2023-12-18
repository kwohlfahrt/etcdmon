package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kwohlfahrt/etcdmon/pkg/etcdmon"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

var testenv env.Environment

func TestMain(m *testing.M) {
	cfg := envconf.NewWithKubeConfig("./kubeconfig.yaml")
	testenv = env.NewWithConfig(cfg)
	os.Exit(testenv.Run(m))
}

func makeEtcdArgs(name string, replicas int32, new bool) []string {
	initialCluster := make([]string, 0, replicas)
	for i := int32(0); i < replicas; i++ {
		hostname := fmt.Sprintf("%s-%d", name, i)
		initialCluster = append(initialCluster, fmt.Sprintf("%s=http://%s.%s:2380", hostname, hostname, name))
	}

	initialState := "new"
	if !new {
		initialState = "existing"
	}

	return []string{
		"--name=$(HOSTNAME)",
		fmt.Sprintf("--initial-cluster-state=%s", initialState),
		fmt.Sprintf("--initial-cluster=%s", strings.Join(initialCluster, ",")),
		"--listen-peer-urls=http://$(POD_IP):2380",
		"--initial-advertise-peer-urls=http://$(POD_IP):2380",
		"--listen-client-urls=http://0.0.0.0:2379",
		"--advertise-client-urls=http://$(POD_IP):2379",
		"--data-dir=/var/lib/etcd",
	}
}

func startEtcd(name string, replicas int32) func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client := c.Client()

		labels := map[string]string{"app": "etcd", "instance": name}

		dnsService := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: corev1.ServiceSpec{
				Selector:                 labels,
				ClusterIP:                "None",
				PublishNotReadyAddresses: true,
				Ports: []corev1.ServicePort{
					{Name: "client", TargetPort: intstr.FromString("client"), Port: 2379},
				},
			},
		}

		statefulSet := appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: appsv1.StatefulSetSpec{
				Replicas:            &replicas,
				Selector:            &metav1.LabelSelector{MatchLabels: labels},
				ServiceName:         dnsService.Name,
				PodManagementPolicy: appsv1.ParallelPodManagement,
				VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
					ObjectMeta: metav1.ObjectMeta{Name: "etcd"},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("128Mi"),
						}},
					},
				}},
				PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
					WhenScaled:  appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
					WhenDeleted: appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: labels},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:    "etcd",
							Image:   "registry.k8s.io/etcd:3.5.6-0",
							Command: []string{"etcd"},
							Args:    makeEtcdArgs(name, replicas, true),
							Ports: []corev1.ContainerPort{
								{Name: "client", ContainerPort: 2379},
								{Name: "health", ContainerPort: 2379},
							},
							ReadinessProbe: &corev1.Probe{
								TimeoutSeconds: 15,
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/health?serializable=true",
										Port: intstr.FromString("health"),
									},
								},
							},
							StartupProbe: &corev1.Probe{
								FailureThreshold: 10,
								TimeoutSeconds:   15,
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/health?serializable=false",
										Port: intstr.FromString("health"),
									},
								},
							},
							VolumeMounts: []corev1.VolumeMount{{Name: "etcd", MountPath: "/var/lib/etcd"}},
							Env: []corev1.EnvVar{{
								Name: "POD_IP",
								ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
								},
							}, {
								Name: "HOSTNAME",
								ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
								},
							}},
						}},
					},
				},
			},
		}

		service := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-node", name), Namespace: "default"},
			Spec: corev1.ServiceSpec{
				Selector: labels,
				Type:     corev1.ServiceTypeNodePort,
				Ports: []corev1.ServicePort{
					{Name: "client", TargetPort: intstr.FromString("client"), Port: 2379, NodePort: 30790},
				},
			},
		}

		resources := []k8s.Object{&dnsService, &statefulSet, &service}
		for _, r := range resources {
			if err := client.Resources().Create(ctx, r); err != nil {
				t.Fatal(err)
			}
		}

		if err := wait.For(
			conditions.New(client.Resources()).ResourceScaled(&statefulSet, func(object k8s.Object) int32 {
				statefulSet := object.(*appsv1.StatefulSet)
				return statefulSet.Status.ReadyReplicas
			}, replicas),
			wait.WithTimeout(time.Minute*1),
		); err != nil {
			t.Fatal(err)
		}

		return ctx
	}
}

func stopEtcd(name string) func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client := c.Client()

		service := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-node", name), Namespace: "default"},
		}
		statefulSet := appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		}
		dnsService := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		}
		resources := []k8s.Object{&dnsService, &statefulSet, &service}
		for _, r := range resources {
			if err := client.Resources().Delete(ctx, r); err != nil {
				t.Fatal(err)
			}
		}

		for _, r := range resources {
			wait.For(conditions.New(client.Resources()).ResourceDeleted(r), wait.WithTimeout(time.Minute*1))
		}

		return ctx
	}
}

func scaleEtcd(name string, replicas int32, start int32) func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client := c.Client()
		etcd := appsv1.StatefulSet{}
		if err := client.Resources().Get(ctx, "foo", "default", &etcd); err != nil {
			t.Fatal(err)
		}

		etcd.Spec.Replicas = &replicas
		etcd.Spec.Ordinals = &appsv1.StatefulSetOrdinals{Start: start}
		etcd.Spec.Template.Spec.Containers[0].Args = makeEtcdArgs(name, replicas, false)
		if err := client.Resources().Update(ctx, &etcd); err != nil {
			t.Fatal(err)
		}

		if err := wait.For(
			conditions.New(client.Resources()).ResourceScaled(&etcd, func(object k8s.Object) int32 {
				statefulSet := object.(*appsv1.StatefulSet)
				return statefulSet.Status.CurrentReplicas
			}, replicas),
			wait.WithTimeout(time.Minute*1),
		); err != nil {
			t.Fatal(err)
		}

		return ctx
	}
}

func startEtcdmon(etcdName string) func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client := c.Client()

		replicas := int32(1)
		etcdLabels := map[string]string{"app": "etcd", "instance": etcdName}
		etcdmonLabels := map[string]string{"app": "etcdmon"}
		selector := labels.SelectorFromSet(etcdLabels)

		role := rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: "etcdmon", Namespace: "default"},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch"}},
			},
		}
		roleBinding := rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "etcdmon", Namespace: "default"},
			RoleRef:    rbacv1.RoleRef{Name: role.Name, APIGroup: rbacv1.SchemeGroupVersion.Group, Kind: "Role"},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "default", Namespace: "default"}},
		}
		deployment := appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "etcdmon", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{MatchLabels: etcdmonLabels},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: etcdmonLabels},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:            "etcdmon",
							Image:           "etcdmon:latest",
							ImagePullPolicy: corev1.PullNever,
							Args: []string{
								"--namespace=default",
								fmt.Sprintf("--selector=%s", selector.String()),
								"--timeout=5s",
								"-v=3",
							},
						}},
					},
				},
			},
		}

		resources := []k8s.Object{&role, &roleBinding, &deployment}
		for _, r := range resources {
			if err := client.Resources().Create(ctx, r); err != nil {
				t.Fatal(err)
			}
		}

		if err := wait.For(
			conditions.New(client.Resources()).ResourceScaled(&deployment, func(object k8s.Object) int32 {
				deployment := object.(*appsv1.Deployment)
				return deployment.Status.AvailableReplicas
			}, 1),
			wait.WithTimeout(time.Second*10),
		); err != nil {
			t.Fatal(err)
		}

		return ctx
	}
}

func stopEtcdmon(name string) func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client := c.Client()

		role := rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: "etcdmon", Namespace: "default"},
		}
		roleBinding := rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "etcdmon", Namespace: "default"},
		}
		deployment := appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "etcdmon", Namespace: "default"},
		}

		resources := []k8s.Object{&role, &roleBinding, &deployment}
		for _, r := range resources {
			if err := client.Resources().Delete(ctx, r); err != nil {
				t.Fatal(err)
			}
		}

		for _, r := range resources {
			wait.For(conditions.New(client.Resources()).ResourceDeleted(r), wait.WithTimeout(time.Minute*1))
		}

		return ctx
	}
}

func waitForEtcd(name string, count int) func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client := c.Client()

		etcd := appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
		if err := wait.For(
			conditions.New(client.Resources()).ResourceScaled(&etcd, func(object k8s.Object) int32 {
				statefulSet := object.(*appsv1.StatefulSet)
				return min(statefulSet.Status.ReadyReplicas, statefulSet.Status.CurrentReplicas)
			}, int32(count)),
			wait.WithTimeout(time.Minute*1),
		); err != nil {
			t.Error(err)
		}

		etcdClient, err := etcdmon.NewEtcd(etcdmon.CertPaths{})

		if err != nil {
			t.Fatal(err)
		}
		err = etcdClient.Start(ctx, "http://localhost:5001")
		if err != nil {
			t.Fatal(err)
		}

		for ctx.Err() == nil {
			etcdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			members, err := etcdClient.MemberList(etcdCtx)
			cancel()

			if err != nil {
				t.Fatal(err)
				break
			} else if len(members.Members) == count {
				break
			} else {
				time.Sleep(500 * time.Millisecond)
			}
		}

		return ctx
	}
}

func TestKubernetes(t *testing.T) {
	remove := features.New("remove etcd member").
		WithSetup("start etcd", startEtcd("foo", int32(3))).
		WithSetup("start etcdmon", startEtcdmon("foo")).
		WithTeardown("stop etcdmon", stopEtcdmon("foo")).
		WithTeardown("stop etcd", stopEtcd("foo")).
		WithSetup("remove etcd member", scaleEtcd("foo", 2, 0)).
		Assess("etcd has correct members", waitForEtcd("foo", 2)).Feature()

	add := features.New("add etcd member").
		WithSetup("start etcd", startEtcd("foo", int32(2))).
		WithSetup("start etcdmon", startEtcdmon("foo")).
		WithTeardown("stop etcdmon", stopEtcdmon("foo")).
		WithTeardown("stop etcd", stopEtcd("foo")).
		WithSetup("remove etcd member", scaleEtcd("foo", 3, 0)).
		Assess("etcd has correct members", waitForEtcd("foo", 3)).Feature()

	replace := features.New("replace etcd member").
		WithSetup("start etcd", startEtcd("foo", int32(3))).
		WithSetup("start etcdmon", startEtcdmon("foo")).
		WithSetup("replace etcd member", scaleEtcd("foo", 3, 1)).
		WithTeardown("stop etcdmon", stopEtcdmon("foo")).
		WithTeardown("stop etcd", stopEtcd("foo")).
		Assess("etcd has correct members", waitForEtcd("foo", 3)).Feature()

	removeOnStartup := features.New("remove etcd member on startup").
		WithSetup("start etcd", startEtcd("foo", int32(3))).
		WithSetup("remove etcd member", scaleEtcd("foo", 2, 0)).
		WithSetup("start etcdmon", startEtcdmon("foo")).
		WithTeardown("stop etcdmon", stopEtcdmon("foo")).
		WithTeardown("stop etcd", stopEtcd("foo")).
		Assess("etcd has correct members", waitForEtcd("foo", 2)).Feature()

	addOnStartup := features.New("add etcd member on startup").
		WithSetup("start etcd", startEtcd("foo", int32(2))).
		WithSetup("add etcd member", scaleEtcd("foo", 3, 0)).
		WithSetup("start etcdmon", startEtcdmon("foo")).
		WithTeardown("stop etcdmon", stopEtcdmon("foo")).
		WithTeardown("stop etcd", stopEtcd("foo")).
		Assess("etcd has correct members", waitForEtcd("foo", 3)).Feature()

	replaceOnStartup := features.New("replace etcd member on startup").
		WithSetup("start etcd", startEtcd("foo", int32(3))).
		WithSetup("replace etcd member", scaleEtcd("foo", 3, 1)).
		WithSetup("start etcdmon", startEtcdmon("foo")).
		WithTeardown("stop etcdmon", stopEtcdmon("foo")).
		WithTeardown("stop etcd", stopEtcd("foo")).
		Assess("etcd has correct members", waitForEtcd("foo", 3)).Feature()

	testenv.Test(t, remove, add, replace, removeOnStartup, addOnStartup, replaceOnStartup)
}

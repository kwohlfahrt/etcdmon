package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kwohlfahrt/etcdmon/pkg/etcdmon"
	"go.etcd.io/etcd/api/v3/etcdserverpb"

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

type contextKey string

func TestMain(m *testing.M) {
	cfg := envconf.NewWithKubeConfig("./kubeconfig.yaml").WithParallelTestEnabled()
	testenv = env.NewWithConfig(cfg)
	os.Exit(testenv.Run(m))
}

type certificatePair struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

func (c *certificatePair) KeyPem() (key string, err error) {
	encoded, err := x509.MarshalECPrivateKey(c.Key)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: encoded})), nil
}

func (c *certificatePair) CertPem() (key string, err error) {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Cert.Raw})), nil

}

func makeCertificate(ca *certificatePair, subject pkix.Name, expiry time.Duration) (*certificatePair, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	maxSerial := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, maxSerial)
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               subject,
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              time.Now().Add(expiry),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	var certDer []byte
	if ca == nil {
		template.IsCA = true
		template.KeyUsage |= x509.KeyUsageCertSign
		certDer, err = x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	} else {
		certDer, err = x509.CreateCertificate(rand.Reader, template, ca.Cert, &key.PublicKey, ca.Key)
	}
	if err != nil {
		return nil, err
	}

	cert, err := x509.ParseCertificate(certDer)
	if err != nil {
		return nil, err
	}

	return &certificatePair{Cert: cert, Key: key}, nil
}

func createCa() func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		ca, err := makeCertificate(nil, pkix.Name{CommonName: "etcd CA"}, 48*time.Hour)
		if err != nil {
			t.Fatal(err.Error())
		}
		return context.WithValue(ctx, contextKey("ca"), ca)
	}
}

func makeEtcdArgs(name string, replicas int32, start int32, new bool) []string {
	initialCluster := make([]string, 0, replicas)
	for i := start; i < start+replicas; i++ {
		hostname := fmt.Sprintf("%s-%d", name, i)
		initialCluster = append(initialCluster, fmt.Sprintf("%s=https://%s.%s:2380", hostname, hostname, name))
	}

	initialState := "new"
	if !new {
		initialState = "existing"
	}

	return []string{
		"--name=$(HOSTNAME)",
		fmt.Sprintf("--initial-cluster-state=%s", initialState),
		fmt.Sprintf("--initial-cluster=%s", strings.Join(initialCluster, ",")),
		"--listen-peer-urls=https://$(POD_IP):2380",
		"--initial-advertise-peer-urls=https://$(POD_IP):2380",
		"--listen-client-urls=https://0.0.0.0:2379",
		"--advertise-client-urls=https://$(POD_IP):2379",
		"--listen-metrics-urls=http://$(POD_IP):2378",
		"--client-cert-auth=true",
		"--trusted-ca-file=/etc/etcd/pki/ca.pem",
		"--cert-file=/etc/etcd/pki/cert.pem",
		"--key-file=/etc/etcd/pki/key.pem",
		"--peer-client-cert-auth=true",
		"--peer-trusted-ca-file=/etc/etcd/pki/ca.pem",
		"--peer-cert-file=/etc/etcd/pki/cert.pem",
		"--peer-key-file=/etc/etcd/pki/key.pem",
		"--data-dir=/var/lib/etcd",
	}
}

func createNamespace(name string, nodePort int32) func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client := c.Client()
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: name, Labels: map[string]string{"app": "etcdmon-test"},
		}}
		if err := client.Resources().Create(ctx, ns); err != nil {
			t.Fatal(err)
		}

		ctx = context.WithValue(ctx, contextKey("namespace"), ns)
		return context.WithValue(ctx, contextKey("nodePort"), nodePort)
	}
}

func deleteNamespace() func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client := c.Client()
		ns := ctx.Value(contextKey("namespace")).(*corev1.Namespace)
		if err := client.Resources().Delete(ctx, ns); err != nil {
			t.Fatal(err)
		}

		return ctx
	}
}

func startEtcd(name string, replicas int32) func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client := c.Client()
		nodePort := ctx.Value(contextKey("nodePort")).(int32)
		ns := ctx.Value(contextKey("namespace")).(*corev1.Namespace)

		labels := map[string]string{"app": "etcd", "instance": name}

		ca := ctx.Value(contextKey("ca")).(*certificatePair)
		certPem, err := ca.CertPem()
		if err != nil {
			t.Fatal(err.Error())
		}
		keyPem, err := ca.KeyPem()
		if err != nil {
			t.Fatal(err.Error())
		}

		certSecret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "ca-cert", Namespace: ns.Name},
			StringData: map[string]string{"cert.pem": certPem, "key.pem": keyPem},
		}

		dnsService := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name},
			Spec: corev1.ServiceSpec{
				Selector:                 labels,
				ClusterIP:                "None",
				PublishNotReadyAddresses: true,
				Ports: []corev1.ServicePort{
					{Name: "client", TargetPort: intstr.FromString("client"), Port: 2379},
				},
			},
		}

		env := []corev1.EnvVar{{
			Name: "POD_IP",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
			},
		}, {
			Name: "HOSTNAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
			},
		}, {
			Name:  "SERVICE_NAME",
			Value: dnsService.Name,
		}}

		statefulSet := appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name},
			Spec: appsv1.StatefulSetSpec{
				Replicas:            &replicas,
				Selector:            &metav1.LabelSelector{MatchLabels: labels},
				ServiceName:         dnsService.Name,
				PodManagementPolicy: appsv1.ParallelPodManagement,
				VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
					ObjectMeta: metav1.ObjectMeta{Name: "etcd"},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
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
						InitContainers: []corev1.Container{{
							Name:  "certs",
							Image: "alpine/openssl:3.3.2",
							Args: []string{
								"req", "-x509", "-newkey=ec", "-pkeyopt=ec_paramgen_curve:prime256v1", "-nodes",
								"-CA=/etc/etcd/ca/cert.pem", "-CAkey=/etc/etcd/ca/key.pem",
								"-keyout=/etc/etcd/pki/key.pem", "-out=/etc/etcd/pki/cert.pem",
								"-days=1", "-subj=/CN=etcd",
								"-addext=subjectAltName=DNS:localhost,DNS:$(HOSTNAME).$(SERVICE_NAME),IP:$(POD_IP),IP:127.0.0.1",
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "ca", MountPath: "/etc/etcd/ca"},
								{Name: "pki", MountPath: "/etc/etcd/pki"},
							},
							Env: env,
						}, {
							Name:  "ca",
							Image: "busybox",
							Args:  []string{"cp", "/etc/etcd/ca/cert.pem", "/etc/etcd/pki/ca.pem"},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "ca", MountPath: "/etc/etcd/ca"},
								{Name: "pki", MountPath: "/etc/etcd/pki"},
							},
							Env: env,
						}},
						Containers: []corev1.Container{{
							Name:    "etcd",
							Image:   "registry.k8s.io/etcd:3.5.17-0",
							Command: []string{"etcd"},
							Args:    makeEtcdArgs(name, replicas, 0, true),
							Ports: []corev1.ContainerPort{
								{Name: "client", ContainerPort: 2379},
								{Name: "metrics", ContainerPort: 2378},
							},
							ReadinessProbe: &corev1.Probe{
								TimeoutSeconds: 15,
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/readyz",
										Port: intstr.FromString("metrics"),
									},
								},
							},
							StartupProbe: &corev1.Probe{
								FailureThreshold: 10,
								TimeoutSeconds:   15,
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/health?serializable=true",
										Port: intstr.FromString("metrics"),
									},
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "etcd", MountPath: "/var/lib/etcd"},
								{Name: "pki", MountPath: "/etc/etcd/pki"},
							},
							Env: env,
						}},
						Volumes: []corev1.Volume{
							{Name: "pki", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
							{Name: "ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: certSecret.Name}}},
						},
					},
				},
			},
		}

		service := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-node", name), Namespace: ns.Name},
			Spec: corev1.ServiceSpec{
				Selector: labels,
				Type:     corev1.ServiceTypeNodePort,
				Ports: []corev1.ServicePort{
					{Name: "client", TargetPort: intstr.FromString("client"), Port: 2379, NodePort: nodePort},
				},
			},
		}

		resources := []k8s.Object{&certSecret, &dnsService, &statefulSet, &service}
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
			wait.WithTimeout(time.Minute*2),
		); err != nil {
			t.Fatal(err)
		}

		return ctx
	}
}

func scaleEtcd(name string, replicas int32, start int32) func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client := c.Client()
		ns := ctx.Value(contextKey("namespace")).(*corev1.Namespace)

		etcd := appsv1.StatefulSet{}
		if err := client.Resources().Get(ctx, "foo", ns.Name, &etcd); err != nil {
			t.Fatal(err)
		}

		etcd.Spec.Replicas = &replicas
		etcd.Spec.Ordinals = &appsv1.StatefulSetOrdinals{Start: start}
		etcd.Spec.Template.Spec.Containers[0].Args = makeEtcdArgs(name, replicas, start, false)
		if err := client.Resources().Update(ctx, &etcd); err != nil {
			t.Fatal(err)
		}

		if err := wait.For(
			conditions.New(client.Resources()).ResourceScaled(&etcd, func(object k8s.Object) int32 {
				statefulSet := object.(*appsv1.StatefulSet)
				return statefulSet.Status.Replicas
			}, replicas),
			wait.WithTimeout(time.Second*30),
		); err != nil {
			t.Fatal(err)
		}

		return ctx
	}
}

func createEtcdmonCert(expiry time.Duration, update bool) func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client := c.Client()
		ns := ctx.Value(contextKey("namespace")).(*corev1.Namespace)

		ca := ctx.Value(contextKey("ca")).(*certificatePair)
		caPem, err := ca.CertPem()
		if err != nil {
			t.Fatal(err.Error())
		}
		cert, err := makeCertificate(ca, pkix.Name{CommonName: "etcdmon"}, expiry)
		if err != nil {
			t.Fatal(err.Error())
		}
		certPem, err := cert.CertPem()
		if err != nil {
			t.Fatal(err.Error())
		}
		keyPem, err := cert.KeyPem()
		if err != nil {
			t.Fatal(err.Error())
		}

		certSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "etcdmon-cert", Namespace: ns.Name},
			StringData: map[string]string{"ca.pem": caPem, "cert.pem": certPem, "key.pem": keyPem},
		}

		if !update {
			err = client.Resources().Create(ctx, certSecret)
		} else {
			err = client.Resources().Update(ctx, certSecret)
		}
		if err != nil {
			t.Fatal(err)
		}
		if update {
			// Wait for the update to propagate before continuing
			time.Sleep(1 * time.Minute)
		}

		ctx = context.WithValue(ctx, contextKey("etcdmon-certs"), certSecret)
		return ctx
	}
}

func startEtcdmon(etcdName string) func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client := c.Client()
		ns := ctx.Value(contextKey("namespace")).(*corev1.Namespace)
		certSecret := ctx.Value(contextKey("etcdmon-certs")).(*corev1.Secret)

		replicas := int32(1)
		etcdLabels := map[string]string{"app": "etcd", "instance": etcdName}
		etcdmonLabels := map[string]string{"app": "etcdmon"}
		selector := labels.SelectorFromSet(etcdLabels)

		role := rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: "etcdmon", Namespace: ns.Name},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch"}},
			},
		}
		roleBinding := rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "etcdmon", Namespace: ns.Name},
			RoleRef:    rbacv1.RoleRef{Name: role.Name, APIGroup: rbacv1.SchemeGroupVersion.Group, Kind: "Role"},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "default", Namespace: ns.Name}},
		}
		deployment := appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "etcdmon", Namespace: ns.Name},
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
								fmt.Sprintf("--namespace=%s", ns.Name),
								fmt.Sprintf("--selector=%s", selector.String()),
								"--ca-cert=/etc/etcdmon/pki/ca.pem",
								"--client-cert=/etc/etcdmon/pki/cert.pem",
								"--client-key=/etc/etcdmon/pki/key.pem",
								"--timeout=5s",
								"-v=3",
							},
							VolumeMounts: []corev1.VolumeMount{{Name: "pki", MountPath: "/etc/etcdmon/pki"}},
						}},
						Volumes: []corev1.Volume{
							{Name: "pki", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: certSecret.Name}}},
						},
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

func waitForEtcd(name string, count int) func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client := c.Client()
		nodePort := ctx.Value(contextKey("nodePort")).(int32)
		ns := ctx.Value(contextKey("namespace")).(*corev1.Namespace)

		ca := ctx.Value(contextKey("ca")).(*certificatePair)
		cert, err := makeCertificate(ca, pkix.Name{CommonName: "test-client"}, 1*time.Hour)
		if err != nil {
			t.Fatal(err.Error())
		}

		etcd := appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name}}
		if err := wait.For(
			conditions.New(client.Resources()).ResourceScaled(&etcd, func(object k8s.Object) int32 {
				statefulSet := object.(*appsv1.StatefulSet)
				return min(statefulSet.Status.ReadyReplicas, statefulSet.Status.CurrentReplicas)
			}, int32(count)),
			wait.WithTimeout(time.Minute*2),
		); err != nil {
			t.Error(err)
		}

		caPool := x509.NewCertPool()
		caPool.AddCert(ca.Cert)
		tlsConfig := &tls.Config{
			RootCAs:      caPool,
			Certificates: []tls.Certificate{{Certificate: [][]byte{cert.Cert.Raw}, PrivateKey: cert.Key}},
		}
		etcdClient := etcdmon.NewEtcd(tlsConfig)

		if err != nil {
			t.Fatal(err)
		}
		err = etcdClient.Start(ctx, fmt.Sprintf("https://localhost:%d", nodePort))
		if err != nil {
			t.Fatal(err)
		}

		for ctx.Err() == nil {
			etcdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			members, err := etcdClient.MemberList(etcdCtx)
			cancel()

			fullMembers := make([]*etcdserverpb.Member, 0, len(members.Members))
			for _, member := range members.Members {
				if !member.IsLearner {
					fullMembers = append(fullMembers, member)
				}
			}

			if err != nil {
				t.Fatal(err)
				break
			} else if len(fullMembers) == count {
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
		WithSetup("create namespace", createNamespace("remove-etcd-test", 30790)).
		WithSetup("create ca", createCa()).
		WithSetup("create etcdmon certs", createEtcdmonCert(1*time.Hour, false)).
		WithTeardown("delete namespace", deleteNamespace()).
		WithSetup("start etcd", startEtcd("foo", int32(3))).
		WithSetup("start etcdmon", startEtcdmon("foo")).
		WithSetup("remove etcd member", scaleEtcd("foo", 2, 0)).
		Assess("etcd has correct members", waitForEtcd("foo", 2)).Feature()

	add := features.New("add etcd member").
		WithSetup("create namespace", createNamespace("add-etcd-test", 30791)).
		WithSetup("create ca", createCa()).
		WithSetup("create etcdmon certs", createEtcdmonCert(1*time.Hour, false)).
		WithTeardown("delete namespace", deleteNamespace()).
		WithSetup("start etcd", startEtcd("foo", int32(2))).
		WithSetup("start etcdmon", startEtcdmon("foo")).
		WithSetup("add etcd member", scaleEtcd("foo", 3, 0)).
		Assess("etcd has correct members", waitForEtcd("foo", 3)).Feature()

	replace := features.New("replace etcd member").
		WithSetup("create namespace", createNamespace("replace-etcd-test", 30792)).
		WithSetup("create ca", createCa()).
		WithSetup("create etcdmon certs", createEtcdmonCert(1*time.Hour, false)).
		WithTeardown("delete namespace", deleteNamespace()).
		WithSetup("start etcd", startEtcd("foo", int32(3))).
		WithSetup("start etcdmon", startEtcdmon("foo")).
		WithSetup("replace etcd member", scaleEtcd("foo", 3, 1)).
		Assess("etcd has correct members", waitForEtcd("foo", 3)).Feature()

	removeOnStartup := features.New("remove etcd member on startup").
		WithSetup("create namespace", createNamespace("startup-remove-etcd-test", 30793)).
		WithSetup("create ca", createCa()).
		WithSetup("create etcdmon certs", createEtcdmonCert(1*time.Hour, false)).
		WithTeardown("delete namespace", deleteNamespace()).
		WithSetup("start etcd", startEtcd("foo", int32(3))).
		WithSetup("remove etcd member", scaleEtcd("foo", 2, 0)).
		WithSetup("start etcdmon", startEtcdmon("foo")).
		Assess("etcd has correct members", waitForEtcd("foo", 2)).Feature()

	addOnStartup := features.New("add etcd member on startup").
		WithSetup("create namespace", createNamespace("startup-add-etcd-test", 30794)).
		WithSetup("create ca", createCa()).
		WithSetup("create etcdmon certs", createEtcdmonCert(1*time.Hour, false)).
		WithSetup("start etcd", startEtcd("foo", int32(2))).
		WithSetup("add etcd member", scaleEtcd("foo", 3, 0)).
		WithSetup("start etcdmon", startEtcdmon("foo")).
		Assess("etcd has correct members", waitForEtcd("foo", 3)).Feature()

	replaceOnStartup := features.New("replace etcd member on startup").
		WithSetup("create namespace", createNamespace("startup-replace-etcd-test", 30795)).
		WithSetup("create ca", createCa()).
		WithSetup("create etcdmon certs", createEtcdmonCert(1*time.Hour, false)).
		WithSetup("start etcd", startEtcd("foo", int32(3))).
		WithSetup("replace etcd member", scaleEtcd("foo", 3, 1)).
		WithSetup("start etcdmon", startEtcdmon("foo")).
		Assess("etcd has correct members", waitForEtcd("foo", 3)).Feature()

	refreshCertificate := features.New("refresh etcd certificate").
		WithSetup("create namespace", createNamespace("certificate-test", 30796)).
		WithSetup("create ca", createCa()).
		WithSetup("create etcdmon certs", createEtcdmonCert(-1*time.Second, false)).
		WithSetup("start etcd", startEtcd("foo", int32(2))).
		WithSetup("start etcdmon", startEtcdmon("foo")).
		WithSetup("update etcdmon certs", createEtcdmonCert(1*time.Hour, true)).
		WithSetup("add etcd member", scaleEtcd("foo", 3, 0)).
		Assess("etcd has correct members", waitForEtcd("foo", 3)).Feature()

	testenv.TestInParallel(t, remove, add, replace, removeOnStartup, addOnStartup, replaceOnStartup, refreshCertificate)
}

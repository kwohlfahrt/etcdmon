ARG KUBERNETES_VERSION=1.24.2
FROM kindest/node:v${KUBERNETES_VERSION}
COPY ./ca.key ./ca.crt /etc/kubernetes/pki/etcd/

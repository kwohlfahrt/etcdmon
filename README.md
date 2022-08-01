# etcdmon

This tool helps administer kubernetes control-planes using the [stacked etcd
topology], by deleting etcd members when their kubernetes node is removed.

If these members are not deleted, and a new node is brought up to replace it,
`kubeadm join` on the new node will fail, when it detects an unhealthy etcd
node. By automatically removing the etcd member, the control-plane can be
backed by e.g. an AWS autoscaling group, that automatically replaces failed
nodes.

## Alternatives

An alternatve approach is to use persistent storage for the nodes, as described
in [this post](https://www.signifytechnology.com/blog/2018/01/monzo-very-robust-etcd)
by Monzo. This requires one auto-scaling group per control-plane node, which
makes it difficult to automate rolling upgrades of the nodes.

[stacked etcd topology]: https://kubernetes.io/docs/setup/production-environment/tools/kubeadm/ha-topology/#stacked-etcd-topology

# kubevirt-ipam-claims
This repo provide a KubeVirt extension to create (and manage the lifecycle of)
`IPAMClaim`s on behalf of KubeVirt virtual machines.

## Description
This project provides a Kubernetes controller and mutating webhook that will
monitor KubeVirt virtual machines.

When it sees a KubeVirt VM being created, it will create an IPAMClaim for the
VM interfaces attached to a network that features the `persistent ips` feature.

It will also mutate the launcher pod where the VM will run to request a
persistent IP from the CNI plugin.

It implements the
[multi-network de-facto standard v1.3](https://github.com/k8snetworkplumbingwg/multi-net-spec/tree/master/v1.3)
IPAM extensions, explicitly the IPAMClaim CRD, and the `ipam-claim-reference`
network selection element attribute, defined in sections 8.2, and 4.1.2.1.11
respectively.

The [OVN-Kubernetes CNI](https://github.com/ovn-org/ovn-kubernetes) is a CNI
that implements this IPAM multi-network standard.

## Getting Started

### Prerequisites
- go version v1.21.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/kubevirt-ipam-claims:<tag>
```

**NOTE:** This image ought to be published in the personal registry you specified. 
And it is required to have access to pull the image from the working environment. 
Make sure you have the proper permission to the registry if the above commands don’t work.

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/kubevirt-ipam-claims:<tag>
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin 
privileges or be logged in as admin.

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following are the steps to build the installer and distribute this project to users.

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/kubevirt-ipam-claims:<tag>
```

NOTE: The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without
its dependencies.

2. Using the installer

Users can just run kubectl apply -f <URL for YAML BUNDLE> to install the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/maiqueb/kubevirt-ipam-claims/main/dist/install.yaml
```

## Requesting persistent IPs for KubeVirt VMs
To opt-in to this feature, the network must allow persistent IPs; for that,
the user should configure the network-attachment-definition in the following
way:
```yaml
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: mynet
  namespace: default
spec:
  config: |2
    {
        "cniVersion": "0.3.1",
        "name": "tenantblue",
        "netAttachDefName": "default/mynet",
        "topology": "layer2",
        "type": "ovn-k8s-cni-overlay",
        "subnets": "192.168.200.0/24",
        "excludeSubnets": "192.168.200.1/32",
        "allowPersistentIPs": true
    }
```

The relevant configuration is the `allowPersistentIPs` key.

Once the NAD has been provisioned, the user should provision a VM whose
interfaces connect to this network. Take the following yaml as an example:
```yaml
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  labels:
    kubevirt.io/vm: vm-a
  name: vm-a
spec:
  running: true
  template:
    metadata:
      name: vm-a
      namespace: default
    spec:
      domain:
        devices:
          disks:
          - disk:
              bus: virtio
            name: containerdisk
          - disk:
              bus: virtio
            name: cloudinitdisk
          interfaces:
          - bridge: {}
            name: anet
          rng: {}
        resources:
          requests:
            memory: 1024M
      networks:
      - multus:
          networkName: default/mynet
        name: anet
      terminationGracePeriodSeconds: 0
      volumes:
      - containerDisk:
          image: quay.io/kubevirt/fedora-with-test-tooling-container-disk:v1.2.0
        name: containerdisk
      - cloudInitNoCloud:
          userData: |-
            #cloud-config
            password: fedora
            chpasswd: { expire: False }
        name: cloudinitdisk
```

The controller should create the required `IPAMClaim`, then mutate the launcher
pods to request using the aforementioned claims to persist their IP addresses.

## Contributing
Currently, there's not much to be said ... Just ensure if you're updating code
to provide unit-tests.

This section will be improved later on.

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.


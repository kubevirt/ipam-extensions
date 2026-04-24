# 2. Persistent IPAM via IPAMClaim CRDs

Date: 2026-04-24

## Status

Accepted

## Context

KubeVirt VMs need persistent IP addresses across restarts and migrations.
The Kubernetes Network Plumbing Working Group
[multi-network specification v1.3](https://github.com/k8snetworkplumbingwg/multi-net-spec/tree/master/v1.3)
defines the IPAMClaim CRD and `ipam-claim-reference` network selection element
attribute for this purpose (sections 8.2 and 4.1.2.1.11).

OVN-Kubernetes CNI implements this specification.

## Decision

Implement a Kubernetes controller and mutating webhook:

- **VirtualMachine controller** creates and manages IPAMClaim CRDs on behalf of
  KubeVirt VMs, handling the full lifecycle via finalizers.
- **VirtualMachineInstance controller** manages IPAM claims for running instances.
- **Pod mutation webhook** annotates virt-launcher pods with IPAM claim references
  so the CNI plugin allocates persistent IPs.

The project uses the Kubebuilder framework with controller-runtime.

## Consequences

- VMs get persistent IPs without manual IPAM management
- Depends on CNI plugins implementing multi-network spec v1.3
- Requires cert-manager for webhook TLS certificate management
- Lifecycle management (create/cleanup) handled via Kubernetes finalizers
- Controller must be deployed alongside KubeVirt and a compatible CNI

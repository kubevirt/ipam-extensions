package testobjects

import (
	"fmt"
	"strings"
	"time"

	virtv1 "kubevirt.io/api/core/v1"

	"k8s.io/utils/ptr"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubevirt/ipam-extensions/pkg/claims"

	ipamclaimsapi "github.com/k8snetworkplumbingwg/ipamclaims/pkg/crd/ipamclaims/v1alpha1"
	nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
)

func DummyVM(nadName string) *virtv1.VirtualMachine {
	return &virtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm1",
			Namespace: "ns1",
		},
		Spec: virtv1.VirtualMachineSpec{
			Template: &virtv1.VirtualMachineInstanceTemplateSpec{
				Spec: DummyVMISpec(nadName),
			},
		},
	}
}

func DummyVMI(nadName string) *virtv1.VirtualMachineInstance {
	return &virtv1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm1",
			Namespace: "ns1",
		},
		Spec: DummyVMISpec(nadName),
	}
}

func DummyVMISpec(nadName string) virtv1.VirtualMachineInstanceSpec {
	return virtv1.VirtualMachineInstanceSpec{
		Networks: []virtv1.Network{
			{
				Name:          "podnet",
				NetworkSource: virtv1.NetworkSource{Pod: &virtv1.PodNetwork{}},
			},
			{
				Name: "randomnet",
				NetworkSource: virtv1.NetworkSource{
					Multus: &virtv1.MultusNetwork{
						NetworkName: nadName,
					},
				},
			},
		},
	}
}

func IpamClaimsCleaner(ipamClaims ...ipamclaimsapi.IPAMClaim) []ipamclaimsapi.IPAMClaim {
	for i := range ipamClaims {
		ipamClaims[i].ObjectMeta.ResourceVersion = ""
	}
	return ipamClaims
}

func DummyIPAMClaimWithFinalizer(namespace, vmName string) *ipamclaimsapi.IPAMClaim {
	ipamClaim := DummyIPAMClaimWithoutFinalizer(namespace, vmName)
	ipamClaim.Finalizers = []string{claims.KubevirtVMFinalizer}
	return ipamClaim
}

func DummyIPAMClaimWithoutFinalizer(namespace, vmName string) *ipamclaimsapi.IPAMClaim {
	return &ipamclaimsapi.IPAMClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s.%s", vmName, "randomnet"),
			Namespace: namespace,
			Labels:    claims.OwnedByVMLabel(vmName),
			OwnerReferences: []metav1.OwnerReference{{
				Name:               vmName,
				Kind:               "VirtualMachine",
				Controller:         ptr.To(true),
				BlockOwnerDeletion: ptr.To(true),
			}},
		},
		Spec: ipamclaimsapi.IPAMClaimSpec{
			Network: "goodnet",
		},
	}
}

func DummyNAD(nadName string) *nadv1.NetworkAttachmentDefinition {
	namespaceAndName := strings.Split(nadName, "/")
	return &nadv1.NetworkAttachmentDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespaceAndName[0],
			Name:      namespaceAndName[1],
		},
		Spec: nadv1.NetworkAttachmentDefinitionSpec{
			Config: `{"name": "goodnet", "allowPersistentIPs": true}`,
		},
	}
}

func DummyMarkedForDeletionVM(nadName string) *virtv1.VirtualMachine {
	vm := DummyVM(nadName)
	vm.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	vm.ObjectMeta.Finalizers = []string{metav1.FinalizerDeleteDependents}

	return vm
}

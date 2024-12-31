package env

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/utils/pointer"

	nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	kubevirtv1 "kubevirt.io/api/core/v1"
)

func GenerateLayer2WithSubnetNAD(nadName, namespace, role string) *nadv1.NetworkAttachmentDefinition {
	const randCharacters = 5
	networkName := strings.Join([]string{"l2", role, rand.String(randCharacters)}, "-")
	return &nadv1.NetworkAttachmentDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      nadName,
		},
		Spec: nadv1.NetworkAttachmentDefinitionSpec{
			Config: fmt.Sprintf(`
{
        "cniVersion": "0.3.0",
        "name": "%[3]s",
        "type": "ovn-k8s-cni-overlay",
        "topology": "layer2",
        "subnets": "10.100.200.0/24",
        "netAttachDefName": "%[1]s/%[2]s",
        "role": "%[4]s",
        "allowPersistentIPs": true
}
`, namespace, nadName, networkName, role),
		},
	}
}

type VMIOption func(vmi *kubevirtv1.VirtualMachineInstance)

func NewVirtualMachineInstance(namespace string, opts ...VMIOption) *kubevirtv1.VirtualMachineInstance {
	vmi := &kubevirtv1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      RandomName("alpine", 16),
		},
		Spec: kubevirtv1.VirtualMachineInstanceSpec{
			Domain: kubevirtv1.DomainSpec{
				Devices: kubevirtv1.Devices{
					Disks: []kubevirtv1.Disk{
						{
							DiskDevice: kubevirtv1.DiskDevice{
								Disk: &kubevirtv1.DiskTarget{
									Bus: kubevirtv1.DiskBusVirtio,
								},
							},
							Name: "containerdisk",
						},
					},
					Interfaces: []kubevirtv1.Interface{},
				},
			},
			Networks:                      []kubevirtv1.Network{},
			TerminationGracePeriodSeconds: pointer.Int64(5),
			Volumes: []kubevirtv1.Volume{
				{
					Name: "containerdisk",
					VolumeSource: kubevirtv1.VolumeSource{
						ContainerDisk: &kubevirtv1.ContainerDiskSource{
							Image: "quay.io/kubevirtci/alpine-container-disk-demo:devel_alt",
						},
					},
				},
			},
		},
	}

	for _, f := range opts {
		f(vmi)
	}

	return vmi
}

func WithMemory(memory string) VMIOption {
	return func(vmi *kubevirtv1.VirtualMachineInstance) {
		vmi.Spec.Domain.Resources.Requests = corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse(memory),
		}
	}
}

func WithInterface(iface kubevirtv1.Interface) VMIOption {
	return func(vmi *kubevirtv1.VirtualMachineInstance) {
		vmi.Spec.Domain.Devices.Interfaces = append(vmi.Spec.Domain.Devices.Interfaces, iface)
	}
}

func WithNetwork(network kubevirtv1.Network) VMIOption {
	return func(vmi *kubevirtv1.VirtualMachineInstance) {
		vmi.Spec.Networks = append(vmi.Spec.Networks, network)
	}
}

func WithCloudInitNoCloudVolume(cloudInitNetworkData string) VMIOption {
	return func(vmi *kubevirtv1.VirtualMachineInstance) {
		vmi.Spec.Volumes = append(vmi.Spec.Volumes, kubevirtv1.Volume{
			Name: "cloudinitdisk",
			VolumeSource: kubevirtv1.VolumeSource{
				CloudInitNoCloud: &kubevirtv1.CloudInitNoCloudSource{
					NetworkData: cloudInitNetworkData,
				},
			},
		})
	}
}

type VMOption func(vm *kubevirtv1.VirtualMachine)

func NewVirtualMachine(vmi *kubevirtv1.VirtualMachineInstance, opts ...VMOption) *kubevirtv1.VirtualMachine {
	vm := &kubevirtv1.VirtualMachine{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kubevirtv1.GroupVersion.String(),
			Kind:       "VirtualMachine",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmi.Name,
			Namespace: vmi.Namespace,
		},
		Spec: kubevirtv1.VirtualMachineSpec{
			Running: pointer.Bool(false),
			Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: vmi.ObjectMeta.Annotations,
					Labels:      vmi.ObjectMeta.Labels,
				},
				Spec: vmi.Spec,
			},
		},
	}

	for _, f := range opts {
		f(vm)
	}

	return vm
}

func WithRunning() VMOption {
	return func(vm *kubevirtv1.VirtualMachine) {
		vm.Spec.Running = pointer.Bool(true)
	}
}

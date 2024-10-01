package env

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubevirtv1 "kubevirt.io/api/core/v1"
)

// ThisVMI fetches the latest state of the VirtualMachineInstance. If the object does not exist, nil is returned.
func ThisVMI(vmi *kubevirtv1.VirtualMachineInstance) func() (*kubevirtv1.VirtualMachineInstance, error) {
	return func() (p *kubevirtv1.VirtualMachineInstance, err error) {
		p = &kubevirtv1.VirtualMachineInstance{}
		err = Client.Get(context.Background(), client.ObjectKeyFromObject(vmi), p)
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return
	}
}

func ThisVMReadiness(vm *kubevirtv1.VirtualMachine) func() (bool, error) {
	return func() (bool, error) {
		if err := Client.Get(context.Background(), client.ObjectKeyFromObject(vm), vm); err != nil {
			return false, err
		}
		return vm.Status.Ready, nil
	}
}

func lookupInterfaceStatusByName(interfaces []kubevirtv1.VirtualMachineInstanceNetworkInterface, name string) *kubevirtv1.VirtualMachineInstanceNetworkInterface {
	for index := range interfaces {
		if interfaces[index].Name == name {
			return &interfaces[index]
		}
	}
	return nil
}

func GetIPsFromVMIStatus(vmi *kubevirtv1.VirtualMachineInstance, networkInterfaceName string) []string {
	ifaceStatus := lookupInterfaceStatusByName(vmi.Status.Interfaces, networkInterfaceName)
	if ifaceStatus == nil {
		return nil
	}
	return ifaceStatus.IPs
}

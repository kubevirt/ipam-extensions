package env

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

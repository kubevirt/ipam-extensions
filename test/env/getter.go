package env

import (
	"context"
	"encoding/json"
	"fmt"

	nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	corev1 "k8s.io/api/core/v1"
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

func virtualMachineInstancePod(vmi *kubevirtv1.VirtualMachineInstance) (*corev1.Pod, error) {
	pod, err := lookupPodBySelector(vmi.Namespace, vmiLabelSelector(vmi), vmiFieldSelector(vmi))
	if err != nil {
		return nil, fmt.Errorf("failed to find pod for VMI %s (%s)", vmi.Name, string(vmi.GetUID()))
	}
	return pod, nil
}

func lookupPodBySelector(namespace string, labelSelector, fieldSelector map[string]string) (*corev1.Pod, error) {
	pods := &corev1.PodList{}
	err := Client.List(
		context.Background(),
		pods,
		client.InNamespace(namespace),
		client.MatchingLabels(labelSelector),
		client.MatchingFields(fieldSelector))
	if err != nil {
		return nil, err
	}

	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("failed to lookup pod with labels %v, fields %v in namespace %s", labelSelector, fieldSelector, namespace)
	}

	return &pods.Items[0], nil
}

func vmiLabelSelector(vmi *kubevirtv1.VirtualMachineInstance) map[string]string {
	return map[string]string{kubevirtv1.CreatedByLabel: string(vmi.GetUID())}
}

func vmiFieldSelector(vmi *kubevirtv1.VirtualMachineInstance) map[string]string {
	fieldSelectors := map[string]string{}
	if vmi.Status.Phase == kubevirtv1.Running {
		const podPhase = "status.phase"
		fieldSelectors[podPhase] = string(corev1.PodRunning)
	}
	return fieldSelectors
}

func parsePodNetworkStatusAnnotation(podNetStatus string) ([]nadv1.NetworkStatus, error) {
	if len(podNetStatus) == 0 {
		return nil, fmt.Errorf("network status annotation not found")
	}

	var netStatus []nadv1.NetworkStatus
	if err := json.Unmarshal([]byte(podNetStatus), &netStatus); err != nil {
		return nil, err
	}

	return netStatus, nil
}

func DefaultNetworkStatus(vmi *kubevirtv1.VirtualMachineInstance) (*nadv1.NetworkStatus, error) {
	virtLauncherPod, err := virtualMachineInstancePod(vmi)
	if err != nil {
		return nil, err
	}

	netStatuses, err := parsePodNetworkStatusAnnotation(virtLauncherPod.Annotations[nadv1.NetworkStatusAnnot])
	if err != nil {
		return nil, err
	}

	for _, netStatus := range netStatuses {
		if netStatus.Default {
			return &netStatus, nil
		}
	}
	return nil, fmt.Errorf("primary IPs not found")
}

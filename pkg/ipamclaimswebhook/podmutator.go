/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ipamclaimswebhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	netutils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"

	virtv1 "kubevirt.io/api/core/v1"

	"github.com/kubevirt/ipam-extensions/pkg/claims"
	"github.com/kubevirt/ipam-extensions/pkg/config"
	"github.com/kubevirt/ipam-extensions/pkg/ips"
	"github.com/kubevirt/ipam-extensions/pkg/udn"
)

// +kubebuilder:webhook:path=/mutate-v1-pod,mutating=true,failurePolicy=fail,groups="",resources=pods,verbs=create;update,versions=v1,name=ipam-claims.k8s.cni.cncf.io,admissionReviewVersions=v1,sideEffects=None
//nolint:lll

// IPAMClaimsValet annotates Pods
type IPAMClaimsValet struct {
	client.Client
	decoder                admission.Decoder
	defaultNetNADNamespace string
}

type Option func(*IPAMClaimsValet)

func NewIPAMClaimsValet(manager manager.Manager, opts ...Option) *IPAMClaimsValet {
	claimsManager := &IPAMClaimsValet{
		decoder: admission.NewDecoder(manager.GetScheme()),
		Client:  manager.GetClient(),
	}
	for _, opt := range opts {
		opt(claimsManager)
	}
	return claimsManager
}

func WithDefaultNetNADNamespace(namespace string) Option {
	return func(ipamValet *IPAMClaimsValet) {
		ipamValet.defaultNetNADNamespace = namespace
	}
}

func (a *IPAMClaimsValet) Handle(ctx context.Context, request admission.Request) admission.Response {
	log := logf.FromContext(ctx)

	pod := &corev1.Pod{}
	if err := a.decoder.Decode(request, pod); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	log.Info("webhook handling event")

	vmName, hasVMAnnotation := pod.Annotations["kubevirt.io/domain"]
	if !hasVMAnnotation {
		log.Info(
			"does not have the kubevirt VM annotation",
		)
		return admission.Allowed("not a VM")
	}

	networkSelectionElements, err := netutils.ParsePodNetworkAnnotation(pod)
	if err != nil {
		var goodTypeOfError *v1.NoK8sNetworkError
		if !errors.As(err, &goodTypeOfError) {
			return admission.Errored(http.StatusBadRequest, fmt.Errorf("failed to parse pod network selection elements"))
		}
	}

	var newPod *corev1.Pod
	hasChangedNetworkSelectionElements, err :=
		ensureIPAMClaimRefAtNetworkSelectionElements(ctx, a.Client, pod, vmName, networkSelectionElements)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	if hasChangedNetworkSelectionElements {
		if newPod == nil {
			newPod = pod.DeepCopy()
		}
		if err := updatePodSelectionElements(newPod, networkSelectionElements); err != nil {
			return admission.Errored(http.StatusInternalServerError, err)
		}
	}

	vmKey := types.NamespacedName{Namespace: pod.Namespace, Name: vmName}
	vmi := &virtv1.VirtualMachineInstance{}
	if err := a.Get(context.Background(), vmKey, vmi); err != nil {
		return admission.Errored(
			http.StatusInternalServerError,
			fmt.Errorf(
				"failed to access the VMI running in pod %q: %w",
				types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}.String(),
				err,
			),
		)
	}

	primaryNetwork, err := primaryNetworkConfig(a.Client, ctx, vmi)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}

	if primaryNetwork != nil {
		log.Info(
			"primary network attachment found",
			"network", primaryNetwork.Name,
		)
		primaryUDNInterface, err := findPrimaryUDNInterface(ctx, vmi, primaryNetwork)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError,
				fmt.Errorf("failed looking for primary user defined IPAMClaim name: %v", err))
		}

		if primaryUDNInterface != nil {
			if newPod == nil {
				newPod = pod.DeepCopy()
			}

			primaryUDNIPRequests, err := ips.VmiInterfaceIPRequests(vmi, primaryUDNInterface.Name, primaryNetwork)
			if err != nil {
				return admission.Errored(http.StatusInternalServerError, err)
			}

			primaryUDNNetworkSelectionElement := multusDefaultNetworkAnnotation(
				a.defaultNetNADNamespace,
				primaryUDNInterface.MacAddress,
				claims.ComposeKey(vmi.Name, primaryUDNInterface.Name),
				primaryUDNIPRequests...,
			)

			// TODO: once we have deprecated the ipam-claim dedicated OVN-K annotation, we can drop the if below
			//if len(primaryUDNIPRequests) > 0 {
			if err := definePodMultusDefaultNetworkAnnotation(newPod, primaryUDNNetworkSelectionElement); err != nil {
				return admission.Errored(http.StatusInternalServerError, err)
			}
			//}

			// Set the legacy OVN primary network IPAM claim annotation for backwards compatibility
			updatePodWithOVNPrimaryNetworkIPAMClaimAnnotation(newPod, claims.ComposeKey(vmi.Name, primaryUDNInterface.Name))
		}
	}

	if newPod != nil {
		if reflect.DeepEqual(newPod, pod) {
			return admission.Allowed("mutation not needed")
		}

		marshaledPod, err := json.Marshal(newPod)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, err)
		}

		log.Info("new pod annotations", "pod", newPod.Annotations)
		return admission.PatchResponseFromRaw(request.Object.Raw, marshaledPod)
	}

	return admission.Allowed("carry on")
}

// returns the names of the kubevirt VM networks indexed by their NAD name
func vmiSecondaryNetworks(vmi *virtv1.VirtualMachineInstance) map[string]string {
	indexedSecondaryNetworks := map[string]string{}
	for _, network := range vmi.Spec.Networks {
		if network.Multus == nil {
			continue
		}
		if network.Multus.Default {
			continue
		}

		nadName := network.Multus.NetworkName // NAD name must be formatted in <ns>/<name> format
		if !strings.Contains(network.Multus.NetworkName, "/") {
			nadName = fmt.Sprintf("%s/%s", vmi.Namespace, network.Multus.NetworkName)
		}
		indexedSecondaryNetworks[nadName] = network.Name
	}

	return indexedSecondaryNetworks
}

func updatePodSelectionElements(pod *corev1.Pod, networks []*v1.NetworkSelectionElement) error {
	newNets, err := json.Marshal(networks)
	if err != nil {
		return err
	}
	pod.Annotations[v1.NetworkAttachmentAnnot] = string(newNets)
	return nil
}

func definePodMultusDefaultNetworkAnnotation(pod *corev1.Pod, networkConfig *v1.NetworkSelectionElement) error {
	rawNetData, err := json.Marshal([]*v1.NetworkSelectionElement{networkConfig})
	if err != nil {
		return fmt.Errorf("failed to marshal network configuration: %w", err)
	}
	pod.Annotations[config.MultusDefaultNetAnnotation] = string(rawNetData)
	return nil
}

func updatePodWithOVNPrimaryNetworkIPAMClaimAnnotation(pod *corev1.Pod, ipamClaimName string) {
	pod.Annotations[config.OVNPrimaryNetworkIPAMClaimAnnotation] = ipamClaimName
}

func ensureIPAMClaimRefAtNetworkSelectionElements(ctx context.Context,
	cli client.Client, pod *corev1.Pod, vmName string,
	networkSelectionElements []*v1.NetworkSelectionElement) (changed bool, err error) {
	log := logf.FromContext(ctx)
	hasChangedNetworkSelectionElements := false
	for i, networkSelectionElement := range networkSelectionElements {
		nadName := fmt.Sprintf("%s/%s", networkSelectionElement.Namespace, networkSelectionElement.Name)
		log.Info(
			"iterating network selection elements",
			"NAD", nadName,
		)
		nadKey := types.NamespacedName{
			Namespace: networkSelectionElement.Namespace,
			Name:      networkSelectionElement.Name,
		}

		nad := v1.NetworkAttachmentDefinition{}
		if err := cli.Get(context.Background(), nadKey, &nad); err != nil {
			if k8serrors.IsNotFound(err) {
				log.Info("NAD not found, will hang on scheduler", "NAD", nadName)
				return false, nil
			}
			return false, err
		}

		pluginConfig, err := config.NewConfig(nad.Spec.Config)
		if err != nil {
			return false, err
		}

		if !pluginConfig.AllowPersistentIPs {
			continue
		}

		log.Info(
			"will request persistent IPs",
			"NAD", nadName,
			"network", pluginConfig.Name,
		)

		vmKey := types.NamespacedName{Namespace: pod.Namespace, Name: vmName}
		vmi := &virtv1.VirtualMachineInstance{}
		if err := cli.Get(context.Background(), vmKey, vmi); err != nil {
			return false, err
		}

		vmiNets := vmiSecondaryNetworks(vmi)
		networkName, foundNetworkName := vmiNets[nadKey.String()]
		if !foundNetworkName {
			log.Info(
				"network name not found",
				"NAD", nadName,
				"network", networkName,
			)
			continue
		}

		networkSelectionElements[i].IPAMClaimReference = claims.ComposeKey(vmName, networkName)
		log.Info(
			"requesting claim",
			"NAD", nadName,
			"network", pluginConfig.Name,
			"claim", networkSelectionElement.IPAMClaimReference,
		)
		hasChangedNetworkSelectionElements = true
		continue
	}
	return hasChangedNetworkSelectionElements, nil
}

func findPrimaryUDNInterface(
	ctx context.Context,
	vmi *virtv1.VirtualMachineInstance,
	pluginConfig *config.RelevantConfig,
) (*virtv1.Interface, error) {
	log := logf.FromContext(ctx)

	if !pluginConfig.AllowPersistentIPs {
		return nil, nil
	}

	podNetwork := vmiPodNetwork(vmi)
	if podNetwork == nil {
		log.Info(
			"vmi has no pod network primary UDN ipam claim will not be created",
			"vmi",
			client.ObjectKeyFromObject(vmi),
		)
		return nil, nil
	}

	return vmiNetworkInterface(vmi, *podNetwork), nil
}

func primaryNetworkConfig(
	cli client.Client,
	ctx context.Context,
	vmi *virtv1.VirtualMachineInstance,
) (*config.RelevantConfig, error) {
	log := logf.FromContext(ctx)

	log.Info(
		"Looking for primary network config",
		"vmi",
		client.ObjectKeyFromObject(vmi),
	)

	primaryNetworkNAD, err := udn.FindPrimaryNetwork(ctx, cli, vmi.Namespace)
	if err != nil {
		return nil, err
	}

	if primaryNetworkNAD == nil {
		log.Info(
			"Did not find primary network config",
			"namespace", vmi.Namespace,
		)
		return nil, nil
	}

	log.Info(
		"Found primary network NAD",
		"NAD",
		client.ObjectKeyFromObject(primaryNetworkNAD),
		"NADs contents",
		primaryNetworkNAD.Spec.Config,
	)

	pluginConfig, err := config.NewConfig(primaryNetworkNAD.Spec.Config)
	log.Info(
		"plugin config",
		"namespace", vmi.Namespace,
		"plugin", pluginConfig,
	)
	if err != nil {
		return nil, err
	}
	return pluginConfig, nil
}

// returns the KubeVirt VM pod network
func vmiPodNetwork(vmi *virtv1.VirtualMachineInstance) *virtv1.Network {
	for _, network := range vmi.Spec.Networks {
		if network.Pod != nil {
			return &network
		}
	}
	return nil
}

func vmiNetworkInterface(vmi *virtv1.VirtualMachineInstance, network virtv1.Network) *virtv1.Interface {
	for _, iface := range vmi.Spec.Domain.Devices.Interfaces {
		if iface.Name == network.Name {
			return &iface
		}
	}
	return nil
}

func multusDefaultNetworkAnnotation(
	namespace string,
	mac string,
	ipamClaimName string,
	ips ...string,
) *v1.NetworkSelectionElement {
	const defaultNetworkName = "default"
	return &v1.NetworkSelectionElement{
		Name:               defaultNetworkName,
		Namespace:          namespace,
		IPRequest:          ips,
		MacRequest:         mac,
		IPAMClaimReference: ipamClaimName,
	}
}

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

	"github.com/kubevirt/ipam-extensions/pkg/config"
)

// +kubebuilder:webhook:path=/mutate-v1-pod,mutating=true,failurePolicy=fail,groups="",resources=pods,verbs=create;update,versions=v1,name=ipam-claims.k8s.cni.cncf.io,admissionReviewVersions=v1,sideEffects=None

// IPAMClaimsValet annotates Pods
type IPAMClaimsValet struct {
	client.Client
	decoder *admission.Decoder
}

func NewIPAMClaimsValet(manager manager.Manager) *IPAMClaimsValet {
	return &IPAMClaimsValet{
		decoder: admission.NewDecoder(manager.GetScheme()),
		Client:  manager.GetClient(),
	}
}

func (a *IPAMClaimsValet) Handle(ctx context.Context, request admission.Request) admission.Response {
	log := logf.FromContext(ctx)

	pod := &corev1.Pod{}
	if err := a.decoder.Decode(request, pod); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	vmName, hasVMAnnotation := pod.Annotations["kubevirt.io/domain"]
	var primaryUDNNetwork *config.RelevantConfig
	if hasVMAnnotation {
		log.Info("webhook handling event - checking primary UDN flow for", "VM", vmName, "namespace", pod.Namespace)
		var err error
		primaryUDNNetwork, err = a.vmiPrimaryUDN(ctx, pod.Namespace)
		if err != nil {
			// TODO: figure out what to do. Probably fail
			return admission.Errored(http.StatusInternalServerError, err)
		}

		if primaryUDNNetwork != nil && primaryUDNNetwork.AllowPersistentIPs {
			log.Info(
				"found primary UDN for",
				"vmName",
				vmName,
				"namespace",
				pod.Namespace,
				"primary UDN name",
				primaryUDNNetwork.Name,
			)
			annotatePodWithUDN(pod, vmName, primaryUDNNetwork.Name)
		}
	}

	log.Info("webhook handling event - checking secondary networks flow for", "pod", pod.Name, "namespace", pod.Namespace)
	networkSelectionElements, err := netutils.ParsePodNetworkAnnotation(pod)
	if err != nil {
		var goodTypeOfError *v1.NoK8sNetworkError
		if errors.As(err, &goodTypeOfError) && primaryUDNNetwork == nil {
			return admission.Allowed("no secondary networks requested")
		} else if primaryUDNNetwork == nil {
			return admission.Errored(http.StatusBadRequest, fmt.Errorf("failed to parse pod network selection elements"))
		}
	}

	var (
		hasChangedNetworkSelectionElements bool
		podNetworkSelectionElements        = make([]v1.NetworkSelectionElement, 0, len(networkSelectionElements))
	)
	for _, networkSelectionElement := range networkSelectionElements {
		nadName := types.NamespacedName{
			Namespace: networkSelectionElement.Namespace,
			Name:      networkSelectionElement.Name,
		}.String()
		log.Info(
			"iterating network selection elements",
			"NAD", nadName,
		)
		nadKey := types.NamespacedName{
			Namespace: networkSelectionElement.Namespace,
			Name:      networkSelectionElement.Name,
		}

		nad := v1.NetworkAttachmentDefinition{}
		if err := a.Client.Get(context.Background(), nadKey, &nad); err != nil {
			if k8serrors.IsNotFound(err) {
				return admission.Allowed("NAD not found, will hang on scheduler")
			}
			return admission.Errored(http.StatusInternalServerError, err)
		}

		pluginConfig, err := config.NewConfig(nad.Spec.Config)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, err)
		}

		if pluginConfig.AllowPersistentIPs {
			log.Info(
				"will request persistent IPs",
				"NAD", nadName,
				"network", pluginConfig.Name,
			)
			if !hasVMAnnotation {
				log.Info(
					"does not have the kubevirt VM annotation",
					"NAD", nadName,
					"network", pluginConfig.Name,
				)
				return admission.Allowed("not a VM")
			}

			vmKey := types.NamespacedName{Namespace: pod.Namespace, Name: vmName}
			vmi := &virtv1.VirtualMachineInstance{}
			if err := a.Client.Get(context.Background(), vmKey, vmi); err != nil {
				return admission.Errored(http.StatusInternalServerError, err)
			}

			vmiNets := vmiSecondaryNetworks(vmi)
			networkName, foundNetworkName := vmiNets[nadKey.String()]
			if !foundNetworkName {
				log.Info(
					"network name not found",
					"NAD", nadName,
					"network", networkName,
				)
				podNetworkSelectionElements = append(podNetworkSelectionElements, *networkSelectionElement)
				continue
			}

			networkSelectionElement.IPAMClaimReference = fmt.Sprintf("%s.%s", vmName, networkName)
			log.Info(
				"requesting claim",
				"NAD", nadName,
				"network", pluginConfig.Name,
				"claim", networkSelectionElement.IPAMClaimReference,
			)
			podNetworkSelectionElements = append(podNetworkSelectionElements, *networkSelectionElement)
			hasChangedNetworkSelectionElements = true
			continue
		}
		podNetworkSelectionElements = append(podNetworkSelectionElements, *networkSelectionElement)
	}

	if len(podNetworkSelectionElements) > 0 || (primaryUDNNetwork != nil && primaryUDNNetwork.AllowPersistentIPs && vmName != "") {
		newPod, err := podWithUpdatedSelectionElements(pod, podNetworkSelectionElements)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, err)
		}
		if primaryUDNNetwork == nil && (reflect.DeepEqual(newPod, pod) || !hasChangedNetworkSelectionElements) {
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

func podWithUpdatedSelectionElements(pod *corev1.Pod, networks []v1.NetworkSelectionElement) (*corev1.Pod, error) {
	newPod := pod.DeepCopy()
	newNets, err := json.Marshal(networks)
	if err != nil {
		return nil, err
	}
	if string(newNets) != "[]" {
		newPod.Annotations[v1.NetworkAttachmentAnnot] = string(newNets)
	}
	return newPod, nil
}

func annotatePodWithUDN(pod *corev1.Pod, vmName string, primaryUDNName string) {
	const ovnUDNIPAMClaimName = "k8s.ovn.org/ovn-udn-ipamclaim-reference"
	udnAnnotations := pod.Annotations
	udnAnnotations[ovnUDNIPAMClaimName] = fmt.Sprintf("%s.%s-primary-udn", vmName, primaryUDNName)
	pod.SetAnnotations(udnAnnotations)
}

func (a *IPAMClaimsValet) vmiPrimaryUDN(ctx context.Context, namespace string) (*config.RelevantConfig, error) {
	const (
		NetworkRolePrimary   = "primary"
		NetworkRoleSecondary = "secondary"
	)

	log := logf.FromContext(ctx)
	var namespaceNads v1.NetworkAttachmentDefinitionList
	if err := a.List(ctx, &namespaceNads, &client.ListOptions{}); err != nil {
		return nil, fmt.Errorf("failed to list the NADs on namespace %q: %v", namespace, err)
	}

	for _, nad := range namespaceNads.Items {
		networkConfig, err := config.NewConfig(nad.Spec.Config)
		if err != nil {
			log.Error(
				err,
				"failed extracting the relevant NAD configuration",
				"NAD name",
				nad.Name,
				"NAD namespace",
				nad.Namespace,
			)
			return nil, fmt.Errorf("failed to extract the relevant NAD information")
		}

		if networkConfig.Role == NetworkRolePrimary {
			return networkConfig, nil
		}
	}
	return nil, nil
}

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

	"github.com/maiqueb/kubevirt-ipam-claims/pkg/config"
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
	networkSelectionElements, err := netutils.ParsePodNetworkAnnotation(pod)
	if err != nil {
		var goodTypeOfError *v1.NoK8sNetworkError
		if errors.As(err, &goodTypeOfError) {
			return admission.Allowed("no secondary networks requested")
		}
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("failed to parse pod network selection elements"))
	}
	var (
		hasChangedNetworkSelectionElements bool
		podNetworkSelectionElements        = make([]v1.NetworkSelectionElement, 0, len(networkSelectionElements))
	)
	for _, networkSelectionElement := range networkSelectionElements {
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
			vmName, hasVMAnnotation := pod.Annotations["kubevirt.io/domain"]
			if !hasVMAnnotation {
				return admission.Allowed("not a VM")
			}
			vmKey := types.NamespacedName{
				Namespace: pod.Namespace,
				Name:      vmName,
			}

			vmi := &virtv1.VirtualMachineInstance{}
			if err := a.Client.Get(context.Background(), vmKey, vmi); err != nil {
				return admission.Errored(http.StatusInternalServerError, err)
			}

			vmiNets := vmiSecondaryNetworks(vmi)
			networkName, foundNetworkName := vmiNets[nadKey.String()]
			if !foundNetworkName {
				log.V(5).Info("network name not found", "network name", networkName)
				podNetworkSelectionElements = append(podNetworkSelectionElements, *networkSelectionElement)
				continue
			}

			networkSelectionElement.IPAMClaimReference = fmt.Sprintf("%s.%s", vmName, networkName)
			podNetworkSelectionElements = append(podNetworkSelectionElements, *networkSelectionElement)
			hasChangedNetworkSelectionElements = true
			continue
		}
		podNetworkSelectionElements = append(podNetworkSelectionElements, *networkSelectionElement)
	}

	if len(podNetworkSelectionElements) > 0 {
		newPod, err := podWithUpdatedSelectionElements(pod, podNetworkSelectionElements)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, err)
		}

		if reflect.DeepEqual(newPod, pod) || !hasChangedNetworkSelectionElements {
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
		indexedSecondaryNetworks[network.Multus.NetworkName] = network.Name
	}

	return indexedSecondaryNetworks
}

func podWithUpdatedSelectionElements(pod *corev1.Pod, networks []v1.NetworkSelectionElement) (*corev1.Pod, error) {
	newPod := pod.DeepCopy()
	newNets, err := json.Marshal(networks)
	if err != nil {
		return nil, err
	}
	newPod.Annotations[v1.NetworkAttachmentAnnot] = string(newNets)
	return newPod, nil
}

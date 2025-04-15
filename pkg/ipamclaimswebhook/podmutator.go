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
	"github.com/kubevirt/ipam-extensions/pkg/udn"
)

// +kubebuilder:webhook:path=/mutate-v1-pod,mutating=true,failurePolicy=fail,groups="",resources=pods,verbs=create;update,versions=v1,name=ipam-claims.k8s.cni.cncf.io,admissionReviewVersions=v1,sideEffects=None

// IPAMClaimsValet annotates Pods
type IPAMClaimsValet struct {
	client.Client
	decoder admission.Decoder
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

	newPrimaryNetworkIPAMClaimName, err := findNewPrimaryNetworkIPAMClaimName(ctx, a.Client, pod, vmName)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError,
			fmt.Errorf("failed looking for primary user defined IPAMClaim name: %v", err))
	}
	if newPrimaryNetworkIPAMClaimName != "" {
		if newPod == nil {
			newPod = pod.DeepCopy()
		}
		updatePodWithOVNPrimaryNetworkIPAMClaimAnnotation(newPod, newPrimaryNetworkIPAMClaimName)
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

func updatePodWithOVNPrimaryNetworkIPAMClaimAnnotation(pod *corev1.Pod, primaryNetworkIPAMClaimName string) {
	pod.Annotations[config.OVNPrimaryNetworkIPAMClaimAnnotation] = primaryNetworkIPAMClaimName
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

func findNewPrimaryNetworkIPAMClaimName(ctx context.Context,
	cli client.Client, pod *corev1.Pod, vmName string) (string, error) {
	log := logf.FromContext(ctx)
	if pod.Annotations[config.OVNPrimaryNetworkIPAMClaimAnnotation] != "" {
		return "", nil
	}
	primaryNetworkNAD, err := udn.FindPrimaryNetwork(ctx, cli, pod.Namespace)
	if err != nil {
		return "", err
	}
	if primaryNetworkNAD == nil {
		return "", nil
	}
	pluginConfig, err := config.NewConfig(primaryNetworkNAD.Spec.Config)
	if err != nil {
		return "", err
	}

	if !pluginConfig.AllowPersistentIPs {
		return "", nil
	}

	log.Info(
		"will request primary network persistent IPs",
		"NAD", client.ObjectKeyFromObject(primaryNetworkNAD),
		"network", pluginConfig.Name,
	)
	vmKey := types.NamespacedName{Namespace: pod.Namespace, Name: vmName}
	vmi := &virtv1.VirtualMachineInstance{}
	if err := cli.Get(context.Background(), vmKey, vmi); err != nil {
		return "", err
	}

	networkName := vmiPodNetworkName(vmi)
	if networkName == "" {
		log.Info("vmi has no pod network primary UDN ipam claim will not be created", "vmi", vmKey.String())
		return "", nil
	}

	return claims.ComposeKey(vmi.Name, networkName), nil
}

// returns the name of the kubevirt VM pod network
func vmiPodNetworkName(vmi *virtv1.VirtualMachineInstance) string {
	for _, network := range vmi.Spec.Networks {
		if network.Pod != nil {
			return network.Name
		}
	}
	return ""
}

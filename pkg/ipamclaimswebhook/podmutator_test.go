package ipamclaimswebhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	"gomodules.xyz/jsonpatch/v2"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	virtv1 "kubevirt.io/api/core/v1"

	ipamclaimsapi "github.com/k8snetworkplumbingwg/ipamclaims/pkg/crd/ipamclaims/v1alpha1"
	nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	"github.com/kubevirt/ipam-extensions/pkg/config"
)

type testConfig struct {
	inputVM                   *virtv1.VirtualMachine
	inputVMI                  *virtv1.VirtualMachineInstance
	inputNADs                 []*nadv1.NetworkAttachmentDefinition
	inputPod                  *corev1.Pod
	expectedAdmissionResponse admissionv1.AdmissionResponse
	expectedAdmissionPatches  types.GomegaMatcher
}

func TestController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Webhook test suite")
}

var _ = Describe("KubeVirt IPAM launcher pod mutato machine", Serial, func() {
	var (
		patchType = admissionv1.PatchTypeJSONPatch
		testEnv   *envtest.Environment
	)

	BeforeEach(func() {
		log.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
		testEnv = &envtest.Environment{}
		_, err := testEnv.Start()
		Expect(err).NotTo(HaveOccurred())

		Expect(virtv1.AddToScheme(scheme.Scheme)).To(Succeed())
		Expect(nadv1.AddToScheme(scheme.Scheme)).To(Succeed())
		Expect(ipamclaimsapi.AddToScheme(scheme.Scheme)).To(Succeed())
		// +kubebuilder:scaffold:scheme
	})

	AfterEach(func() {
		Expect(testEnv.Stop()).To(Succeed())
	})

	const (
		nadName       = "ns1/supadupanet"
		vmName        = "vm1"
		namespaceName = "randomNS"
	)

	DescribeTable("admits / rejects pod creation requests as expected", func(config testConfig) {
		var (
			initialObjects []client.Object
		)

		if config.inputVM != nil {
			initialObjects = append(initialObjects, config.inputVM)
		}

		if config.inputVMI != nil {
			initialObjects = append(initialObjects, config.inputVMI)
		}

		for _, nad := range config.inputNADs {
			initialObjects = append(initialObjects, nad)
		}

		ctrlOptions := controllerruntime.Options{
			Scheme: scheme.Scheme,
			NewClient: func(_ *rest.Config, _ client.Options) (client.Client, error) {
				return fake.NewClientBuilder().
					WithScheme(scheme.Scheme).
					WithObjects(initialObjects...).
					Build(), nil
			},
		}

		mgr, err := controllerruntime.NewManager(&rest.Config{}, ctrlOptions)
		Expect(err).NotTo(HaveOccurred())

		ipamClaimsManager := NewIPAMClaimsValet(mgr, WithDefaultNetNADNamespace(namespaceName))

		result := ipamClaimsManager.Handle(context.Background(), podAdmissionRequest(config.inputPod))

		Expect(result.AdmissionResponse).To(Equal(config.expectedAdmissionResponse))
		if config.expectedAdmissionPatches != nil {
			Expect(result.Patches).To(config.expectedAdmissionPatches)
		}
	},
		Entry("pod not beloging to a VM and not requesting secondary "+
			"attachments and no primary user defined network is accepted", testConfig{
			inputVM:  dummyVM(nadName),
			inputVMI: dummyVMI(nadName),
			inputNADs: []*nadv1.NetworkAttachmentDefinition{
				dummyNAD(nadName),
			},
			inputPod: &corev1.Pod{},
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				Allowed: true,
				Result: &metav1.Status{
					Message: "not a VM",
					Code:    http.StatusOK,
				},
			},
		}),
		Entry("vm launcher pod with an attachment to a primary and secondary user "+
			"defined network with persistent IPs enabled requests an IPAMClaim", testConfig{
			inputVM:  dummyVM(nadName),
			inputVMI: dummyVMI(nadName),
			inputNADs: []*nadv1.NetworkAttachmentDefinition{
				dummyNAD(nadName),
				dummyPrimaryNetworkNAD(nadName),
			},
			inputPod: dummyPodForVM(nadName, vmName),
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				Allowed:   true,
				PatchType: &patchType,
			},
			expectedAdmissionPatches: ConsistOf([]jsonpatch.JsonPatchOperation{
				{
					Operation: "add",
					Path:      "/metadata/annotations/k8s.ovn.org~1primary-udn-ipamclaim",
					Value:     "vm1.podnet",
				},
				{
					Operation: "replace",
					Path:      "/metadata/annotations/k8s.v1.cni.cncf.io~1networks",
					Value:     "[{\"name\":\"supadupanet\",\"namespace\":\"ns1\",\"ipam-claim-reference\":\"vm1.randomnet\"}]",
				},
			}),
		}),
		Entry("vm launcher pod with primary user defined network defined "+
			"at namespace with persistent IPs enabled requests an IPAMClaim", testConfig{
			inputVM:  dummyVM(nadName),
			inputVMI: dummyVMI(nadName),
			inputNADs: []*nadv1.NetworkAttachmentDefinition{
				dummyPrimaryNetworkNAD(nadName),
			},
			inputPod: dummyPodForVM("" /*without network selection element*/, vmName),
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				Allowed:   true,
				PatchType: &patchType,
			},
			expectedAdmissionPatches: ConsistOf([]jsonpatch.JsonPatchOperation{
				{
					Operation: "add",
					Path:      "/metadata/annotations/k8s.ovn.org~1primary-udn-ipamclaim",
					Value:     "vm1.podnet",
				},
			}),
		}),
		Entry("vm launcher pod with a MAC address request for primary user defined network defined "+
			"at namespace with persistent IPs enabled requests an IPAMClaim", testConfig{
			inputVM:  dummyVM(nadName),
			inputVMI: dummyVMI(nadName, WithMACRequest("podnet", "02:03:04:05:06:07")),
			inputNADs: []*nadv1.NetworkAttachmentDefinition{
				dummyPrimaryNetworkNAD(nadName),
			},
			inputPod: dummyPodForVM("" /*without network selection element*/, vmName),
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				Allowed:   true,
				PatchType: &patchType,
			},
			expectedAdmissionPatches: ConsistOf([]jsonpatch.JsonPatchOperation{
				{
					Operation: "add",
					Path:      "/metadata/annotations/k8s.ovn.org~1primary-udn-ipamclaim",
					Value:     "vm1.podnet",
				},
				{
					Operation: "add",
					Path:      "/metadata/annotations/v1.multus-cni.io~1default-network",
					Value: "[{\"name\":\"default\",\"namespace\":\"randomNS\"," +
						"\"mac\":\"02:03:04:05:06:07\",\"ipam-claim-reference\":\"vm1.podnet\"}]",
				},
			}),
		}),
		Entry("vm launcher pod with requested MAC and IPs for primary user defined network defined "+
			"at namespace with persistent IPs enabled requests an IPAMClaim", testConfig{
			inputVM: dummyVM(nadName),
			inputVMI: dummyVMI(
				nadName,
				WithMACRequest("podnet", "02:03:04:05:06:07"),
				WithIPRequests("podnet", "192.168.1.10", "fd20:1234::200"),
			),
			inputNADs: []*nadv1.NetworkAttachmentDefinition{
				dummyPrimaryNetworkNAD(nadName),
			},
			inputPod: dummyPodForVM("" /*without network selection element*/, vmName),
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				Allowed:   true,
				PatchType: &patchType,
			},
			expectedAdmissionPatches: ConsistOf([]jsonpatch.JsonPatchOperation{
				{
					Operation: "add",
					Path:      "/metadata/annotations/k8s.ovn.org~1primary-udn-ipamclaim",
					Value:     "vm1.podnet",
				},
				{
					Operation: "add",
					Path:      "/metadata/annotations/v1.multus-cni.io~1default-network",
					Value: "[{\"name\":\"default\",\"namespace\":\"randomNS\"," +
						"\"ips\":[\"192.168.1.10/16\",\"fd20:1234::200/64\"]," +
						"\"mac\":\"02:03:04:05:06:07\",\"ipam-claim-reference\":\"vm1.podnet\"}]",
				},
			}),
		}),
		Entry("vm launcher pod with an attachment to a secondary user defined "+
			"network with persistent IPs enabled requests an IPAMClaim", testConfig{
			inputVM:  dummyVM(nadName),
			inputVMI: dummyVMI(nadName),
			inputNADs: []*nadv1.NetworkAttachmentDefinition{
				dummyNAD(nadName),
			},
			inputPod: dummyPodForVM(nadName, vmName),
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				Allowed:   true,
				PatchType: &patchType,
			},
			expectedAdmissionPatches: Equal([]jsonpatch.JsonPatchOperation{
				{
					Operation: "replace",
					Path:      "/metadata/annotations/k8s.v1.cni.cncf.io~1networks",
					Value:     "[{\"name\":\"supadupanet\",\"namespace\":\"ns1\",\"ipam-claim-reference\":\"vm1.randomnet\"}]",
				},
			}),
		}),
		Entry("vm launcher pod with an attachment to a network *without* persistentIPs is accepted", testConfig{
			inputVM:  dummyVM(nadName),
			inputVMI: dummyVMI(nadName),
			inputNADs: []*nadv1.NetworkAttachmentDefinition{
				dummyNADWithoutPersistentIPs(nadName),
			},
			inputPod: dummyPodForVM(nadName, vmName),
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				Allowed: true,
				Result: &metav1.Status{
					Message: "carry on",
					Code:    http.StatusOK,
				},
			},
		}),
		Entry("pod not belonging to a VM with an attachment to a network with persistent IPs enabled is accepted", testConfig{
			inputNADs: []*nadv1.NetworkAttachmentDefinition{
				dummyNAD(nadName),
			},
			inputPod: dummyPod(nadName),
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				Allowed: true,
				Result: &metav1.Status{
					Message: "not a VM",
					Code:    http.StatusOK,
				},
			},
		}),
		Entry("pod requesting an attachment via a NAD with an invalid configuration throws a BAD REQUEST", testConfig{
			inputVM:  dummyVM(nadName),
			inputVMI: dummyVMI(nadName),
			inputNADs: []*nadv1.NetworkAttachmentDefinition{
				dummyNAD(nadName),
			},
			inputPod: dummyPodForVM("{not json}", vmName),
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				Allowed: false,
				Result: &metav1.Status{
					Message: "failed to parse pod network selection elements",
					Code:    http.StatusBadRequest,
				},
			},
		}),
		Entry("pod requesting an attachment via a NAD with an invalid configuration throws a BAD REQUEST", testConfig{
			inputVM:  dummyVM(nadName),
			inputVMI: dummyVMI(nadName),
			inputPod: dummyPodForVM(nadName, vmName),
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				Allowed: true,
				Result: &metav1.Status{
					Message: "carry on",
					Code:    http.StatusOK,
				},
			},
		}),
		Entry("launcher pod whose VMI is not found throws a server error", testConfig{
			inputNADs: []*nadv1.NetworkAttachmentDefinition{
				dummyNAD(nadName),
			},
			inputPod: dummyPodForVM(nadName, vmName),
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				Allowed: false,
				Result: &metav1.Status{
					Message: "virtualmachineinstances.kubevirt.io \"vm1\" not found",
					Code:    http.StatusInternalServerError,
				},
			},
		}),
	)
})

func dummyVM(nadName string) *virtv1.VirtualMachine {
	return &virtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm1",
			Namespace: "ns1",
		},
		Spec: virtv1.VirtualMachineSpec{
			Template: &virtv1.VirtualMachineInstanceTemplateSpec{
				Spec: dummyVMISpec(nadName),
			},
		},
	}
}

func dummyVMI(nadName string, opts ...VMCreationOptions) *virtv1.VirtualMachineInstance {
	vmi := &virtv1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm1",
			Namespace: "ns1",
		},
		Spec: dummyVMISpec(nadName),
	}

	for _, opt := range opts {
		if err := opt(vmi); err != nil {
			panic(err)
		}
	}

	return vmi
}

func dummyVMISpec(nadName string) virtv1.VirtualMachineInstanceSpec {
	return virtv1.VirtualMachineInstanceSpec{
		Domain: virtv1.DomainSpec{
			Devices: virtv1.Devices{
				Interfaces: []virtv1.Interface{
					{
						Name: "podnet",
					},
				},
			},
		},
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

func dummyNADWithConfig(nadName string, config string) *nadv1.NetworkAttachmentDefinition {
	namespaceAndName := strings.Split(nadName, "/")
	return &nadv1.NetworkAttachmentDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespaceAndName[0],
			Name:      namespaceAndName[1],
		},
		Spec: nadv1.NetworkAttachmentDefinitionSpec{
			Config: config,
		},
	}
}

func dummyNAD(nadName string) *nadv1.NetworkAttachmentDefinition {
	return dummyNADWithConfig(nadName, `{"name": "goodnet", "allowPersistentIPs": true}`)
}

func dummyPrimaryNetworkNAD(nadName string) *nadv1.NetworkAttachmentDefinition {
	return dummyNADWithConfig(nadName+"primary", `
{
	"name": "primarynet",
	"role": "primary",
	"allowPersistentIPs": true,
	"subnets": "192.168.0.0/16,fd12:1234::123/64"
}`)
}
func dummyNADWithoutPersistentIPs(nadName string) *nadv1.NetworkAttachmentDefinition {
	return dummyNADWithConfig(nadName, `{"name": "goodnet"}`)
}

func podAdmissionRequest(pod *corev1.Pod) admission.Request {
	rawPod, err := json.Marshal(pod)
	if err != nil {
		return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Object: runtime.RawExtension{}}}
	}
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Object: runtime.RawExtension{
				Raw: rawPod,
			},
		},
	}
}

func dummyPodForVM(nadName string, vmName string) *corev1.Pod {
	return pod(nadName, map[string]string{
		"kubevirt.io/domain": vmName,
	})
}

func dummyPod(nadName string) *corev1.Pod {
	return pod(nadName, nil)
}

func pod(nadName string, annotations map[string]string) *corev1.Pod {
	baseAnnotations := map[string]string{}
	if nadName != "" {
		baseAnnotations[nadv1.NetworkAttachmentAnnot] = nadName
	}
	for k, v := range annotations {
		baseAnnotations[k] = v
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "pod1",
			Namespace:   "ns1",
			Annotations: baseAnnotations,
		},
	}
}

type VMCreationOptions func(*virtv1.VirtualMachineInstance) error

func WithIPRequests(logicalNetworkName string, ips ...string) VMCreationOptions {
	return func(vm *virtv1.VirtualMachineInstance) error {
		currentLogicalNetsAddrs := map[string][]string{}
		rawCurrentLogicalNetsAddrs, isAnnotationPresent := vm.Annotations[config.IPRequestsAnnotation]
		if !isAnnotationPresent {
			currentLogicalNetsAddrs = map[string][]string{logicalNetworkName: ips}
		} else {
			if err := json.Unmarshal([]byte(rawCurrentLogicalNetsAddrs), &currentLogicalNetsAddrs); err != nil {
				return fmt.Errorf("failed to unmarshal current logical nets addrs: %w", err)
			}
			currentLogicalNetsAddrs[logicalNetworkName] = ips
		}

		rawVMAddrsRequest, err := json.Marshal(currentLogicalNetsAddrs)
		if err != nil {
			return fmt.Errorf("failed to marshal current logical nets addrs: %w", err)
		}
		if vm.Annotations == nil {
			vm.Annotations = map[string]string{config.IPRequestsAnnotation: string(rawVMAddrsRequest)}
		}
		return nil
	}
}

func WithMACRequest(logicalNetworkName string, macAddress string) VMCreationOptions {
	return func(vm *virtv1.VirtualMachineInstance) error {
		for i, iface := range vm.Spec.Domain.Devices.Interfaces {
			if iface.Name == logicalNetworkName {
				vm.Spec.Domain.Devices.Interfaces[i].MacAddress = macAddress
				return nil
			}
		}
		return fmt.Errorf("interface %q not found", logicalNetworkName)
	}
}

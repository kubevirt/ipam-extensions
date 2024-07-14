package ipamclaimswebhook

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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

	"github.com/kubevirt/ipam-extensions/pkg/testobjects"

	ipamclaimsapi "github.com/k8snetworkplumbingwg/ipamclaims/pkg/crd/ipamclaims/v1alpha1"
	nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
)

type testConfig struct {
	inputVM                   *virtv1.VirtualMachine
	inputVMI                  *virtv1.VirtualMachineInstance
	inputNAD                  *nadv1.NetworkAttachmentDefinition
	inputPod                  *corev1.Pod
	expectedAdmissionResponse admission.Response
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
		nadName = "ns1/supadupanet"
		vmName  = "vm1"
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

		if config.inputNAD != nil {
			initialObjects = append(initialObjects, config.inputNAD)
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

		ipamClaimsManager := NewIPAMClaimsValet(mgr)

		Expect(
			ipamClaimsManager.Handle(context.Background(), podAdmissionRequest(config.inputPod)),
		).To(
			Equal(config.expectedAdmissionResponse),
		)
	},
		Entry("pod not requesting secondary attachments is accepted", testConfig{
			inputVM:  testobjects.DummyVM(nadName),
			inputVMI: testobjects.DummyVMI(nadName),
			inputNAD: testobjects.DummyNAD(nadName),
			inputPod: &corev1.Pod{},
			expectedAdmissionResponse: admission.Response{
				AdmissionResponse: admissionv1.AdmissionResponse{
					Allowed: true,
					Result: &metav1.Status{
						Message: "no secondary networks requested",
						Code:    http.StatusOK,
					},
				},
			},
		}),
		Entry("vm launcher pod with an attachment to a network with persistent IPs enabled requests an IPAMClaim", testConfig{
			inputVM:  testobjects.DummyVM(nadName),
			inputVMI: testobjects.DummyVMI(nadName),
			inputNAD: testobjects.DummyNAD(nadName),
			inputPod: dummyPodForVM(nadName, vmName),
			expectedAdmissionResponse: admission.Response{
				AdmissionResponse: admissionv1.AdmissionResponse{
					Allowed:   true,
					PatchType: &patchType,
				},
				Patches: []jsonpatch.JsonPatchOperation{
					{
						Operation: "replace",
						Path:      "/metadata/annotations/k8s.v1.cni.cncf.io~1networks",
						Value:     "[{\"name\":\"supadupanet\",\"namespace\":\"ns1\",\"ipam-claim-reference\":\"vm1.randomnet\"}]",
					},
				},
			},
		}),
		Entry("vm launcher pod with an attachment to a network *without* persistentIPs is accepted", testConfig{
			inputVM:  testobjects.DummyVM(nadName),
			inputVMI: testobjects.DummyVMI(nadName),
			inputNAD: dummyNADWithoutPersistentIPs(nadName),
			inputPod: dummyPodForVM(nadName, vmName),
			expectedAdmissionResponse: admission.Response{
				AdmissionResponse: admissionv1.AdmissionResponse{
					Allowed: true,
					Result: &metav1.Status{
						Message: "mutation not needed",
						Code:    http.StatusOK,
					},
				},
			},
		}),
		Entry("pod not belonging to a VM with an attachment to a network with persistent IPs enabled is accepted", testConfig{
			inputNAD: testobjects.DummyNAD(nadName),
			inputPod: dummyPod(nadName),
			expectedAdmissionResponse: admission.Response{
				AdmissionResponse: admissionv1.AdmissionResponse{
					Allowed: true,
					Result: &metav1.Status{
						Message: "not a VM",
						Code:    http.StatusOK,
					},
				},
			},
		}),
		Entry("pod requesting an attachment via a NAD with an invalid configuration throws a BAD REQUEST", testConfig{
			inputVM:  testobjects.DummyVM(nadName),
			inputVMI: testobjects.DummyVMI(nadName),
			inputNAD: testobjects.DummyNAD(nadName),
			inputPod: pod("{not json}", nil),
			expectedAdmissionResponse: admission.Response{
				AdmissionResponse: admissionv1.AdmissionResponse{
					Allowed: false,
					Result: &metav1.Status{
						Message: "failed to parse pod network selection elements",
						Code:    http.StatusBadRequest,
					},
				},
			},
		}),
		Entry("pod requesting an attachment via a NAD with an invalid configuration throws a BAD REQUEST", testConfig{
			inputVM:  testobjects.DummyVM(nadName),
			inputVMI: testobjects.DummyVMI(nadName),
			inputPod: dummyPodForVM(nadName, vmName),
			expectedAdmissionResponse: admission.Response{
				AdmissionResponse: admissionv1.AdmissionResponse{
					Allowed: true,
					Result: &metav1.Status{
						Message: "NAD not found, will hang on scheduler",
						Code:    http.StatusOK,
					},
				},
			},
		}),
		Entry("launcher pod whose VMI is not found throws a server error", testConfig{
			inputNAD: testobjects.DummyNAD(nadName),
			inputPod: dummyPodForVM(nadName, vmName),
			expectedAdmissionResponse: admission.Response{
				AdmissionResponse: admissionv1.AdmissionResponse{
					Allowed: false,
					Result: &metav1.Status{
						Message: "virtualmachineinstances.kubevirt.io \"vm1\" not found",
						Code:    http.StatusInternalServerError,
					},
				},
			},
		}),
	)
})

func dummyNADWithoutPersistentIPs(nadName string) *nadv1.NetworkAttachmentDefinition {
	namespaceAndName := strings.Split(nadName, "/")
	return &nadv1.NetworkAttachmentDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespaceAndName[0],
			Name:      namespaceAndName[1],
		},
		Spec: nadv1.NetworkAttachmentDefinitionSpec{
			Config: `{"name": "goodnet"}`,
		},
	}
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
	baseAnnotations := map[string]string{
		nadv1.NetworkAttachmentAnnot: nadName,
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

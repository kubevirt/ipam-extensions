package vminetworkscontroller_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	virtv1 "kubevirt.io/api/core/v1"

	"github.com/kubevirt/ipam-extensions/pkg/vminetworkscontroller"

	ipamclaimsapi "github.com/k8snetworkplumbingwg/ipamclaims/pkg/crd/ipamclaims/v1alpha1"
	nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	"github.com/kubevirt/ipam-extensions/pkg/claims"
)

func TestController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "VMI Controller test suite")
}

var (
	testEnv *envtest.Environment
)

type testConfig struct {
	inputVM            *virtv1.VirtualMachine
	inputVMI           *virtv1.VirtualMachineInstance
	inputNAD           *nadv1.NetworkAttachmentDefinition
	existingIPAMClaim  *ipamclaimsapi.IPAMClaim
	expectedError      error
	expectedResponse   reconcile.Result
	expectedIPAMClaims []ipamclaimsapi.IPAMClaim
}

var _ = Describe("VMI IPAM controller", Serial, func() {
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
		nadName       = "ns1/superdupernad"
		namespace     = "ns1"
		vmName        = "vm1"
		dummyUID      = "dummyUID"
		unexpectedUID = "unexpectedUID"
	)

	DescribeTable("reconcile behavior is as expected", func(config testConfig) {
		var initialObjects []client.Object

		if config.inputVM != nil {
			initialObjects = append(initialObjects, config.inputVM)
		}

		var vmiKey apitypes.NamespacedName
		if config.inputVMI != nil {
			vmiKey = apitypes.NamespacedName{
				Namespace: config.inputVMI.Namespace,
				Name:      config.inputVMI.Name,
			}
			initialObjects = append(initialObjects, config.inputVMI)
		}

		if config.inputNAD != nil {
			initialObjects = append(initialObjects, config.inputNAD)
		}

		if config.existingIPAMClaim != nil {
			initialObjects = append(initialObjects, config.existingIPAMClaim)
		}

		if vmiKey.Namespace == "" && vmiKey.Name == "" {
			// must apply some default for the VMI DEL scenarios ...
			vmiKey = apitypes.NamespacedName{
				Namespace: namespace,
				Name:      vmName,
			}
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

		reconcileVMI := vminetworkscontroller.NewVMIReconciler(mgr)
		if config.expectedError != nil {
			_, err := reconcileVMI.Reconcile(context.Background(), controllerruntime.Request{NamespacedName: vmiKey})
			Expect(err).To(MatchError(config.expectedError))
		} else {
			Expect(
				reconcileVMI.Reconcile(context.Background(), controllerruntime.Request{NamespacedName: vmiKey}),
			).To(Equal(config.expectedResponse))
		}

		if len(config.expectedIPAMClaims) > 0 {
			ipamClaimList := &ipamclaimsapi.IPAMClaimList{}

			Expect(mgr.GetClient().List(context.Background(), ipamClaimList, claims.OwnedByVMLabel(vmName))).To(Succeed())
			Expect(ipamClaimsCleaner(ipamClaimList.Items...)).To(ConsistOf(config.expectedIPAMClaims))
		}
	},
		Entry("when the VM has an associated VMI pointing to an existing NAD", testConfig{
			inputVM:          dummyVM(nadName),
			inputVMI:         dummyVMI(nadName),
			inputNAD:         dummyNAD(nadName),
			expectedResponse: reconcile.Result{},
			expectedIPAMClaims: []ipamclaimsapi.IPAMClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:       fmt.Sprintf("%s.%s", vmName, "randomnet"),
						Namespace:  namespace,
						Finalizers: []string{claims.KubevirtVMFinalizer},
						Labels:     claims.OwnedByVMLabel(vmName),
						OwnerReferences: []metav1.OwnerReference{{
							Name:               vmName,
							Controller:         ptr.To(true),
							BlockOwnerDeletion: ptr.To(true)},
						},
					},
					Spec: ipamclaimsapi.IPAMClaimSpec{Network: "goodnet"},
				},
			},
		}),
		Entry("when the VM has an associated VMI pointing to an existing NAD with an improper config", testConfig{
			inputVM:       dummyVM(nadName),
			inputVMI:      dummyVMI(nadName),
			inputNAD:      dummyNADWrongFormat(nadName),
			expectedError: fmt.Errorf("failed to extract the relevant NAD information"),
		}),
		Entry("the associated VMI exists but points to a NAD that doesn't exist", testConfig{
			inputVM:  dummyVM(nadName),
			inputVMI: dummyVMI(nadName),
			expectedError: &errors.StatusError{
				ErrStatus: metav1.Status{
					Status:  "Failure",
					Message: "networkattachmentdefinitions.k8s.cni.cncf.io \"superdupernad\" not found",
					Reason:  "NotFound",
					Details: &metav1.StatusDetails{
						Name:  "superdupernad",
						Group: "k8s.cni.cncf.io",
						Kind:  "networkattachmentdefinitions",
					},
					Code: 404,
				},
			},
		}),
		Entry("the VMI does not exist on the datastore - it might have been deleted in the meantime", testConfig{
			expectedResponse: reconcile.Result{},
		}),
		Entry("the VMI was deleted, thus the existing IPAMClaims finalizers must be removed", testConfig{
			expectedResponse: reconcile.Result{},
			existingIPAMClaim: &ipamclaimsapi.IPAMClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:       fmt.Sprintf("%s.%s", vmName, "randomnet"),
					Namespace:  namespace,
					Finalizers: []string{claims.KubevirtVMFinalizer},
					Labels:     claims.OwnedByVMLabel(vmName),
				},
				Spec: ipamclaimsapi.IPAMClaimSpec{Network: "doesitmatter?"},
			},
			expectedIPAMClaims: []ipamclaimsapi.IPAMClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vm1.randomnet",
						Namespace: "ns1",
						Labels:    claims.OwnedByVMLabel(vmName),
					},
					Spec: ipamclaimsapi.IPAMClaimSpec{Network: "doesitmatter?"},
				},
			},
		}),
		Entry("everything is OK but there's already an IPAMClaim with this name", testConfig{
			inputVM:  dummyVM(nadName),
			inputVMI: dummyVMI(nadName),
			inputNAD: dummyNAD(nadName),
			existingIPAMClaim: &ipamclaimsapi.IPAMClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s.%s", vmName, "randomnet"),
					Namespace: namespace,
				},
				Spec: ipamclaimsapi.IPAMClaimSpec{Network: "doesitmatter?"},
			},
			expectedError: fmt.Errorf("failed since it found an existing IPAMClaim for \"vm1.randomnet\""),
		}),
		Entry("found an existing IPAMClaim for the same VM", testConfig{
			inputVM:  decorateVMWithUID(dummyUID, dummyVM(nadName)),
			inputVMI: dummyVMI(nadName),
			inputNAD: dummyNAD(nadName),
			existingIPAMClaim: &ipamclaimsapi.IPAMClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s.%s", vmName, "randomnet"),
					Namespace: namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "v1",
							Kind:       "virtualmachines",
							Name:       "vm1",
							UID:        dummyUID,
						},
					},
					Labels:     claims.OwnedByVMLabel(vmName),
					Finalizers: []string{claims.KubevirtVMFinalizer},
				},
				Spec: ipamclaimsapi.IPAMClaimSpec{Network: "doesitmatter?"},
			},
			expectedResponse: reconcile.Result{},
			expectedIPAMClaims: []ipamclaimsapi.IPAMClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vm1.randomnet",
						Namespace: "ns1",
						Labels:    claims.OwnedByVMLabel(vmName),
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "v1",
								Kind:       "virtualmachines",
								Name:       "vm1",
								UID:        dummyUID,
							},
						},
						Finalizers: []string{claims.KubevirtVMFinalizer},
					},
					Spec: ipamclaimsapi.IPAMClaimSpec{Network: "doesitmatter?"},
				},
			},
		}),
		Entry("found an existing IPAMClaim for a VM with same name but different UID", testConfig{
			inputVM:  decorateVMWithUID(dummyUID, dummyVM(nadName)),
			inputVMI: dummyVMI(nadName),
			inputNAD: dummyNAD(nadName),
			existingIPAMClaim: &ipamclaimsapi.IPAMClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s.%s", vmName, "randomnet"),
					Namespace: namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "v1",
							Kind:       "virtualmachines",
							Name:       "vm1",
							UID:        unexpectedUID,
						},
					},
					Labels:     claims.OwnedByVMLabel(vmName),
					Finalizers: []string{claims.KubevirtVMFinalizer},
				},
				Spec: ipamclaimsapi.IPAMClaimSpec{Network: "doesitmatter?"},
			},
			expectedError: fmt.Errorf("failed since it found an existing IPAMClaim for \"vm1.randomnet\""),
		}),
		Entry("a lonesome VMI (with no corresponding VM) is a valid migration use-case", testConfig{
			inputVMI:         dummyVMI(nadName),
			inputNAD:         dummyNAD(nadName),
			expectedResponse: reconcile.Result{},
			expectedIPAMClaims: []ipamclaimsapi.IPAMClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "vm1.randomnet",
						Namespace:  "ns1",
						Labels:     claims.OwnedByVMLabel(vmName),
						Finalizers: []string{claims.KubevirtVMFinalizer},
						OwnerReferences: []metav1.OwnerReference{{
							Name:               vmName,
							Controller:         ptr.To(true),
							BlockOwnerDeletion: ptr.To(true)},
						},
					},
					Spec: ipamclaimsapi.IPAMClaimSpec{Network: "goodnet"},
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

func dummyVMI(nadName string) *virtv1.VirtualMachineInstance {
	return &virtv1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm1",
			Namespace: "ns1",
		},
		Spec: dummyVMISpec(nadName),
	}
}

func dummyVMISpec(nadName string) virtv1.VirtualMachineInstanceSpec {
	return virtv1.VirtualMachineInstanceSpec{
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

func dummyNAD(nadName string) *nadv1.NetworkAttachmentDefinition {
	namespaceAndName := strings.Split(nadName, "/")
	return &nadv1.NetworkAttachmentDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespaceAndName[0],
			Name:      namespaceAndName[1],
		},
		Spec: nadv1.NetworkAttachmentDefinitionSpec{
			Config: `{"name": "goodnet", "allowPersistentIPs": true}`,
		},
	}
}

func dummyNADWrongFormat(nadName string) *nadv1.NetworkAttachmentDefinition {
	namespaceAndName := strings.Split(nadName, "/")
	return &nadv1.NetworkAttachmentDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespaceAndName[0],
			Name:      namespaceAndName[1],
		},
		Spec: nadv1.NetworkAttachmentDefinitionSpec{
			Config: "this is not JSON !!!",
		},
	}
}

func ipamClaimsCleaner(ipamClaims ...ipamclaimsapi.IPAMClaim) []ipamclaimsapi.IPAMClaim {
	for i := range ipamClaims {
		ipamClaims[i].ObjectMeta.ResourceVersion = ""
	}
	return ipamClaims
}

func decorateVMWithUID(uid string, vm *virtv1.VirtualMachine) *virtv1.VirtualMachine {
	vm.UID = apitypes.UID(uid)
	return vm
}

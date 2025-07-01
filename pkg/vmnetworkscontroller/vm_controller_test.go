package vmnetworkscontroller_test

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	virtv1 "kubevirt.io/api/core/v1"

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

	ipamclaimsapi "github.com/k8snetworkplumbingwg/ipamclaims/pkg/crd/ipamclaims/v1alpha1"
	nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	"github.com/kubevirt/ipam-extensions/pkg/claims"
	"github.com/kubevirt/ipam-extensions/pkg/vmnetworkscontroller"
)

func TestController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "VM Controller test suite")
}

type testConfig struct {
	inputVM            *virtv1.VirtualMachine
	inputVMI           *virtv1.VirtualMachineInstance
	existingIPAMClaim  *ipamclaimsapi.IPAMClaim
	expectedError      error
	expectedResponse   reconcile.Result
	expectedIPAMClaims []ipamclaimsapi.IPAMClaim
}

var (
	testEnv *envtest.Environment
)

var _ = Describe("VM IPAM controller", Serial, func() {
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
		nadName   = "ns1/superdupernad"
		namespace = "ns1"
		vmName    = "vm1"
	)

	DescribeTable("reconcile behavior is as expected", func(config testConfig) {
		var initialObjects []client.Object

		var vmKey apitypes.NamespacedName
		if config.inputVM != nil {
			vmKey = apitypes.NamespacedName{
				Namespace: config.inputVM.Namespace,
				Name:      config.inputVM.Name,
			}
			initialObjects = append(initialObjects, config.inputVM)
		}

		if config.inputVMI != nil {
			initialObjects = append(initialObjects, config.inputVMI)
		}

		if config.existingIPAMClaim != nil {
			initialObjects = append(initialObjects, config.existingIPAMClaim)
		}

		if vmKey.Namespace == "" && vmKey.Name == "" {
			vmKey = apitypes.NamespacedName{
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

		reconcileVM := vmnetworkscontroller.NewVMReconciler(mgr)
		if config.expectedError != nil {
			_, err := reconcileVM.Reconcile(context.Background(), controllerruntime.Request{NamespacedName: vmKey})
			Expect(err).To(MatchError(config.expectedError))
		} else {
			Expect(
				reconcileVM.Reconcile(context.Background(), controllerruntime.Request{NamespacedName: vmKey}),
			).To(Equal(config.expectedResponse))
		}

		if len(config.expectedIPAMClaims) > 0 {
			ipamClaimList := &ipamclaimsapi.IPAMClaimList{}

			Expect(mgr.GetClient().List(context.Background(), ipamClaimList, claims.OwnedByVMLabel(vmName))).To(Succeed())
			Expect(ipamClaimsCleaner(ipamClaimList.Items...)).To(ConsistOf(config.expectedIPAMClaims))
		}
	},
		Entry("when the VM is marked for deletion and its VMI is gone", testConfig{
			inputVM:           dummyMarkedForDeletionVM(nadName),
			inputVMI:          nil,
			existingIPAMClaim: dummyIPAMClaimWithFinalizer(namespace, vmName),
			expectedResponse:  reconcile.Result{},
			expectedIPAMClaims: []ipamclaimsapi.IPAMClaim{
				*dummyIPAMClaimmWithoutFinalizer(namespace, vmName),
			},
		}),
		Entry("when the VM is gone", testConfig{
			inputVM:           nil,
			inputVMI:          nil,
			existingIPAMClaim: dummyIPAMClaimWithFinalizer(namespace, vmName),
			expectedResponse:  reconcile.Result{},
			expectedIPAMClaims: []ipamclaimsapi.IPAMClaim{
				*dummyIPAMClaimmWithoutFinalizer(namespace, vmName),
			},
		}),
		Entry("when the VM is marked for deletion and its VMI still exist", testConfig{
			inputVM:           dummyMarkedForDeletionVM(nadName),
			inputVMI:          dummyVMI(nadName),
			existingIPAMClaim: dummyIPAMClaimWithFinalizer(namespace, vmName),
			expectedResponse:  reconcile.Result{},
			expectedIPAMClaims: []ipamclaimsapi.IPAMClaim{
				*dummyIPAMClaimWithFinalizer(namespace, vmName),
			},
		}),
	)
})

func dummyMarkedForDeletionVM(nadName string) *virtv1.VirtualMachine {
	vm := dummyVM(nadName)
	vm.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	vm.ObjectMeta.Finalizers = []string{metav1.FinalizerDeleteDependents}

	return vm
}

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

func ipamClaimsCleaner(ipamClaims ...ipamclaimsapi.IPAMClaim) []ipamclaimsapi.IPAMClaim {
	for i := range ipamClaims {
		ipamClaims[i].ObjectMeta.ResourceVersion = ""
	}
	return ipamClaims
}

func dummyIPAMClaimWithFinalizer(namespace, vmName string) *ipamclaimsapi.IPAMClaim {
	ipamClaim := dummyIPAMClaimmWithoutFinalizer(namespace, vmName)
	ipamClaim.Finalizers = []string{claims.KubevirtVMFinalizer}
	return ipamClaim
}

func dummyIPAMClaimmWithoutFinalizer(namespace, vmName string) *ipamclaimsapi.IPAMClaim {
	return &ipamclaimsapi.IPAMClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claims.ComposeKey(vmName, "randomnet"),
			Namespace: namespace,
			Labels:    claims.OwnedByVMLabel(vmName),
			OwnerReferences: []metav1.OwnerReference{{
				Name:               vmName,
				Kind:               "VirtualMachine",
				Controller:         ptr.To(true),
				BlockOwnerDeletion: ptr.To(true),
			}},
		},
		Spec: ipamclaimsapi.IPAMClaimSpec{
			Network: "goodnet",
		},
	}
}

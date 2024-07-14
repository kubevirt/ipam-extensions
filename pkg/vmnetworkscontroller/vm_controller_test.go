package vmnetworkscontroller_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	virtv1 "kubevirt.io/api/core/v1"

	apitypes "k8s.io/apimachinery/pkg/types"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

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
	"github.com/kubevirt/ipam-extensions/pkg/testobjects"
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
			Expect(testobjects.IpamClaimsCleaner(ipamClaimList.Items...)).To(ConsistOf(config.expectedIPAMClaims))
		}
	},
		Entry("when the VM is marked for deletion and its VMI is gone", testConfig{
			inputVM:           testobjects.DummyMarkedForDeletionVM(nadName),
			inputVMI:          nil,
			existingIPAMClaim: testobjects.DummyIPAMClaimWithFinalizer(namespace, vmName),
			expectedResponse:  reconcile.Result{},
			expectedIPAMClaims: []ipamclaimsapi.IPAMClaim{
				*testobjects.DummyIPAMClaimWithoutFinalizer(namespace, vmName),
			},
		}),
		Entry("when the VM is gone", testConfig{
			inputVM:           nil,
			inputVMI:          nil,
			existingIPAMClaim: testobjects.DummyIPAMClaimWithFinalizer(namespace, vmName),
			expectedResponse:  reconcile.Result{},
			expectedIPAMClaims: []ipamclaimsapi.IPAMClaim{
				*testobjects.DummyIPAMClaimWithoutFinalizer(namespace, vmName),
			},
		}),
		Entry("when the VM is marked for deletion and its VMI still exist", testConfig{
			inputVM:           testobjects.DummyMarkedForDeletionVM(nadName),
			inputVMI:          testobjects.DummyVMI(nadName),
			existingIPAMClaim: testobjects.DummyIPAMClaimWithFinalizer(namespace, vmName),
			expectedResponse:  reconcile.Result{},
			expectedIPAMClaims: []ipamclaimsapi.IPAMClaim{
				*testobjects.DummyIPAMClaimWithFinalizer(namespace, vmName),
			},
		}),
	)
})

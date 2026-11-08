package claimcontroller_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gomegatypes "github.com/onsi/gomega/types"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	ipamclaimsapi "github.com/k8snetworkplumbingwg/ipamclaims/pkg/crd/ipamclaims/v1alpha1"
	virtv1 "kubevirt.io/api/core/v1"

	"github.com/kubevirt/ipam-extensions/pkg/claimcontroller"
)

func TestClaimRelease(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Claims Release Suite")
}

var scheme = runtime.NewScheme()

func init() {
	_ = ipamclaimsapi.AddToScheme(scheme)
	_ = virtv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
}

type managerStub struct {
	controllerruntime.Manager
	ctl client.Client
}

func (m managerStub) GetClient() client.Client {
	return m.ctl
}

var _ = Describe("Reconcile", func() {
	const ns = "test"
	const anotherNS = "another-test"

	var (
		blueVMKey   = types.NamespacedName{Namespace: ns, Name: "blueVM"}
		blueVMIKey  = types.NamespacedName{Namespace: ns, Name: "blueVMI"}
		redVMIKey   = types.NamespacedName{Namespace: ns, Name: "redVMI"}
		redVMKey    = types.NamespacedName{Namespace: ns, Name: "redVM"}
		greenVMKey  = types.NamespacedName{Namespace: ns, Name: "greenVM"}
		orangeVMKey = types.NamespacedName{Namespace: anotherNS, Name: "orangeVM"}
		ufoKey      = types.NamespacedName{Namespace: anotherNS, Name: "ufo"}
	)

	type testData struct {
		input              types.NamespacedName
		expectationMatcher gomegatypes.GomegaMatcher
	}

	testReconcile := func(ctl client.Client, tds ...testData) {
		GinkgoHelper()
		ctrl := claimcontroller.NewClaimReconciler(managerStub{ctl: ctl})
		for _, td := range tds {
			req := controllerruntime.Request{NamespacedName: td.input}
			_, err := ctrl.Reconcile(context.Background(), req)
			Expect(err).To(td.expectationMatcher)
		}
	}

	It("should succeed when the claim does not exist", func() {
		ctl := newFakeClient(interceptor.Funcs{})
		key := types.NamespacedName{Namespace: ns, Name: "not-exist-claim"}

		testReconcile(ctl, testData{key, Succeed()})
	})

	It("should fail when getting the claim fails", func() {
		want := errors.New("get failure")
		ctl := newFakeClient(injectGetError(want))

		testReconcile(ctl, testData{blueVMKey, MatchError(want)})
	})

	It("should succeed when no terminating claim exist", func() {
		ctl := newFakeClient(interceptor.Funcs{},
			newClaim(blueVMKey, newVMOwnerRef(blueVMKey.Name, "9000")),
			newClaim(redVMKey, newVMIOwnerRef(redVMKey.Name, "8000")),
		)

		testReconcile(ctl,
			testData{blueVMKey, Succeed()},
			testData{redVMKey, Succeed()},
		)

		assertClaimKubevirtVMFinalizer(ctl, blueVMKey)
		assertClaimKubevirtVMFinalizer(ctl, redVMKey)
	})

	It("should fail when get VM owner reference object fails", func() {
		want := errors.New("get error")
		ctl := newFakeClient(
			injectGetError(want),
			newTerminatingClaim(blueVMKey, newVMOwnerRef(blueVMKey.Name, "9000")),
		)

		testReconcile(ctl,
			testData{blueVMKey, MatchError(want)},
		)
	})

	It("should fail when patch orphan claim (remove KV finalizer) fails", func() {
		want := errors.New("patch error")
		ctl := newFakeClient(injectPatchError(want),
			newTerminatingClaim(blueVMKey, newVMOwnerRef(blueVMKey.Name, "9000")),
		)

		testReconcile(ctl,
			testData{blueVMKey, MatchError(want)},
		)

		assertClaimKubevirtVMFinalizer(ctl, blueVMKey)
	})

	It("should release terminating claims owned by a VM that no longer exist", func() {
		ctl := newFakeClient(interceptor.Funcs{},
			newTerminatingClaim(blueVMKey, newVMOwnerRef(blueVMKey.Name, "9000")),
			newTerminatingClaim(redVMIKey, newVMIOwnerRef(redVMIKey.Name, "8000")),

			newClaim(greenVMKey, newVMIOwnerRef(greenVMKey.Name, "10000")),

			newVM(orangeVMKey, "20000"),
			newTerminatingClaim(orangeVMKey, newVMOwnerRef(orangeVMKey.Name, "20000")),
		)

		testReconcile(ctl,
			testData{blueVMKey, Succeed()},
			testData{redVMIKey, Succeed()},
			testData{greenVMKey, Succeed()},
			testData{orangeVMKey, Succeed()},
		)

		assertClaimAbsent(ctl, blueVMKey)
		assertClaimAbsent(ctl, redVMIKey)
		assertClaimKubevirtVMFinalizer(ctl, greenVMKey)
		assertClaimKubevirtVMFinalizer(ctl, orangeVMKey)
	})

	It("should not touch finalizers other then VM persistent IPs finalizer", func() {
		claim := newTerminatingClaim(blueVMKey, newVMOwnerRef(blueVMKey.Name, "9000"))
		Expect(claim.Finalizers).To(ConsistOf("kubevirt.io/persistent-ipam"))
		claim.Finalizers = append(claim.Finalizers, "kubernetes")

		ctl := newFakeClient(interceptor.Funcs{}, claim)

		testReconcile(ctl,
			testData{blueVMKey, Succeed()},
		)

		assertClaimFinalizers(ctl, blueVMKey, "kubernetes")
	})

	It("should not release terminated claims owned by existing VM", func() {
		ctl := newFakeClient(interceptor.Funcs{},
			newVM(orangeVMKey, "20000"),
			newTerminatingClaim(orangeVMKey, newVMOwnerRef(orangeVMKey.Name, "20000")),
			newVMI(blueVMIKey, "20000"),
			newTerminatingClaim(blueVMIKey, newVMIOwnerRef(blueVMIKey.Name, "20000")),
		)

		testReconcile(ctl,
			testData{orangeVMKey, Succeed()},
			testData{blueVMIKey, Succeed()},
		)

		assertClaimKubevirtVMFinalizer(ctl, orangeVMKey)
		assertClaimKubevirtVMFinalizer(ctl, blueVMIKey)
	})

	It("should not release terminated claims owned by non VM object that not exist", func() {
		nonVMClaim := newClaim(ufoKey, newUnkownOwnerRef())
		nonVMClaim.Finalizers = []string{"kubernetes"}

		ctl := newFakeClient(interceptor.Funcs{}, nonVMClaim)

		testReconcile(ctl, testData{ufoKey, Succeed()})

		assertClaimFinalizers(ctl, ufoKey, "kubernetes")
	})
})

func newFakeClient(funcs interceptor.Funcs, objects ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithInterceptorFuncs(funcs).
		Build()
}

func injectGetError(err error) interceptor.Funcs {
	return interceptor.Funcs{
		Get: func(_ context.Context, _ client.WithWatch, _ types.NamespacedName, _ client.Object,
			_ ...client.GetOption) error {
			return err
		},
	}
}

func injectPatchError(err error) interceptor.Funcs {
	return interceptor.Funcs{
		Patch: func(_ context.Context, _ client.WithWatch, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
			return err
		},
	}
}

func newTerminatingClaim(key types.NamespacedName, ownerRefs ...metav1.OwnerReference) *ipamclaimsapi.IPAMClaim {
	claim := newClaim(key, ownerRefs...)
	claim.SetDeletionTimestamp(new(metav1.Now()))
	return claim
}

func newClaim(key types.NamespacedName, ownerRefs ...metav1.OwnerReference) *ipamclaimsapi.IPAMClaim {
	return &ipamclaimsapi.IPAMClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:            key.Name,
			Namespace:       key.Namespace,
			OwnerReferences: ownerRefs,
			Finalizers:      []string{"kubevirt.io/persistent-ipam"},
		},
	}
}

func newVM(key types.NamespacedName, uid types.UID) *virtv1.VirtualMachine {
	return &virtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
			UID:       uid,
		},
	}
}

func newVMI(key types.NamespacedName, uid types.UID) *virtv1.VirtualMachineInstance {
	return &virtv1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
			UID:       uid,
		},
	}
}

func newVMOwnerRef(name string, uid types.UID) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: "kubevirt.io/v1",
		Kind:       "VirtualMachine",
		Name:       name,
		UID:        uid,
	}
}

func newVMIOwnerRef(name string, uid types.UID) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: "kubevirt.io/v1",
		Kind:       "VirtualMachineInstance",
		Name:       name,
		UID:        uid,
	}
}

func newUnkownOwnerRef() metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: "unkown.com/v1",
		Kind:       "Unknown",
		Name:       rand.String(10),
		UID:        types.UID(rand.String(10)),
	}
}

func assertClaimAbsent(ctl client.Client, key types.NamespacedName) {
	GinkgoHelper()
	err := ctl.Get(context.Background(), key, &ipamclaimsapi.IPAMClaim{})
	desc := fmt.Sprintf("expected claim %q to be deleted after finalizer removal, got %v", key, err)
	Expect(err).To(MatchError(k8serrors.IsNotFound, desc))
}

func assertClaimKubevirtVMFinalizer(ctl client.Client, key types.NamespacedName) {
	GinkgoHelper()
	assertClaimFinalizers(ctl, key, "kubevirt.io/persistent-ipam")
}

func assertClaimFinalizers(ctl client.Client, key types.NamespacedName, finalizer string) {
	GinkgoHelper()
	claim := &ipamclaimsapi.IPAMClaim{}
	Expect(ctl.Get(context.Background(), key, claim)).To(Succeed())
	Expect(claim.Finalizers).To(ConsistOf(finalizer))
}

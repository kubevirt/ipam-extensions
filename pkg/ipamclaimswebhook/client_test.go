package ipamclaimswebhook_test

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/kubevirt/ipam-extensions/pkg/ipamclaimswebhook"
)

var _ = Describe("get function", func() {
	const (
		testNamespace = "test-ns"
		testName      = "test-configmap"
	)

	var (
		testObj    *corev1.ConfigMap
		testObjKey client.ObjectKey
	)

	BeforeEach(func() {
		testObj = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
			Data:       map[string]string{"allo": "hamora"},
		}
		testObjKey = client.ObjectKey{Name: testName, Namespace: testNamespace}
	})

	It("should succeed when object found in informer cache", func() {
		// populate informer client with test object
		ctl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(testObj).Build()
		// inject error to  ensure API reader client is not called
		funcs := newTestsFakeClientFuncs(errors.New("apiReader client error"))
		apiReaderCtl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithInterceptorFuncs(funcs).Build()

		actualObj := &corev1.ConfigMap{}
		err := ipamclaimswebhook.Get(context.Background(), ctl, apiReaderCtl, testObjKey, actualObj)

		Expect(err).NotTo(HaveOccurred())
		Expect(actualObj).To(Equal(testObj))
	})

	It("should fail when client returns an unexpected error", func() {
		// inject error to simulate client failure due to non not-found error
		expectedClientErr := errors.New("client error")
		funcs := newTestsFakeClientFuncs(expectedClientErr)
		ctl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithInterceptorFuncs(funcs).Build()
		apiReaderCtl := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()

		actualObj := &corev1.ConfigMap{}
		err := ipamclaimswebhook.Get(context.Background(), ctl, apiReaderCtl, testObjKey, actualObj)

		Expect(err).To(MatchError(expectedClientErr))
	})

	It("should fail when object not found in informer cache and API reader client fails", func() {
		ctl := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
		// inject error to simulate API reader client failure
		expectedApiReaderErr := errors.New("apiReader client error")
		funcs := newTestsFakeClientFuncs(expectedApiReaderErr)
		apiReaderCtl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithInterceptorFuncs(funcs).Build()

		actualObj := &corev1.ConfigMap{}
		err := ipamclaimswebhook.Get(context.Background(), ctl, apiReaderCtl, testObjKey, actualObj)

		Expect(err).To(MatchError(expectedApiReaderErr))
	})

	It("when object not found in informer cache, should succeed fetching object from API server", func() {
		// client is NOT populted with test object
		ctl := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
		// apiReader client is populted with test object
		apiReaderCtl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(testObj).Build()

		actualObj := &corev1.ConfigMap{}
		err := ipamclaimswebhook.Get(context.Background(), ctl, apiReaderCtl, testObjKey, actualObj)

		Expect(err).NotTo(HaveOccurred())
		Expect(actualObj).To(Equal(testObj))
	})
})

func newTestsFakeClientFuncs(err error) interceptor.Funcs {
	return interceptor.Funcs{
		Get: func(ctx context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
			return err
		},
	}
}

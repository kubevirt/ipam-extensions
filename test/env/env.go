/*
Copyright The Kubevirt IPAM controller authors


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

package env

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	ipamclaimsv1alpha1 "github.com/k8snetworkplumbingwg/ipamclaims/pkg/crd/ipamclaims/v1alpha1"
	nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

var (
	cfg     *rest.Config
	Client  client.Client // You'll be using this client in your tests.
	testEnv *envtest.Environment
)

type TestData struct {
	Namespace string
	Client    client.Client
}

func GenerateTestData() TestData {
	return TestData{
		Namespace: RandomNamespace(),
	}
}

func (td *TestData) SetUp() {
	GinkgoHelper()
	By(fmt.Sprintf("Creating namespace %s", td.Namespace))
	Expect(Client.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: td.Namespace}})).To(Succeed())
}

func (td *TestData) TearDown() {
	GinkgoHelper()
	By(fmt.Sprintf("Deleting namespace %s", td.Namespace))
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: td.Namespace}}
	Expect(Client.Delete(context.Background(), namespace)).To(Succeed())
	Eventually(func() error {
		return Client.Get(context.Background(), client.ObjectKeyFromObject(namespace), namespace)
	}).
		WithTimeout(5*time.Minute).
		WithPolling(2*time.Second).
		Should(WithTransform(apierrors.IsNotFound, BeTrue()), "should tear down namespace")
}

func Start() {
	GinkgoHelper()
	useExistingCluster := true
	testEnv = &envtest.Environment{
		UseExistingCluster: &useExistingCluster,
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(cfg).ToNot(BeNil())

	Expect(ipamclaimsv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(kubevirtv1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(nadv1.AddToScheme(scheme.Scheme)).To(Succeed())

	Client, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).ToNot(HaveOccurred())
	Expect(Client).ToNot(BeNil())
}

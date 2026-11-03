package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	ipamclaimsv1alpha1 "github.com/k8snetworkplumbingwg/ipamclaims/pkg/crd/ipamclaims/v1alpha1"
	nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	testenv "github.com/kubevirt/ipam-extensions/test/env"
)

const (
	recoveryManagerNamespace = "kubevirt-ipam-controller-system"
	recoveryManagerName      = "kubevirt-ipam-controller-manager"
	recoveryWebhookName      = "kubevirt-ipam-controller-mutating-webhook-configuration"
	recoveryWebhookService   = "kubevirt-ipam-controller-webhook-service"
	recoveryFinalizer        = "kubevirt.io/persistent-ipam"
	recoveryInterface        = "net1"
	recoveryTimeout          = 2 * time.Minute
)

// These tests stop the shared manager and must not overlap other workload tests.
var _ = Describe("IPAMClaim recovery", Serial, func() {
	var (
		td            testenv.TestData
		manager       appsv1.Deployment
		webhook       admissionv1.MutatingWebhookConfiguration
		replicas      int32
		vmTemplate    *kubevirtv1.VirtualMachine
		vm            *kubevirtv1.VirtualMachine
		originalClaim ipamclaimsv1alpha1.IPAMClaim
	)

	JustAfterEach(func() {
		// Always collect before restoration or fallback finalizer removal, including on failure.
		recordRecoveryState(td.Namespace, "before-cleanup")
	})

	BeforeEach(func() {
		ctx := context.Background()
		td = testenv.GenerateTestData()
		manager = appsv1.Deployment{}
		webhook = admissionv1.MutatingWebhookConfiguration{}
		originalClaim = ipamclaimsv1alpha1.IPAMClaim{}

		// Register in reverse execution order. Separate callbacks ensure restoration
		// is attempted even if evidence collection or another cleanup callback fails.
		DeferCleanup(func() {
			if webhook.UID == "" {
				return
			}
			current := &admissionv1.MutatingWebhookConfiguration{}
			Expect(testenv.Client.Get(ctx, client.ObjectKeyFromObject(&webhook), current)).To(Succeed())
			Expect(current.Webhooks).To(Equal(webhook.Webhooks), "webhook settings must remain unchanged")
		})
		namespaceCreated := false
		DeferCleanup(func() {
			if namespaceCreated {
				cleanRecoveryNamespace(td)
			}
		})

		managerKey := client.ObjectKey{Namespace: recoveryManagerNamespace, Name: recoveryManagerName}
		Expect(testenv.Client.Get(ctx, managerKey, &manager)).To(Succeed())
		replicas = 1
		if manager.Spec.Replicas != nil {
			replicas = *manager.Spec.Replicas
		}
		DeferCleanup(func() {
			setRecoveryManagerReplicas(manager, replicas)
			if replicas > 0 {
				waitForRecoveryManager(manager, replicas)
			}
		})
		Expect(replicas).To(BeNumerically(">", 0), "the manager must be running before the outage")
		Expect(testenv.Client.Get(ctx, client.ObjectKey{Name: recoveryWebhookName}, &webhook)).To(Succeed())
		waitForRecoveryManager(manager, replicas)

		td.SetUp(nil)
		namespaceCreated = true
		nad := testenv.GenerateLayer2WithSubnetNAD("recovery-net", td.Namespace, roleSecondary)
		Expect(testenv.Client.Create(ctx, nad)).To(Succeed())
		vmi := testenv.ComposeAlpineWithMultusVMI(td.Namespace, recoveryInterface, nad.Name)
		vmTemplate = testenv.NewVirtualMachine(vmi, testenv.WithRunning())
		vm = vmTemplate.DeepCopy()
		Expect(testenv.Client.Create(ctx, vm)).To(Succeed())

		By("waiting for a ready VMI with an allocated, VM-owned claim on the secondary network")
		// VMI readiness proves controller/CNI readiness here, not guest boot.
		Eventually(func(g Gomega) {
			g.Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(vmi), vmi)).To(Succeed())
			g.Expect(vmi.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", kubevirtv1.VirtualMachineInstanceAgentConnected),
				HaveField("Status", corev1.ConditionTrue))))
			g.Expect(vmi).To(testenv.BeReadyWithIPsAtInterface(recoveryInterface, Not(BeEmpty())))
		}).WithPolling(time.Second).WithTimeout(5 * time.Minute).Should(Succeed())

		By("fetch updated VM")
		Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(vm), vm)).To(Succeed())

		By("VM-owned ipam claim on the secondary network")
		Eventually(func(g Gomega) {
			claims := &ipamclaimsv1alpha1.IPAMClaimList{}
			g.Expect(testenv.Client.List(ctx, claims, client.InNamespace(td.Namespace))).To(Succeed())
			g.Expect(claims.Items).To(HaveLen(1))
			claim := claims.Items[0]
			g.Expect(claim.UID).NotTo(BeEmpty())
			expectRecoveryClaimOwner(g, claim, *vm)
			g.Expect(claim.DeletionTimestamp).To(BeNil())
			g.Expect(claim.Finalizers).To(ContainElement(recoveryFinalizer))
			g.Expect(claim.Status.IPs).NotTo(BeEmpty())
			g.Expect(testenv.Client.Get(ctx, client.ObjectKeyFromObject(vmi), vmi)).To(Succeed())
			g.Expect(vmi).To(testenv.BeReadyWithIPsAtInterface(recoveryInterface, ContainElements(claimAddresses(claim))))
			originalClaim = claim
		}, time.Minute, time.Second).Should(Succeed())

		By("dump state before ipam controller outage")
		recordRecoveryState(td.Namespace, "before-outage")

		By("stopping every manager replica and waiting for all manager Pods to disappear")
		setRecoveryManagerReplicas(manager, 0)
		selector, err := metav1.LabelSelectorAsSelector(manager.Spec.Selector)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			pods := &corev1.PodList{}
			g.Expect(testenv.Client.List(ctx, pods, client.InNamespace(manager.Namespace),
				client.MatchingLabelsSelector{Selector: selector})).To(Succeed())
			g.Expect(pods.Items).To(BeEmpty())
		}, recoveryTimeout, time.Second).Should(Succeed())

		By("background-deleting the VM while the manager is absent")
		//Expect(testenv.Client.Delete(ctx, vm, client.PropagationPolicy(metav1.DeletePropagationBackground))).To(Succeed())
		Expect(testenv.Client.Delete(ctx, vm)).To(Succeed())

		By("wait for VM to be gone")
		waitForRecoveryWorkloadsGone(td.Namespace)

		By("verify ipam claim still exist with the original UID")
		Eventually(func(g Gomega) {
			claim := &ipamclaimsv1alpha1.IPAMClaim{}
			g.Expect(testenv.Client.Get(ctx, client.ObjectKeyFromObject(&originalClaim), claim)).To(Succeed())
			g.Expect(claim.UID).To(Equal(originalClaim.UID))
			g.Expect(claim.OwnerReferences).To(Equal(originalClaim.OwnerReferences))
			g.Expect(claim.DeletionTimestamp).NotTo(BeNil())
			g.Expect(claim.Finalizers).To(ContainElement(recoveryFinalizer))
			g.Expect(claim.Status.IPs).To(ConsistOf(originalClaim.Status.IPs))
		}, time.Minute, time.Second).Should(Succeed())
	})

	When("the controller misses VM deletion", func() {
		It("removes the orphaned claim after restarting without a replacement VM", func() {
			By("bring up ipam controller")
			setRecoveryManagerReplicas(manager, replicas)
			By("wait for ipam controller readiness")
			waitForRecoveryManager(manager, replicas)
			By("verify ipam claim has been disposed")
			Eventually(func() error {
				return testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(&originalClaim),
					&ipamclaimsv1alpha1.IPAMClaim{})
			}, recoveryTimeout, time.Second).Should(WithTransform(apierrors.IsNotFound, BeTrue()),
				"orphan ipam claim should have been cleaned up (even though the controller missed the consumer delete event)")
		})

		It("allocates a fresh correctly owned claim for a same-name replacement present at restart", func() {
			By("creating VM with same-name before restoring the manager")
			vmReplacment := vmTemplate.DeepCopy()
			Expect(testenv.Client.Create(context.Background(), vmReplacment)).To(Succeed())
			Expect(vmReplacment.UID).NotTo(Equal(vm.UID))

			// skip vm readiness because it cannot be ready, this is due the ipam controller webhook server is down
			//By("waiting for VMI replacement readiness")
			//Eventually(func(g Gomega) {
			//	g.Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(vmReplacment), vmReplacment)).To(Succeed())
			//	g.Expect(vmReplacment.Status.Conditions).To(ContainElement(SatisfyAll(
			//		HaveField("Type", kubevirtv1.VirtualMachineInstanceAgentConnected),
			//		HaveField("Status", corev1.ConditionTrue))))
			//	g.Expect(vmReplacment).To(testenv.BeReadyWithIPsAtInterface(recoveryInterface, Not(BeEmpty())))
			//}).WithPolling(time.Second).WithTimeout(5 * time.Minute).Should(Succeed())

			By("record state  VM replacement creation, before controller restore")
			recordRecoveryState(td.Namespace, "replacement-before-restart")

			By("bring up ipam controller")
			setRecoveryManagerReplicas(manager, replicas)
			By("wait for ipam controller readiness")
			waitForRecoveryManager(manager, replicas)

			By("waiting for a fresh claim and a ready replacement VMI using its allocation")
			Eventually(func(g Gomega) {
				claim := &ipamclaimsv1alpha1.IPAMClaim{}
				g.Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(&originalClaim), claim)).
					To(Succeed())
				g.Expect(claim.UID).NotTo(Equal(originalClaim.UID), "must replace the old object, not adopt it")
				expectRecoveryClaimOwner(g, *claim, *vmReplacment)
				g.Expect(claim.Spec).To(Equal(originalClaim.Spec))
				g.Expect(claim.DeletionTimestamp).To(BeNil())
				g.Expect(claim.Finalizers).To(ContainElement(recoveryFinalizer))
				g.Expect(claim.Status.IPs).NotTo(BeEmpty())
				vmi := &kubevirtv1.VirtualMachineInstance{}
				g.Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(vmReplacment), vmi)).To(Succeed())
				g.Expect(metav1.GetControllerOf(vmi)).NotTo(BeNil())
				g.Expect(metav1.GetControllerOf(vmi).UID).To(Equal(vmReplacment.UID))
				// Reusing the numerical IP is valid once a fresh claim owns it.
				g.Expect(vmi).To(testenv.BeReadyWithIPsAtInterface(recoveryInterface, ConsistOf(claimAddresses(*claim))))
			}, recoveryTimeout, time.Second).Should(Succeed())
		})
	})
})

func expectRecoveryClaimOwner(g Gomega, claim ipamclaimsv1alpha1.IPAMClaim, vm kubevirtv1.VirtualMachine) {
	GinkgoHelper()
	owner := metav1.GetControllerOf(&claim)
	g.Expect(owner).NotTo(BeNil())
	g.Expect(owner.APIVersion).To(Equal(kubevirtv1.GroupVersion.String()))
	g.Expect(owner.Kind).To(Equal("VirtualMachine"))
	g.Expect(owner.Name).To(Equal(vm.Name))
	g.Expect(owner.UID).To(Equal(vm.UID))
}

func claimAddresses(claim ipamclaimsv1alpha1.IPAMClaim) []string {
	addresses := make([]string, 0, len(claim.Status.IPs))
	for _, ip := range claim.Status.IPs {
		if prefix, err := netip.ParsePrefix(ip); err == nil {
			ip = prefix.Addr().String()
		}
		addresses = append(addresses, ip)
	}
	return addresses
}

func setRecoveryManagerReplicas(original appsv1.Deployment, replicas int32) {
	GinkgoHelper()
	Eventually(func() error {
		current := &appsv1.Deployment{}
		if err := testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(&original), current); err != nil {
			return err
		}
		if current.UID != original.UID {
			return fmt.Errorf("manager deployment was replaced: original UID %s, current UID %s", original.UID, current.UID)
		}
		current.Spec.Replicas = &replicas
		return testenv.Client.Update(context.Background(), current)
	}, time.Minute, time.Second).Should(Succeed())
}

func waitForRecoveryManager(original appsv1.Deployment, replicas int32) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		current := &appsv1.Deployment{}
		g.Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(&original), current)).To(Succeed())
		g.Expect(current.UID).To(Equal(original.UID))
		g.Expect(current.Status.ObservedGeneration).To(BeNumerically(">=", current.Generation))
		g.Expect(current.Status.UpdatedReplicas).To(Equal(replicas))
		g.Expect(current.Status.ReadyReplicas).To(Equal(replicas))
		g.Expect(current.Status.AvailableReplicas).To(Equal(replicas))
		slices := &discoveryv1.EndpointSliceList{}
		g.Expect(testenv.Client.List(context.Background(), slices, client.InNamespace(original.Namespace),
			client.MatchingLabels{discoveryv1.LabelServiceName: recoveryWebhookService})).To(Succeed())
		ready := 0
		for _, slice := range slices.Items {
			for _, endpoint := range slice.Endpoints {
				if endpoint.Conditions.Ready != nil && *endpoint.Conditions.Ready {
					ready++
				}
			}
		}
		g.Expect(ready).To(BeNumerically(">=", replicas), "webhook endpoints must be ready")
	}, recoveryTimeout, time.Second).Should(Succeed())
}

func waitForRecoveryWorkloadsGone(namespace string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		vms := &kubevirtv1.VirtualMachineList{}
		g.Expect(testenv.Client.List(context.Background(), vms, client.InNamespace(namespace))).To(Succeed())
		g.Expect(vms.Items).To(BeEmpty())
		vmis := &kubevirtv1.VirtualMachineInstanceList{}
		g.Expect(testenv.Client.List(context.Background(), vmis, client.InNamespace(namespace))).To(Succeed())
		g.Expect(vmis.Items).To(BeEmpty())
		pods := &corev1.PodList{}
		g.Expect(testenv.Client.List(context.Background(), pods, client.InNamespace(namespace))).To(Succeed())
		g.Expect(pods.Items).To(BeEmpty(), "all Pods must disappear before releasing any allocation")
	}, 5*time.Minute, time.Second).Should(Succeed())
}

func cleanRecoveryNamespace(td testenv.TestData) {
	GinkgoHelper()
	ctx := context.Background()
	namespace := &corev1.Namespace{}
	err := testenv.Client.Get(ctx, client.ObjectKey{Name: td.Namespace}, namespace)
	if apierrors.IsNotFound(err) {
		return
	}
	Expect(err).NotTo(HaveOccurred())
	By("removing test workloads before releasing leaked test claims")
	Expect(testenv.Client.DeleteAllOf(ctx, &kubevirtv1.VirtualMachine{}, client.InNamespace(td.Namespace),
		client.PropagationPolicy(metav1.DeletePropagationBackground))).To(Succeed())
	// VM garbage collection removes VMIs and their launcher Pods; do not force
	// finalizers or release claims while a workload might still be using them.
	waitForRecoveryWorkloadsGone(td.Namespace)
	claims := &ipamclaimsv1alpha1.IPAMClaimList{}
	Expect(testenv.Client.List(ctx, claims, client.InNamespace(td.Namespace))).To(Succeed())
	for _, claim := range claims.Items {
		By(fmt.Sprintf("removing only %s from unused test claim %s/%s", recoveryFinalizer, claim.Namespace, claim.Name))
		Eventually(func() error {
			current := &ipamclaimsv1alpha1.IPAMClaim{}
			if err := testenv.Client.Get(ctx, client.ObjectKeyFromObject(&claim), current); err != nil {
				return client.IgnoreNotFound(err)
			}
			if current.UID != claim.UID {
				return fmt.Errorf("test claim %s was replaced during cleanup", claim.Name)
			}
			if !controllerutil.RemoveFinalizer(current, recoveryFinalizer) {
				return nil
			}
			return client.IgnoreNotFound(testenv.Client.Update(ctx, current))
		}, time.Minute, time.Second).Should(Succeed())
	}
	td.TearDown()
}

// Use Go clients so evidence collection does not depend on a kubectl executable.
// Collection errors are recorded, without preventing manager restoration.
func recordRecoveryState(namespace, phase string) {
	directory := filepath.Join(logsDir, "claim-recovery", namespace, phase)
	if err := os.MkdirAll(directory, 0700); err != nil {
		GinkgoWriter.Printf("Cannot create recovery evidence directory %s: %v\n", directory, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	evidence := map[string]interface{}{
		"snapshot": map[string]interface{}{
			"time": time.Now().UTC(), "phase": phase, "namespace": namespace,
			"test": CurrentSpecReport().FullText(),
		},
	}
	objects := map[string]client.ObjectList{
		"vms":      &kubevirtv1.VirtualMachineList{},
		"vmis":     &kubevirtv1.VirtualMachineInstanceList{},
		"pods":     &corev1.PodList{},
		"claims":   &ipamclaimsv1alpha1.IPAMClaimList{},
		"networks": &nadv1.NetworkAttachmentDefinitionList{},
		"events":   &corev1.EventList{},
	}
	for name, objects := range objects {
		if err := testenv.Client.List(ctx, objects, client.InNamespace(namespace)); err != nil {
			evidence[name+"Error"] = err.Error()
			continue
		}
		evidence[name] = objects
	}
	managerObjects := map[string]client.ObjectList{
		"managerPods": &corev1.PodList{}, "webhookEndpoints": &discoveryv1.EndpointSliceList{},
	}
	for name, objects := range managerObjects {
		if err := testenv.Client.List(ctx, objects, client.InNamespace(recoveryManagerNamespace)); err != nil {
			evidence[name+"Error"] = err.Error()
			continue
		}
		evidence[name] = objects
	}
	webhook := &admissionv1.MutatingWebhookConfiguration{}
	if err := testenv.Client.Get(ctx, client.ObjectKey{Name: recoveryWebhookName}, webhook); err != nil {
		evidence["webhookError"] = err.Error()
	}
	evidence["webhook"] = webhook
	manager := &appsv1.Deployment{}
	if err := testenv.Client.Get(ctx, client.ObjectKey{
		Namespace: recoveryManagerNamespace, Name: recoveryManagerName,
	}, manager); err != nil {
		evidence["managerError"] = err.Error()
	}
	evidence["manager"] = manager
	for name, object := range evidence {
		writeRecoveryObject(filepath.Join(directory, name+".json"), object)
	}
	AddReportEntry(namespace+"-"+phase, directory)
}

func writeRecoveryObject(path string, object interface{}) {
	data, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		GinkgoWriter.Printf("Cannot serialize recovery evidence %s: %v\n", path, err)
		return
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		GinkgoWriter.Printf("Cannot save recovery evidence %s: %v\n", path, err)
	}
}

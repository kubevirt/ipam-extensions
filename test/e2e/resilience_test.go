package e2e

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	ipamclaimsv1alpha1 "github.com/k8snetworkplumbingwg/ipamclaims/pkg/crd/ipamclaims/v1alpha1"
	kubevirtv1 "kubevirt.io/api/core/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	testenv "github.com/kubevirt/ipam-extensions/test/env"
)

const (
	managerNamespace = "kubevirt-ipam-controller-system"
	managerName      = "kubevirt-ipam-controller-manager"
	webhookService   = "kubevirt-ipam-controller-webhook-service"
	kvFinalizer      = "kubevirt.io/persistent-ipam"
	vmNetIfaceKey    = "l2-sec"
)

// Resilience tests are Serial because they involve ipam-controller restarts
// and must not overlap with test assuming healthy controller
var _ = Describe("Resilience", Serial, func() {
	var (
		td         testenv.TestData
		deployment *appsv1.Deployment
		replicas   int32
	)

	JustAfterEach(func() {
		By("dump logs, before test cleanup")
		dumpTestPhaseLogs(td.Namespace, "before-cleanup")
	})

	BeforeEach(func() {
		By("verifying ipam controller is up")
		deployment = &appsv1.Deployment{}
		ipamCtrlKey := client.ObjectKey{Namespace: managerNamespace, Name: managerName}
		Expect(testenv.Client.Get(context.Background(), ipamCtrlKey, deployment)).To(Succeed())
		replicas = 1
		if deployment.Spec.Replicas != nil {
			replicas = *deployment.Spec.Replicas
		}
		DeferCleanup(func() {
			By("recovering ipam controller deployment replicas")
			setDeployReplicas(*deployment, replicas)
			waitForDeploy(*deployment, replicas)
		})
		Expect(replicas).To(BeNumerically(">", 0), "tests require controller to be running")
		waitForDeploy(*deployment, replicas)

		td = testenv.GenerateTestData()
		td.SetUp(nil)
		DeferCleanup(func() {
			cleanupNamespace(td.Namespace)
			td.TearDown()
		})
	})

	Context("controller is down, orphan ipam-claim left dangling although the associated VM is gone", func() {
		var vmTemplate *kubevirtv1.VirtualMachine
		var originalClaim *ipamclaimsv1alpha1.IPAMClaim

		BeforeEach(func() {
			By("creating test network resource (l2, secondary, persistent-ips")
			nad := testenv.GenerateLayer2WithSubnetNAD("recovery-net", td.Namespace, roleSecondary)
			Expect(testenv.Client.Create(context.Background(), nad)).To(Succeed())

			By("create test VM")
			vmi := testenv.ComposeAlpineWithMultusVMI(td.Namespace, vmNetIfaceKey, nad.Name)
			vmTemplate = testenv.NewVirtualMachine(vmi, testenv.WithRunning())
			vm := vmTemplate.DeepCopy()
			Expect(testenv.Client.Create(context.Background(), vm)).To(Succeed())
			By("waiting for VM readiness")
			Eventually(func(g Gomega) {
				g.Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(vmi), vmi)).To(Succeed())
				g.Expect(vmi.Status.Conditions).To(ContainElement(SatisfyAll(
					HaveField("Type", kubevirtv1.VirtualMachineInstanceAgentConnected),
					HaveField("Status", corev1.ConditionTrue))))
			}).WithPolling(time.Second).WithTimeout(5 * time.Minute).Should(Succeed())
			Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(vm), vm)).To(Succeed())

			By("assert VM's IP is backed by ipam-claim resource")
			Eventually(func(g Gomega) {
				claims := &ipamclaimsv1alpha1.IPAMClaimList{}
				g.Expect(testenv.Client.List(context.Background(), claims, client.InNamespace(td.Namespace))).To(Succeed())
				g.Expect(claims.Items).To(HaveLen(1))
				claim := claims.Items[0]
				assertClaimOwnedByVM(g, claim, *vm)
				asserClaimReady(g, claim)

				g.Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(vmi), vmi)).To(Succeed())
				g.Expect(vmi).To(testenv.BeReadyWithIPsAtInterface(vmNetIfaceKey, ContainElements(claimAddresses(claim))),
					"vm should be ready and status reflect assigned persistent IPs")
				originalClaim = &claim
			}, time.Minute, time.Second).Should(Succeed())

			By("dump logs, before ipam controller outage")
			dumpTestPhaseLogs(td.Namespace, "before-outage")

			By("simulate controller missing delete event / outage (scale down ipam-controller deployment to zero)")
			setDeployReplicas(*deployment, 0)
			selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				pods := &corev1.PodList{}
				g.Expect(testenv.Client.List(context.Background(), pods, client.InNamespace(deployment.Namespace),
					client.MatchingLabelsSelector{Selector: selector})).To(Succeed())
				g.Expect(pods.Items).To(BeEmpty())
			}, time.Minute*2, time.Second).Should(Succeed())

			By("delete VM (while ipam-controller is down -> miss delete event)")
			Expect(testenv.Client.Delete(context.Background(), vm)).To(Succeed())
			By("wait for VM to be gone")
			waitForTestWorkloadsDisposal(td.Namespace)

			By("verify ipam claim exist although the associated VM is gone")
			Eventually(func(g Gomega) {
				claim := &ipamclaimsv1alpha1.IPAMClaim{}
				g.Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(originalClaim), claim)).To(Succeed())
				g.Expect(claim.UID).To(Equal(originalClaim.UID))
				g.Expect(claim.OwnerReferences).To(Equal(originalClaim.OwnerReferences))
				g.Expect(claim.DeletionTimestamp).NotTo(BeNil())
				g.Expect(claim.Finalizers).To(ContainElement(kvFinalizer))
				g.Expect(claim.Status.IPs).To(ConsistOf(originalClaim.Status.IPs))
			}, time.Minute, time.Second).Should(Succeed())
		})

		It("when ipam controller goes back, it should release orphaned claim owned by disposed VM", func() {
			By("restart ipam controller")
			setDeployReplicas(*deployment, replicas)
			By("wait for ipam-controller readiness")
			waitForDeploy(*deployment, replicas)

			By("assert the orphan ipam claim has been disposed")
			Eventually(func() error {
				return testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(originalClaim),
					&ipamclaimsv1alpha1.IPAMClaim{})
			}, time.Minute*2, time.Second).Should(WithTransform(apierrors.IsNotFound, BeTrue()),
				"orphan ipam claim should have been cleaned up when controller gets back up")
		})

		It("when new VM with same name is created, controller restarts, it should create new claim", func() {
			// create replacement VM carrying the same name as the VM
			// that has been deleted associated with the dangling claim
			By("creating new VM with same name as the one deleted")
			vmReplacment := vmTemplate.DeepCopy()
			Expect(testenv.Client.Create(context.Background(), vmReplacment)).To(Succeed())
			Eventually(func(g Gomega) {
				vmi := &kubevirtv1.VirtualMachineInstance{}
				g.Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(vmReplacment), vmi)).To(Succeed())
			}, time.Minute, time.Second).Should(Succeed())
			// skip vm readiness check, it cannot be ready because the ipam-webhook server is down blocking virt-launchers

			By("dump logs,replacement vm create before controller restore")
			dumpTestPhaseLogs(td.Namespace, "replacement-vm-before-restart")

			By("restart ipam controller")
			setDeployReplicas(*deployment, replicas)
			By("wait for ipam-controller readiness")
			waitForDeploy(*deployment, replicas)

			By("assert new claim is created, owned by the replacement VM")
			Eventually(func(g Gomega) {
				claim := &ipamclaimsv1alpha1.IPAMClaim{}
				g.Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(originalClaim), claim)).To(Succeed())
				g.Expect(claim.UID).NotTo(Equal(originalClaim.UID), "claim must replace the old object, not adopt it")
				g.Expect(claim.Spec).To(Equal(originalClaim.Spec), "claim spec should not change")
				assertClaimOwnedByVM(g, *claim, *vmReplacment)
				asserClaimReady(g, *claim)

				vmi := &kubevirtv1.VirtualMachineInstance{}
				g.Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(vmReplacment), vmi)).To(Succeed())
				g.Expect(vmi).To(testenv.BeReadyWithIPsAtInterface(vmNetIfaceKey, ContainElements(claimAddresses(*claim))))
			}, time.Minute*2, time.Second).Should(Succeed())
		})
	})
})

func setDeployReplicas(original appsv1.Deployment, replicas int32) {
	GinkgoHelper()
	By(fmt.Sprintf("set deployment %s/%s replicas %d", original.Name, original.Namespace, replicas))
	Eventually(func() error {
		current := &appsv1.Deployment{}
		if err := testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(&original), current); err != nil {
			return err
		}
		if current.UID != original.UID {
			return fmt.Errorf("deployment UID mismatch; replaced (og=%s, new=%s)", original.UID, current.UID)
		}
		current.Spec.Replicas = &replicas
		if uerr := testenv.Client.Update(context.Background(), current); uerr != nil {
			return fmt.Errorf("failed to update deployment [%s/%s] object following replicas change: %w",
				original.Name, original.Namespace, uerr)
		}
		return nil
	}, time.Minute, time.Second).Should(Succeed())
}

func waitForDeploy(original appsv1.Deployment, replicas int32) {
	GinkgoHelper()
	By(fmt.Sprintf("wait for deployment %s/%s readiness", original.Name, original.Namespace))
	Eventually(func(g Gomega) {
		current := &appsv1.Deployment{}
		g.Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(&original), current)).To(Succeed())
		g.Expect(current.UID).To(Equal(original.UID))
		g.Expect(current.Status.UpdatedReplicas).To(Equal(replicas))
		g.Expect(current.Status.ReadyReplicas).To(Equal(replicas))
		g.Expect(current.Status.AvailableReplicas).To(Equal(replicas))
		slices := &discoveryv1.EndpointSliceList{}
		g.Expect(testenv.Client.List(context.Background(), slices, client.InNamespace(original.Namespace),
			client.MatchingLabels{discoveryv1.LabelServiceName: webhookService})).To(Succeed())
		ready := 0
		for _, slice := range slices.Items {
			for _, endpoint := range slice.Endpoints {
				if endpoint.Conditions.Ready != nil && *endpoint.Conditions.Ready {
					ready++
				}
			}
		}
		g.Expect(ready).To(BeNumerically(">=", replicas), "webhook endpoints must be ready")
	}, time.Minute*2, time.Second).Should(Succeed())
}

func waitForTestWorkloadsDisposal(namespace string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		vms := &kubevirtv1.VirtualMachineList{}
		g.Expect(testenv.Client.List(context.Background(), vms, client.InNamespace(namespace))).To(Succeed())
		g.Expect(vms.Items).To(BeEmpty(), "vms should be gone")
		vmis := &kubevirtv1.VirtualMachineInstanceList{}
		g.Expect(testenv.Client.List(context.Background(), vmis, client.InNamespace(namespace))).To(Succeed())
		g.Expect(vmis.Items).To(BeEmpty(), "vmis should be gone")
		pods := &corev1.PodList{}
		g.Expect(testenv.Client.List(context.Background(), pods, client.InNamespace(namespace))).To(Succeed())
		g.Expect(pods.Items).To(BeEmpty(), "pods should be gone")
	}, 5*time.Minute, time.Second).Should(Succeed())
}

func cleanupNamespace(name string) {
	GinkgoHelper()
	By("cleanup test namespace")
	ctx := context.Background()
	namespace := &corev1.Namespace{}
	err := testenv.Client.Get(ctx, client.ObjectKey{Name: name}, namespace)
	if apierrors.IsNotFound(err) {
		return
	}
	Expect(err).NotTo(HaveOccurred())

	By("delete test workloads (before cleaning up ipam-claims)")
	Expect(testenv.Client.DeleteAllOf(ctx, &kubevirtv1.VirtualMachine{}, client.InNamespace(name),
		client.PropagationPolicy(metav1.DeletePropagationBackground))).To(Succeed())
	waitForTestWorkloadsDisposal(name)

	By("cleanup ipam-claims")
	claims := &ipamclaimsv1alpha1.IPAMClaimList{}
	Expect(testenv.Client.List(ctx, claims, client.InNamespace(name))).To(Succeed())
	for _, claim := range claims.Items {
		By(fmt.Sprintf("removing ipam-claim [%s/%s] finalizer [%s]",
			claim.Namespace, claim.Name, kvFinalizer))
		Eventually(func() error {
			current := &ipamclaimsv1alpha1.IPAMClaim{}
			if gerr := testenv.Client.Get(ctx, client.ObjectKeyFromObject(&claim), current); gerr != nil {
				return client.IgnoreNotFound(gerr)
			}
			if current.UID != claim.UID {
				return fmt.Errorf("ipam claim [%s/%s] UID mismatch; replaced during cleanup",
					claim.Namespace, claim.Name)
			}
			if !controllerutil.RemoveFinalizer(current, kvFinalizer) {
				return fmt.Errorf("failed to remove ipam-claim [%s/%s] finalizer [%s]",
					claim.Namespace, claim.Name, kvFinalizer)
			}
			if uerr := client.IgnoreNotFound(testenv.Client.Update(ctx, current)); uerr != nil {
				return fmt.Errorf("failed to update ipam-claim [%s/%s] following finalizer removal [%s]: %v",
					claim.Namespace, claim.Name, kvFinalizer, uerr)
			}
			return nil
		}, time.Minute, time.Second).Should(Succeed(),
			"should release ipam claims in namespace %s", claim.Namespace)
	}
}

func asserClaimReady(g Gomega, claim ipamclaimsv1alpha1.IPAMClaim) {
	GinkgoHelper()
	g.Expect(claim.DeletionTimestamp).To(BeNil(), "claim should not marked for deleting")
	g.Expect(claim.Finalizers).To(ContainElement(kvFinalizer), "claim should have kv-ipam finalizer set")
	g.Expect(claim.Status.IPs).NotTo(BeEmpty(), "claim status should reflect assigned IPs")
}

func assertClaimOwnedByVM(g Gomega, claim ipamclaimsv1alpha1.IPAMClaim, vm kubevirtv1.VirtualMachine) {
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

func dumpTestPhaseLogs(namespace, phase string) {
	phaseDir := filepath.Join(logsDir, "resilience", phase)
	if err := os.MkdirAll(phaseDir, 0700); err != nil {
		GinkgoWriter.Printf("Cannot create recovery evidence directory %s: %v\n", phaseDir, err)
		return
	}

	testNsDir := filepath.Join(phaseDir, namespace)
	if err := os.MkdirAll(testNsDir, 0700); err != nil {
		GinkgoWriter.Printf("Cannot create recovery evidence directory %s: %v\n", testNsDir, err)
		return
	}

	c := 0
	logCommandTo(testNsDir, []string{"get", "namespaces", namespace, "-oyaml"}, "namespace", c)
	logCommandTo(testNsDir, []string{"get", "vm", "-n", namespace, "-oyaml"}, "vms", c)
	logCommandTo(testNsDir, []string{"get", "vmi", "-n", namespace, "-oyaml"}, "vmis", c)
	logCommandTo(testNsDir, []string{"get", "pods", "-n", namespace, "-oyaml"}, "pods", c)
	logCommandTo(testNsDir, []string{"get", "ipamclaims", "-n", namespace, "-oyaml"}, "ipamclaims", c)
	logCommandTo(testNsDir, []string{"get", "net-attach-def", "-n", namespace, "-oyaml"}, "network-attachments", c)

	const managerNs = "kubevirt-ipam-controller-system"
	const webhook = "kubevirt-ipam-controller-mutating-webhook-configuration"
	managerNsDir := filepath.Join(phaseDir, managerNs)
	if err := os.MkdirAll(managerNsDir, 0700); err != nil {
		GinkgoWriter.Printf("Cannot create recovery evidence directory %s: %v\n", managerNsDir, err)
		return
	}
	logCommandTo(managerNsDir, []string{"get", "namespaces", managerNs, "-oyaml"}, "namespace", c)
	logCommandTo(managerNsDir, []string{"get", "pods", "-n", managerNs, "-oyaml"}, "pods", c)
	logCommandTo(managerNsDir, []string{"get", "deploy", "-n", managerNs, "-oyaml"}, "deployments", c)
	logCommandTo(managerNsDir, []string{"get", "services", "-n", managerNs, "-oyaml"}, "services", c)
	logCommandTo(managerNsDir, []string{"get", "mutatingwebhookconfigurations", webhook, "-oyaml"},
		"mutatingwebhookconfigurations", c)

	dumpManagerPodLogs(managerNsDir, c)

	ovnNsDir := filepath.Join(phaseDir, "ovn-kubenretes")
	if err := os.MkdirAll(ovnNsDir, 0700); err != nil {
		GinkgoWriter.Printf("Cannot create recovery evidence directory %s: %v\n", ovnNsDir, err)
		return
	}
	logOvnPods(ovnNsDir, c)
}

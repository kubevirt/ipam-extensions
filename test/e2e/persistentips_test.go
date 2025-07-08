/*
 * This file is part of the KubeVirt project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright 2024 Red Hat, Inc.
 *
 */

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	kubevirtv1 "kubevirt.io/api/core/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"

	testenv "github.com/kubevirt/ipam-extensions/test/env"
)

const (
	secondaryLogicalNetworkInterfaceName = "multus_iface"
	primaryLogicalNetworkInterfaceName   = "pod_iface"
	nadName                              = "l2-net-attach-def"
)

const (
	rolePrimary   = "primary"
	roleSecondary = "secondary"
)

type testParams struct {
	role                 string
	networkInterfaceName string
	vmi                  func(namespace string) *kubevirtv1.VirtualMachineInstance
}

var _ = DescribeTableSubtree("Persistent IPs", func(params testParams) {
	var failureCount int = 0
	JustAfterEach(func() {
		if CurrentSpecReport().Failed() {
			failureCount++
			logFailure(failureCount)
		}
	})

	When("network attachment definition created with allowPersistentIPs=true", func() {
		var (
			td  testenv.TestData
			vm  *kubevirtv1.VirtualMachine
			vmi *kubevirtv1.VirtualMachineInstance
			nad *nadv1.NetworkAttachmentDefinition
		)

		BeforeEach(func() {
			td = testenv.GenerateTestData()
			labels := map[string]string{}
			if params.role == rolePrimary {
				labels["k8s.ovn.org/primary-user-defined-network"] = ""
			}
			td.SetUp(labels)
			DeferCleanup(func() {
				td.TearDown()
			})

			nad = testenv.GenerateLayer2WithSubnetNAD(nadName, td.Namespace, params.role)
			vmi = params.vmi(td.Namespace)
			vm = testenv.NewVirtualMachine(vmi, testenv.WithRunning())

			By("Create NetworkAttachmentDefinition")
			Expect(testenv.Client.Create(context.Background(), nad)).To(Succeed())
		})
		Context("and a virtual machine using it is also created", func() {
			BeforeEach(func() {
				By("Creating VM with secondary attachments")
				Expect(testenv.Client.Create(context.Background(), vm)).To(Succeed())

				By(fmt.Sprintf("Waiting for readiness at virtual machine %s", vm.Name))
				Eventually(testenv.ThisVMReadiness(vm)).
					WithPolling(time.Second).
					WithTimeout(5 * time.Minute).
					Should(BeTrue())

				By("Wait for IPAMClaim to get created")
				Eventually(testenv.IPAMClaimsFromNamespace(vm.Namespace)).
					WithTimeout(time.Minute).
					WithPolling(time.Second).
					ShouldNot(BeEmpty())

				Expect(testenv.ThisVMI(vmi)()).Should(testenv.MatchIPsAtInterfaceByName(params.networkInterfaceName, Not(BeEmpty())))
			})

			It("should keep ips after live migration", func() {
				Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(vmi), vmi)).To(Succeed())
				vmiIPsBeforeMigration := testenv.GetIPsFromVMIStatus(vmi, params.networkInterfaceName)
				Expect(vmiIPsBeforeMigration).NotTo(BeEmpty())

				testenv.LiveMigrateVirtualMachine(td.Namespace, vm.Name)
				testenv.CheckLiveMigrationSucceeded(td.Namespace, vm.Name)

				By("Wait for VMI to be ready after live migration")
				Eventually(testenv.ThisVMI(vmi)).
					WithPolling(time.Second).
					WithTimeout(5 * time.Minute).
					Should(testenv.ContainConditionVMIReady())

				Expect(testenv.ThisVMI(vmi)()).Should(testenv.MatchIPsAtInterfaceByName(params.networkInterfaceName, ConsistOf(vmiIPsBeforeMigration)))
			})

			It("should garbage collect IPAMClaims after VM deletion", func() {
				Expect(testenv.Client.Delete(context.Background(), vm)).To(Succeed())
				Eventually(testenv.IPAMClaimsFromNamespace(vm.Namespace)).
					WithTimeout(time.Minute).
					WithPolling(time.Second).
					Should(BeEmpty())
			})

			It("should garbage collect IPAMClaims after VM foreground deletion", func() {
				Expect(testenv.Client.Delete(context.Background(), vm, foregroundDeleteOptions())).To(Succeed())
				Eventually(testenv.IPAMClaimsFromNamespace(vm.Namespace)).
					WithTimeout(time.Minute).
					WithPolling(time.Second).
					Should(BeEmpty())
			})

			When("the VM is stopped", func() {
				BeforeEach(func() {
					By("Invoking virtctl stop")
					output, err := exec.Command("virtctl", "stop", "-n", td.Namespace, vmi.Name).CombinedOutput()
					Expect(err).NotTo(HaveOccurred(), output)

					By("Ensuring VM is not running")
					Eventually(testenv.ThisVMI(vmi), 360*time.Second, 1*time.Second).Should(
						SatisfyAll(
							Not(testenv.BeCreated()),
							Not(testenv.BeReady()),
						))

					Consistently(testenv.IPAMClaimsFromNamespace(vm.Namespace)).
						WithTimeout(time.Minute).
						WithPolling(time.Second).
						ShouldNot(BeEmpty())
				})

				It("should garbage collect IPAMClaims after VM is deleted", func() {
					By("Delete VM and check ipam claims are gone")
					Expect(testenv.Client.Delete(context.Background(), vm)).To(Succeed())
					Eventually(testenv.IPAMClaimsFromNamespace(vm.Namespace)).
						WithTimeout(time.Minute).
						WithPolling(time.Second).
						Should(BeEmpty())
				})

				It("should garbage collect IPAMClaims after VM is foreground deleted", func() {
					By("Foreground delete VM and check ipam claims are gone")
					Expect(testenv.Client.Delete(context.Background(), vm, foregroundDeleteOptions())).To(Succeed())
					Eventually(testenv.IPAMClaimsFromNamespace(vm.Namespace)).
						WithTimeout(time.Minute).
						WithPolling(time.Second).
						Should(BeEmpty())
				})
			})

			It("should keep ips after restart", func() {
				Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(vmi), vmi)).To(Succeed())
				vmiIPsBeforeRestart := testenv.GetIPsFromVMIStatus(vmi, params.networkInterfaceName)
				Expect(vmiIPsBeforeRestart).NotTo(BeEmpty())
				vmiUUIDBeforeRestart := vmi.UID

				By("Re-starting the VM")
				output, err := exec.Command("virtctl", "restart", "-n", td.Namespace, vmi.Name).CombinedOutput()
				Expect(err).NotTo(HaveOccurred(), output)

				By("Wait for a new VMI to be re-started")
				Eventually(testenv.ThisVMI(vmi)).
					WithPolling(time.Second).
					WithTimeout(90 * time.Second).
					Should(testenv.BeRestarted(vmiUUIDBeforeRestart))

				By("Wait for VMI to be ready after restart")
				Eventually(testenv.ThisVMI(vmi)).
					WithPolling(time.Second).
					WithTimeout(5 * time.Minute).
					Should(testenv.ContainConditionVMIReady())

				Expect(testenv.ThisVMI(vmi)()).Should(testenv.MatchIPsAtInterfaceByName(params.networkInterfaceName, ConsistOf(vmiIPsBeforeRestart)))
			})
		})

		When("requested for a VM whose VMI has extra finalizers", func() {
			const testFinalizer = "testFinalizer"

			BeforeEach(func() {
				By("Adding VMI custom finalizer to control VMI deletion")
				vm.Spec.Template.ObjectMeta.Finalizers = []string{testFinalizer}

				By("Creating VM with secondary attachments")
				Expect(testenv.Client.Create(context.Background(), vm)).To(Succeed())

				By(fmt.Sprintf("Waiting for readiness at virtual machine %s", vm.Name))
				Eventually(testenv.ThisVMReadiness(vm)).
					WithPolling(time.Second).
					WithTimeout(5 * time.Minute).
					Should(BeTrue())

				By("Wait for IPAMClaim to get created")
				Eventually(testenv.IPAMClaimsFromNamespace(vm.Namespace)).
					WithTimeout(time.Minute).
					WithPolling(time.Second).
					ShouldNot(BeEmpty())

				Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(vmi), vmi)).To(Succeed())
				ips := testenv.GetIPsFromVMIStatus(vmi, params.networkInterfaceName)
				Expect(ips).NotTo(BeEmpty())
			})

			It("should garbage collect IPAMClaims after VM foreground deletion, only after VMI is gone", func() {
				By("Foreground delete the VM, and validate the IPAMClaim isnt deleted since VMI exists")
				Expect(testenv.Client.Delete(context.Background(), vm, foregroundDeleteOptions())).To(Succeed())
				Consistently(testenv.IPAMClaimsFromNamespace(vm.Namespace)).
					WithTimeout(time.Minute).
					WithPolling(time.Second).
					ShouldNot(BeEmpty())

				By("Remove the finalizer (all the other are already deleted in this stage)")
				patchData, err := removeFinalizersPatch()
				Expect(err).NotTo(HaveOccurred())
				Expect(testenv.Client.Patch(context.TODO(), vmi, client.RawPatch(types.MergePatchType, patchData))).To(Succeed())

				By("Check IPAMClaims are now deleted")
				Eventually(testenv.IPAMClaimsFromNamespace(vm.Namespace)).
					WithTimeout(time.Minute).
					WithPolling(time.Second).
					Should(BeEmpty())
			})
		})

		Context("and a virtual machine instance using it is also created", func() {
			BeforeEach(func() {
				By("Creating VMI using the nad")
				Expect(testenv.Client.Create(context.Background(), vmi)).To(Succeed())

				By(fmt.Sprintf("Waiting for readiness at virtual machine instance %s", vmi.Name))
				Eventually(testenv.ThisVMI(vmi)).
					WithPolling(time.Second).
					WithTimeout(5 * time.Minute).
					Should(testenv.ContainConditionVMIReady())

				By("Wait for IPAMClaim to get created")
				Eventually(testenv.IPAMClaimsFromNamespace(vm.Namespace)).
					WithTimeout(time.Minute).
					WithPolling(time.Second).
					ShouldNot(BeEmpty())

				Expect(testenv.ThisVMI(vmi)()).Should(testenv.MatchIPsAtInterfaceByName(params.networkInterfaceName, Not(BeEmpty())))
			})

			It("should keep ips after live migration", func() {
				Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(vmi), vmi)).To(Succeed())
				vmiIPsBeforeMigration := testenv.GetIPsFromVMIStatus(vmi, params.networkInterfaceName)
				Expect(vmiIPsBeforeMigration).NotTo(BeEmpty())

				testenv.LiveMigrateVirtualMachine(td.Namespace, vmi.Name)
				testenv.CheckLiveMigrationSucceeded(td.Namespace, vmi.Name)

				Expect(testenv.ThisVMI(vmi)()).Should(testenv.MatchIPsAtInterfaceByName(params.networkInterfaceName, ConsistOf(vmiIPsBeforeMigration)))
			})

			It("should garbage collect IPAMClaims after VMI deletion", func() {
				Expect(testenv.Client.Delete(context.Background(), vmi)).To(Succeed())
				Eventually(testenv.IPAMClaimsFromNamespace(vmi.Namespace)).
					WithTimeout(time.Minute).
					WithPolling(time.Second).
					Should(BeEmpty())
			})

			It("should garbage collect IPAMClaims after VMI foreground deletion", func() {
				Expect(testenv.Client.Delete(context.Background(), vmi, foregroundDeleteOptions())).To(Succeed())
				Eventually(testenv.IPAMClaimsFromNamespace(vmi.Namespace)).
					WithTimeout(time.Minute).
					WithPolling(time.Second).
					Should(BeEmpty())
			})
		})

	})
},
	Entry("secondary interfaces",
		testParams{
			role:                 roleSecondary,
			networkInterfaceName: secondaryLogicalNetworkInterfaceName,
			vmi:                  vmiWithMultus,
		}),
	Entry("primary UDN",
		testParams{
			role:                 rolePrimary,
			networkInterfaceName: primaryLogicalNetworkInterfaceName,
			vmi:                  vmiWithManagedTap,
		}),
)

var _ = Describe("Primary User Defined Network attachment", func() {
	var failureCount = 0

	JustAfterEach(func() {
		if CurrentSpecReport().Failed() {
			failureCount++
			logFailure(failureCount)
		}
	})

	When("the VM is created with a user defined MAC and IP addresses", func() {
		const (
			userDefinedIP  = "10.100.200.100"
			userDefinedMAC = "02:03:04:05:06:07"
		)

		var (
			vm  *kubevirtv1.VirtualMachine
			vmi *kubevirtv1.VirtualMachineInstance
			td  testenv.TestData
		)

		BeforeEach(func() {
			td = testenv.GenerateTestData()
			td.SetUp(primaryUDNNamespaceLabels())
			DeferCleanup(func() {
				td.TearDown()
			})

			// TODO: delete the code block below once OVN-Kubernetes provisions
			// the default network NAD
			const ovnKubernetesNamespace = "ovn-kubernetes"
			By("Ensuring the cluster default network attachment NetworkAttachmentDefinition exists")
			Expect(ensureDefaultNetworkAttachmentNAD(ovnKubernetesNamespace)).To(Succeed())
			// END code to be deleted block

			nad := testenv.GenerateLayer2WithSubnetNAD(nadName, td.Namespace, rolePrimary)
			By("Create NetworkAttachmentDefinition")
			Expect(testenv.Client.Create(context.Background(), nad)).To(Succeed())

			vmi = vmiWithManagedTap(td.Namespace)
			vm = testenv.NewVirtualMachine(
				vmi,
				testenv.WithRunning(),
				testenv.WithMACAddress(primaryLogicalNetworkInterfaceName, userDefinedMAC),
				testenv.WithStaticIPRequests(primaryLogicalNetworkInterfaceName, userDefinedIP),
			)
			Expect(testenv.Client.Create(context.Background(), vm)).To(Succeed())

			By(fmt.Sprintf("Waiting for readiness at virtual machine %s", vm.Name))
			Eventually(testenv.ThisVMReadiness(vm)).
				WithPolling(time.Second).
				WithTimeout(5 * time.Minute).
				Should(BeTrue())

			By("Wait for IPAMClaim to get created")
			Eventually(testenv.IPAMClaimsFromNamespace(vm.Namespace)).
				WithTimeout(time.Minute).
				WithPolling(time.Second).
				ShouldNot(BeEmpty())
		})

		It("should have the user provided MAC and IP addresses after creation", func() {
			Expect(testenv.ThisVMI(vmi)()).Should(testenv.MatchIPsAtInterfaceByName(
				primaryLogicalNetworkInterfaceName,
				ConsistOf(userDefinedIP),
			))
			Expect(testenv.ThisVMI(vmi)()).Should(testenv.MatchMACAddressAtInterfaceByName(
				primaryLogicalNetworkInterfaceName,
				userDefinedMAC,
			))
		})

		It("should keep the user provided MAC and IP addresses after live migration", func() {
			testenv.LiveMigrateVirtualMachine(td.Namespace, vm.Name)
			testenv.CheckLiveMigrationSucceeded(td.Namespace, vm.Name)

			Expect(testenv.ThisVMI(vmi)()).Should(testenv.MatchIPsAtInterfaceByName(
				primaryLogicalNetworkInterfaceName,
				ConsistOf(userDefinedIP),
			))
			Expect(testenv.ThisVMI(vmi)()).Should(testenv.MatchMACAddressAtInterfaceByName(
				primaryLogicalNetworkInterfaceName,
				userDefinedMAC,
			))
		})
	})

})

func foregroundDeleteOptions() *client.DeleteOptions {
	foreground := metav1.DeletePropagationForeground
	return &client.DeleteOptions{
		PropagationPolicy: &foreground,
	}
}

func removeFinalizersPatch() ([]byte, error) {
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"finalizers": []string{},
		},
	}
	return json.Marshal(patch)
}

func vmiWithMultus(namespace string) *kubevirtv1.VirtualMachineInstance {
	interfaceName := secondaryLogicalNetworkInterfaceName
	return testenv.NewVirtualMachineInstance(
		namespace,
		testenv.WithMemory("128Mi"),
		testenv.WithInterface(kubevirtv1.Interface{
			Name: interfaceName,
			InterfaceBindingMethod: kubevirtv1.InterfaceBindingMethod{
				Bridge: &kubevirtv1.InterfaceBridge{},
			},
		}),
		testenv.WithNetwork(kubevirtv1.Network{

			Name: interfaceName,
			NetworkSource: kubevirtv1.NetworkSource{
				Multus: &kubevirtv1.MultusNetwork{
					NetworkName: nadName,
				},
			},
		}),
	)
}

func vmiWithManagedTap(namespace string) *kubevirtv1.VirtualMachineInstance {
	const (
		interfaceName        = primaryLogicalNetworkInterfaceName
		cloudInitNetworkData = `
version: 2
ethernets:
  eth0:
    dhcp4: true`
	)
	return testenv.NewVirtualMachineInstance(
		namespace,
		testenv.WithMemory("1024Mi"),
		testenv.WithInterface(kubevirtv1.Interface{
			Name: interfaceName,
			Binding: &kubevirtv1.PluginBinding{
				Name: "l2bridge",
			},
		}),
		testenv.WithNetwork(kubevirtv1.Network{
			Name: interfaceName,
			NetworkSource: kubevirtv1.NetworkSource{
				Pod: &kubevirtv1.PodNetwork{},
			},
		}),
		testenv.WithCloudInitNoCloudVolume(cloudInitNetworkData),
	)
}

func logFailure(failureCount int) {
	By(fmt.Sprintf("Test failed, collecting logs and artifacts, failure count %d, process %d", failureCount, GinkgoParallelProcess()))

	logCommand([]string{"get", "pods", "-A"}, "pods", failureCount)
	logCommand([]string{"get", "vm", "-A", "-oyaml"}, "vms", failureCount)
	logCommand([]string{"get", "vmi", "-A", "-oyaml"}, "vmis", failureCount)
	logCommand([]string{"get", "ipamclaims", "-A", "-oyaml"}, "ipamclaims", failureCount)
	logCommand([]string{"get", "net-attach-def", "-A", "-oyaml"}, "network-attachments", failureCount)
	logCommand([]string{"get", "namespaces", "-A", "-oyaml"}, "namespaces", failureCount)
	logOvnPods(failureCount)
}

func primaryUDNNamespaceLabels() map[string]string {
	return map[string]string{
		"k8s.ovn.org/primary-user-defined-network": "",
	}
}

func ensureDefaultNetworkAttachmentNAD(namespace string) error {
	defaultNetNad := &nadv1.NetworkAttachmentDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: namespace,
		},
		Spec: nadv1.NetworkAttachmentDefinitionSpec{
			Config: "{\"cniVersion\": \"0.4.0\", \"name\": \"default\", \"type\": \"ovn-k8s-cni-overlay\"}",
		},
	}

	if err := testenv.Client.Create(
		context.Background(),
		defaultNetNad,
	); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	return nil
}

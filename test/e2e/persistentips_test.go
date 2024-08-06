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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	kubevirtv1 "kubevirt.io/api/core/v1"

	testenv "github.com/kubevirt/ipam-extensions/test/env"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Persistent IPs", func() {
	When("network attachment definition created with allowPersistentIPs=true", func() {
		var (
			td                   testenv.TestData
			networkInterfaceName = "multus"
			vm                   *kubevirtv1.VirtualMachine
			vmi                  *kubevirtv1.VirtualMachineInstance
			nad                  *nadv1.NetworkAttachmentDefinition
		)
		BeforeEach(func() {
			td = testenv.GenerateTestData()
			td.SetUp()
			DeferCleanup(func() {
				td.TearDown()
			})

			nad = testenv.GenerateLayer2WithSubnetNAD(td.Namespace)
			vmi = testenv.GenerateAlpineWithMultusVMI(td.Namespace, networkInterfaceName, nad.Name)
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

				Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(vmi), vmi)).To(Succeed())

				Expect(vmi.Status.Interfaces).NotTo(BeEmpty())
				Expect(vmi.Status.Interfaces[0].IPs).NotTo(BeEmpty())
			})

			It("should keep ips after live migration", func() {
				vmiIPsBeforeMigration := vmi.Status.Interfaces[0].IPs

				testenv.LiveMigrateVirtualMachine(td.Namespace, vm.Name)
				testenv.CheckLiveMigrationSucceeded(td.Namespace, vm.Name)

				By("Wait for VMI to be ready after live migration")
				Eventually(testenv.ThisVMI(vmi)).
					WithPolling(time.Second).
					WithTimeout(5 * time.Minute).
					Should(testenv.ContainConditionVMIReady())

				Expect(testenv.ThisVMI(vmi)()).Should(testenv.MatchIPsAtInterfaceByName(networkInterfaceName, ConsistOf(vmiIPsBeforeMigration)))

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
				vmiIPsBeforeRestart := vmi.Status.Interfaces[0].IPs
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

				Expect(testenv.ThisVMI(vmi)()).Should(testenv.MatchIPsAtInterfaceByName(networkInterfaceName, ConsistOf(vmiIPsBeforeRestart)))
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

				Expect(vmi.Status.Interfaces).NotTo(BeEmpty())
				Expect(vmi.Status.Interfaces[0].IPs).NotTo(BeEmpty())
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

				Expect(testenv.Client.Get(context.Background(), client.ObjectKeyFromObject(vmi), vmi)).To(Succeed())

				Expect(vmi.Status.Interfaces).NotTo(BeEmpty())
				Expect(vmi.Status.Interfaces[0].IPs).NotTo(BeEmpty())
			})

			It("should keep ips after live migration", func() {
				vmiIPsBeforeMigration := vmi.Status.Interfaces[0].IPs

				testenv.LiveMigrateVirtualMachine(td.Namespace, vmi.Name)
				testenv.CheckLiveMigrationSucceeded(td.Namespace, vmi.Name)

				Expect(testenv.ThisVMI(vmi)()).Should(testenv.MatchIPsAtInterfaceByName(networkInterfaceName, ConsistOf(vmiIPsBeforeMigration)))

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

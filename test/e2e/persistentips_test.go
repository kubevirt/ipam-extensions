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
	"fmt"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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
				By("Creating VM using the nad")
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

			It("should garbage collect IPAMClaims after virtual machine deletion", func() {
				Expect(testenv.Client.Delete(context.Background(), vm)).To(Succeed())
				Eventually(testenv.IPAMClaimsFromNamespace(vm.Namespace)).
					WithTimeout(time.Minute).
					WithPolling(time.Second).
					Should(BeEmpty())
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

			It("should garbage collect IPAMClaims after virtual machine deletion", func() {
				Expect(testenv.Client.Delete(context.Background(), vmi)).To(Succeed())
				Eventually(testenv.IPAMClaimsFromNamespace(vmi.Namespace)).
					WithTimeout(time.Minute).
					WithPolling(time.Second).
					Should(BeEmpty())
			})
		})

	})
})

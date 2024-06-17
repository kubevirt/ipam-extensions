package env

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubevirtv1 "kubevirt.io/api/core/v1"
)

func LiveMigrateVirtualMachine(vmNamespace, vmName string) {
	GinkgoHelper()
	vmimCreationRetries := 0
	Eventually(func() error {
		if vmimCreationRetries > 0 {
			// retry due to unknown issue where kubevirt webhook gets stuck reading the request body
			// https://github.com/ovn-org/ovn-kubernetes/issues/3902#issuecomment-1750257559
			By(fmt.Sprintf("Retrying vmim %s creation", vmName))
		}
		vmim := &kubevirtv1.VirtualMachineInstanceMigration{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:    vmNamespace,
				GenerateName: vmName,
			},
			Spec: kubevirtv1.VirtualMachineInstanceMigrationSpec{
				VMIName: vmName,
			},
		}
		err := Client.Create(context.Background(), vmim)
		vmimCreationRetries++
		return err
	}).WithPolling(time.Second).WithTimeout(time.Minute).Should(Succeed())
}

func CheckLiveMigrationSucceeded(vmNamespace, vmName string) {
	GinkgoHelper()
	By("Wait for VM live migration")
	vmi := &kubevirtv1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: vmNamespace,
			Name:      vmName,
		},
	}
	err := Client.Get(context.TODO(), client.ObjectKeyFromObject(vmi), vmi)
	Expect(err).ToNot(HaveOccurred(), "should success retrieving vmi")
	currentNode := vmi.Status.NodeName

	Eventually(func() *kubevirtv1.VirtualMachineInstanceMigrationState {
		err := Client.Get(context.TODO(), client.ObjectKeyFromObject(vmi), vmi)
		Expect(err).ToNot(HaveOccurred())
		return vmi.Status.MigrationState
	}).WithPolling(time.Second).WithTimeout(5*time.Minute).ShouldNot(BeNil(), "should have a MigrationState")
	Eventually(func() string {
		err := Client.Get(context.TODO(), client.ObjectKeyFromObject(vmi), vmi)
		Expect(err).ToNot(HaveOccurred())
		return vmi.Status.MigrationState.TargetNode
	}).WithPolling(time.Second).WithTimeout(2*time.Minute).ShouldNot(Equal(currentNode), "should refresh MigrationState")
	Eventually(func() bool {
		err := Client.Get(context.TODO(), client.ObjectKeyFromObject(vmi), vmi)
		Expect(err).ToNot(HaveOccurred())
		return vmi.Status.MigrationState.Completed
	}).WithPolling(time.Second).WithTimeout(5*time.Minute).Should(BeTrue(), "should complete migration")
	err = Client.Get(context.TODO(), client.ObjectKeyFromObject(vmi), vmi)
	Expect(err).ToNot(HaveOccurred(), "should success retrieving vmi after migration")
	Expect(vmi.Status.MigrationState.Failed).To(BeFalse(), func() string {
		vmiJSON, err := json.Marshal(vmi)
		if err != nil {
			return fmt.Sprintf("failed marshaling migrated VM: %v", vmiJSON)
		}
		return fmt.Sprintf("should live migrate successfully: %s", string(vmiJSON))
	})
}

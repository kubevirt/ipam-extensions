/*
Copyright 2024.

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

package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	ctrl "sigs.k8s.io/controller-runtime"

	testenv "github.com/kubevirt/ipam-extensions/test/env"
)

const logsDir = ".output" // ./test/e2e/.output

var _ = BeforeSuite(func() {
	ctrl.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	testenv.Start()
})

// Run e2e tests using the Ginkgo runner.
func TestE2E(t *testing.T) {
	os.RemoveAll(logsDir)
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		panic(fmt.Sprintf("Error creating directory: %v", err))
	}

	RegisterFailHandler(Fail)
	RunSpecs(t, "kubevirt-ipam-controller e2e suite")
}

func logCommand(args []string, topic string, failureCount int) {
	stdout, stderr, err := kubectl(args...)
	if err != nil {
		fmt.Printf("Error running command kubectl %v, err %v, stderr %s\n", args, err, stderr)
		return
	}

	fileName := fmt.Sprintf(logsDir+"/%d_%s.log", failureCount, topic)
	file, err := os.Create(fileName)
	if err != nil {
		fmt.Printf("Error running command %v, err %v\n", args, err)
		return
	}
	defer file.Close()

	fmt.Fprint(file, fmt.Sprintf("kubectl %s\n%s\n", strings.Join(args, " "), stdout))
}

func logOvnPods(failureCount int) {
	args := []string{"get", "pods", "-n", "ovn-kubernetes", "--no-headers", "-o=custom-columns=NAME:.metadata.name"}
	ovnK8sPods, stderr, err := kubectl(args...)
	if err != nil {
		fmt.Printf("Error running command kubectl %v, stderr %s, err %v\n", args, stderr, err)
		return
	}

	podContainers := []struct {
		PodPrefix  string
		Containers []string
	}{
		{PodPrefix: "ovnkube-control-plane", Containers: []string{"ovnkube-cluster-manager"}},
		{PodPrefix: "ovnkube-node", Containers: []string{"ovnkube-controller"}},
	}

	for _, pod := range strings.Split(ovnK8sPods, "\n") {
		for _, pc := range podContainers {
			if strings.HasPrefix(pod, pc.PodPrefix) {
				for _, container := range pc.Containers {
					logCommand([]string{"logs", "-n", "ovn-kubernetes", pod, "-c", container}, pod+"_"+container, failureCount)
				}
			}
		}
	}
}

func kubectl(command ...string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(os.Getenv("KUBECTL"), command...)
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

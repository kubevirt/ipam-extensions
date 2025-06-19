package ips

import (
	"encoding/json"
	virtv1 "kubevirt.io/api/core/v1"

	"github.com/kubevirt/ipam-extensions/pkg/config"
)

func VmiInterfaceIPRequests(vmi *virtv1.VirtualMachineInstance, podNetworkName string) []string {
	var addrs map[string][]string
	ipRequests := vmi.Annotations[config.IPRequestsAnnotation]
	if err := json.Unmarshal([]byte(ipRequests), &addrs); err != nil {
		return nil
	}

	return addrs[podNetworkName]
}

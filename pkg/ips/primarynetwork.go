package ips

import (
	"encoding/json"
	"fmt"
	"strings"

	virtv1 "kubevirt.io/api/core/v1"

	"github.com/kubevirt/ipam-extensions/pkg/config"
)

func VmiInterfaceIPRequests(
	vmi *virtv1.VirtualMachineInstance,
	ifaceName string,
	netConfig *config.RelevantConfig,
) ([]string, error) {
	var addrs map[string][]string
	ipRequests, doesVMHaveIPRequests := vmi.Annotations[config.IPRequestsAnnotation]
	if !doesVMHaveIPRequests {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(ipRequests), &addrs); err != nil {
		return nil, err
	}

	ipv4Subnets, ipv6Subnets, err := SeparateSubnetsByFamily(netConfig.Subnets)
	if err != nil {
		return nil, err
	}

	primaryNetIPs := addrs[ifaceName]
	result := make([]string, 0, len(primaryNetIPs))

	for _, ip := range primaryNetIPs {
		var targetSubnet string

		if IsIPv4(ip) {
			if len(ipv4Subnets) == 0 {
				return nil, fmt.Errorf("no IPv4 subnet configured for IPv4 IP request: %s", ip)
			}
			targetSubnet = ipv4Subnets[0]
		} else if IsIPv6(ip) {
			if len(ipv6Subnets) == 0 {
				return nil, fmt.Errorf("no IPv6 subnet configured for IPv6 IP request: %s", ip)
			}
			targetSubnet = ipv6Subnets[0]
		} else {
			return nil, fmt.Errorf("invalid IP address format: %s", ip)
		}

		// Extract netmask from subnet
		splitSubnet := strings.Split(targetSubnet, "/")
		if len(splitSubnet) != 2 {
			return nil, fmt.Errorf("invalid subnet format: %s", targetSubnet)
		}
		subnetNetmask := splitSubnet[1]
		result = append(result, fmt.Sprintf("%s/%s", ip, subnetNetmask))
	}

	return result, nil
}

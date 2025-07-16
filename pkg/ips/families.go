package ips

import (
	"fmt"
	"net"
	"strings"
)

func SeparateSubnetsByFamily(subnets string) ([]string, []string, error) {
	var ipv4Subnets []string
	var ipv6Subnets []string

	if subnets == "" {
		return nil, nil, nil
	}

	subnetList := strings.Split(subnets, ",")
	for _, subnet := range subnetList {
		subnet = strings.TrimSpace(subnet)
		if subnet == "" {
			continue
		}

		// Parse the subnet to determine its IP family
		_, ipNet, err := net.ParseCIDR(subnet)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid subnet format: %s", subnet)
		}

		if ipNet.IP.To4() != nil {
			ipv4Subnets = append(ipv4Subnets, subnet)
		} else {
			ipv6Subnets = append(ipv6Subnets, subnet)
		}
	}

	return ipv4Subnets, ipv6Subnets, nil
}

func IsIPv4(ip string) bool {
	parsedIP := net.ParseIP(ip)
	return parsedIP != nil && parsedIP.To4() != nil
}

func IsIPv6(ip string) bool {
	parsedIP := net.ParseIP(ip)
	return parsedIP != nil && parsedIP.To4() == nil
}

package config

import (
	"encoding/json"
	"fmt"
)

type NetworkRole string

const (
	NetworkRolePrimary         NetworkRole = "primary"
	IPRequestsAnnotation       string      = "network.kubevirt.io/addresses"
	MultusDefaultNetAnnotation string      = "v1.multus-cni.io/default-network"
)

const OVNPrimaryNetworkIPAMClaimAnnotation = "k8s.ovn.org/primary-udn-ipamclaim"

type RelevantConfig struct {
	Name               string      `json:"name"`
	AllowPersistentIPs bool        `json:"allowPersistentIPs,omitempty"`
	Role               NetworkRole `json:"role,omitempty"`
}

func NewConfig(nadSpec string) (*RelevantConfig, error) {
	nadConfig := &RelevantConfig{}
	if nadSpec == "" {
		return nadConfig, nil
	}
	if err := json.Unmarshal([]byte(nadSpec), nadConfig); err != nil {
		return nil, fmt.Errorf("failed to extract CNI configuration from NAD: %w", err)
	}
	return nadConfig, nil
}

package config

import (
	"encoding/json"
	"fmt"
)

type RelevantConfig struct {
	Name               string `json:"name"`
	AllowPersistentIPs bool   `json:"allowPersistentIPs,omitempty"`
	Role               string `json:"role,omitempty"`
}

func NewConfig(nadSpec string) (*RelevantConfig, error) {
	nadConfig := &RelevantConfig{}
	if err := json.Unmarshal([]byte(nadSpec), nadConfig); err != nil {
		return nil, fmt.Errorf("failed to extract CNI configuration from NAD: %w", err)
	}
	return nadConfig, nil
}

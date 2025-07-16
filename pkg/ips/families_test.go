package ips

import (
	"reflect"
	"strings"
	"testing"
)

func TestSeparateSubnetsByFamily(t *testing.T) {
	tests := []struct {
		name          string
		subnets       string
		expectedIPv4  []string
		expectedIPv6  []string
		expectedError bool
		errorContains string
	}{
		{
			name:          "empty subnets string",
			subnets:       "",
			expectedIPv4:  nil,
			expectedIPv6:  nil,
			expectedError: false,
		},
		{
			name:          "single IPv4 subnet",
			subnets:       "192.168.1.0/24",
			expectedIPv4:  []string{"192.168.1.0/24"},
			expectedIPv6:  []string{},
			expectedError: false,
		},
		{
			name:          "single IPv6 subnet",
			subnets:       "2001:db8::/32",
			expectedIPv4:  []string{},
			expectedIPv6:  []string{"2001:db8::/32"},
			expectedError: false,
		},
		{
			name:          "mixed IPv4 and IPv6 subnets",
			subnets:       "192.168.1.0/24,2001:db8::/32,10.0.0.0/8",
			expectedIPv4:  []string{"192.168.1.0/24", "10.0.0.0/8"},
			expectedIPv6:  []string{"2001:db8::/32"},
			expectedError: false,
		},
		{
			name:          "subnets with whitespace",
			subnets:       " 192.168.1.0/24 , 2001:db8::/32 ",
			expectedIPv4:  []string{"192.168.1.0/24"},
			expectedIPv6:  []string{"2001:db8::/32"},
			expectedError: false,
		},
		{
			name:          "subnets with empty entries",
			subnets:       "192.168.1.0/24,,2001:db8::/32,",
			expectedIPv4:  []string{"192.168.1.0/24"},
			expectedIPv6:  []string{"2001:db8::/32"},
			expectedError: false,
		},
		{
			name:          "invalid subnet format",
			subnets:       "192.168.1.0/24,invalid-subnet,2001:db8::/32",
			expectedIPv4:  nil,
			expectedIPv6:  nil,
			expectedError: true,
			errorContains: "invalid subnet format: invalid-subnet",
		},
		{
			name:          "IPv4 subnet without CIDR notation",
			subnets:       "192.168.1.0",
			expectedIPv4:  nil,
			expectedIPv6:  nil,
			expectedError: true,
			errorContains: "invalid subnet format: 192.168.1.0",
		},
		{
			name:          "IPv6 subnet without CIDR notation",
			subnets:       "2001:db8::",
			expectedIPv4:  nil,
			expectedIPv6:  nil,
			expectedError: true,
			errorContains: "invalid subnet format: 2001:db8::",
		},
		{
			name:          "multiple IPv4 subnets only",
			subnets:       "192.168.1.0/24,10.0.0.0/8,172.16.0.0/12",
			expectedIPv4:  []string{"192.168.1.0/24", "10.0.0.0/8", "172.16.0.0/12"},
			expectedIPv6:  []string{},
			expectedError: false,
		},
		{
			name:          "multiple IPv6 subnets only",
			subnets:       "2001:db8::/32,fd00::/8,::/0",
			expectedIPv4:  []string{},
			expectedIPv6:  []string{"2001:db8::/32", "fd00::/8", "::/0"},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ipv4Subnets, ipv6Subnets, err := SeparateSubnetsByFamily(tt.subnets)

			if tt.expectedError {
				if err == nil {
					t.Errorf("expected error but got none")
					return
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("error message '%s' does not contain expected substring '%s'", err.Error(), tt.errorContains)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			// Handle empty slice vs nil comparison
			if len(tt.expectedIPv4) == 0 && len(ipv4Subnets) == 0 {
				// Both are empty, consider them equal
			} else if !reflect.DeepEqual(ipv4Subnets, tt.expectedIPv4) {
				t.Errorf("IPv4 subnets mismatch: got %v, want %v", ipv4Subnets, tt.expectedIPv4)
			}

			// Handle empty slice vs nil comparison
			if len(tt.expectedIPv6) == 0 && len(ipv6Subnets) == 0 {
				// Both are empty, consider them equal
			} else if !reflect.DeepEqual(ipv6Subnets, tt.expectedIPv6) {
				t.Errorf("IPv6 subnets mismatch: got %v, want %v", ipv6Subnets, tt.expectedIPv6)
			}
		})
	}
}

type ipTestCase struct {
	name     string
	ip       string
	expected bool
}

func runIPTests(t *testing.T, testFunc func(string) bool, funcName string, tests []ipTestCase) {
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := testFunc(tt.ip)
			if result != tt.expected {
				t.Errorf("%s(%s) = %v, want %v", funcName, tt.ip, result, tt.expected)
			}
		})
	}
}

func TestIsIPv4(t *testing.T) {
	tests := []ipTestCase{
		{
			name:     "valid IPv4 address",
			ip:       "192.168.1.1",
			expected: true,
		},
		{
			name:     "valid IPv4 address - localhost",
			ip:       "127.0.0.1",
			expected: true,
		},
		{
			name:     "valid IPv4 address - zero",
			ip:       "0.0.0.0",
			expected: true,
		},
		{
			name:     "valid IPv4 address - broadcast",
			ip:       "255.255.255.255",
			expected: true,
		},
		{
			name:     "IPv6 address",
			ip:       "2001:db8::1",
			expected: false,
		},
		{
			name:     "IPv6 localhost",
			ip:       "::1",
			expected: false,
		},
		{
			name:     "IPv4-mapped IPv6 address",
			ip:       "::ffff:192.168.1.1",
			expected: true, // Go's net package treats this as IPv4
		},
		{
			name:     "invalid IP address",
			ip:       "invalid-ip",
			expected: false,
		},
		{
			name:     "empty string",
			ip:       "",
			expected: false,
		},
		{
			name:     "IPv4 with invalid octets",
			ip:       "256.256.256.256",
			expected: false,
		},
		{
			name:     "IPv4 with too few octets",
			ip:       "192.168.1",
			expected: false,
		},
		{
			name:     "IPv4 with too many octets",
			ip:       "192.168.1.1.1",
			expected: false,
		},
	}

	runIPTests(t, IsIPv4, "IsIPv4", tests)
}

func TestIsIPv6(t *testing.T) {
	tests := []ipTestCase{
		{
			name:     "valid IPv6 address",
			ip:       "2001:db8::1",
			expected: true,
		},
		{
			name:     "IPv6 localhost",
			ip:       "::1",
			expected: true,
		},
		{
			name:     "IPv6 full address",
			ip:       "2001:0db8:85a3:0000:0000:8a2e:0370:7334",
			expected: true,
		},
		{
			name:     "IPv6 compressed address",
			ip:       "2001:db8:85a3::8a2e:370:7334",
			expected: true,
		},
		{
			name:     "IPv6 zero address",
			ip:       "::",
			expected: true,
		},
		{
			name:     "IPv4-mapped IPv6 address",
			ip:       "::ffff:192.168.1.1",
			expected: false, // Go's net package treats this as IPv4, so IsIPv6 returns false
		},
		{
			name:     "IPv4 address",
			ip:       "192.168.1.1",
			expected: false,
		},
		{
			name:     "IPv4 localhost",
			ip:       "127.0.0.1",
			expected: false,
		},
		{
			name:     "invalid IP address",
			ip:       "invalid-ip",
			expected: false,
		},
		{
			name:     "empty string",
			ip:       "",
			expected: false,
		},
		{
			name:     "IPv6 with invalid characters",
			ip:       "2001:db8::g1",
			expected: false,
		},
		{
			name:     "IPv6 with too many groups",
			ip:       "2001:db8:85a3:0000:0000:8a2e:0370:7334:extra",
			expected: false,
		},
	}

	runIPTests(t, IsIPv6, "IsIPv6", tests)
}

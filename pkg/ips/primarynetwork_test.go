package ips

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	virtv1 "kubevirt.io/api/core/v1"

	"github.com/kubevirt/ipam-extensions/pkg/config"
)

var _ = Describe("VmiInterfaceIPRequests", func() {
	var (
		vmi        *virtv1.VirtualMachineInstance
		primaryNet *config.RelevantConfig
	)

	BeforeEach(func() {
		vmi = &virtv1.VirtualMachineInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-vm",
				Namespace: "default",
			},
		}

		primaryNet = &config.RelevantConfig{
			Name:               "primarynet",
			AllowPersistentIPs: true,
			Role:               config.NetworkRolePrimary,
			Subnets:            "192.168.0.0/16,fd12:1234::/64",
		}
	})

	When("VMI has no IP requests annotation", func() {
		It("should return nil when no IP requests are present", func() {
			result, err := VmiInterfaceIPRequests(vmi, "podnet", primaryNet)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeNil())
		})
	})

	When("VMI has IP requests annotation", func() {
		Context("with valid IPv4 and IPv6 addresses", func() {
			BeforeEach(func() {
				ipRequests := map[string][]string{
					"podnet": {"192.168.1.10", "fd20:1234::200"},
				}
				rawRequests, _ := json.Marshal(ipRequests)
				vmi.Annotations = map[string]string{
					config.IPRequestsAnnotation: string(rawRequests),
				}
			})

			It("should return formatted IP addresses with subnet masks", func() {
				result, err := VmiInterfaceIPRequests(vmi, "podnet", primaryNet)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(ConsistOf("192.168.1.10/16", "fd20:1234::200/64"))
			})
		})

		Context("with only IPv4 addresses", func() {
			BeforeEach(func() {
				ipRequests := map[string][]string{
					"podnet": {"192.168.1.10", "192.168.1.20"},
				}
				rawRequests, _ := json.Marshal(ipRequests)
				vmi.Annotations = map[string]string{
					config.IPRequestsAnnotation: string(rawRequests),
				}
			})

			It("should return formatted IPv4 addresses with correct subnet masks", func() {
				result, err := VmiInterfaceIPRequests(vmi, "podnet", primaryNet)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(ConsistOf("192.168.1.10/16", "192.168.1.20/16"))
			})
		})

		Context("with only IPv6 addresses", func() {
			BeforeEach(func() {
				primaryNet.Subnets = "fd12:1234::/64"
				ipRequests := map[string][]string{
					"podnet": {"fd20:1234::200", "fd20:1234::300"},
				}
				rawRequests, _ := json.Marshal(ipRequests)
				vmi.Annotations = map[string]string{
					config.IPRequestsAnnotation: string(rawRequests),
				}
			})

			It("should return formatted IPv6 addresses with correct subnet masks", func() {
				result, err := VmiInterfaceIPRequests(vmi, "podnet", primaryNet)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(ConsistOf("fd20:1234::200/64", "fd20:1234::300/64"))
			})
		})

		Context("with different network names", func() {
			BeforeEach(func() {
				ipRequests := map[string][]string{
					"podnet":   {"192.168.1.10"},
					"othernet": {"192.168.2.10"},
				}
				rawRequests, _ := json.Marshal(ipRequests)
				vmi.Annotations = map[string]string{
					config.IPRequestsAnnotation: string(rawRequests),
				}
			})

			It("should return IPs only for the specified network", func() {
				result, err := VmiInterfaceIPRequests(vmi, "podnet", primaryNet)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(ConsistOf("192.168.1.10/16"))
			})

			It("should return nil for non-existent network", func() {
				result, err := VmiInterfaceIPRequests(vmi, "nonexistent", primaryNet)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(BeEmpty())
			})
		})

		Context("with error scenarios", func() {
			It("should return error for invalid JSON in annotation", func() {
				vmi.Annotations = map[string]string{
					config.IPRequestsAnnotation: "invalid json",
				}

				result, err := VmiInterfaceIPRequests(vmi, "podnet", primaryNet)
				Expect(err).To(HaveOccurred())
				Expect(result).To(BeNil())
			})

			It("should return error for IPv4 address when no IPv4 subnets configured", func() {
				primaryNet.Subnets = "fd12:1234::/64" // Only IPv6
				ipRequests := map[string][]string{
					"podnet": {"192.168.1.10"},
				}
				rawRequests, _ := json.Marshal(ipRequests)
				vmi.Annotations = map[string]string{
					config.IPRequestsAnnotation: string(rawRequests),
				}

				result, err := VmiInterfaceIPRequests(vmi, "podnet", primaryNet)
				Expect(err).To(MatchError(ContainSubstring("no IPv4 subnet configured")))
				Expect(result).To(BeNil())
			})

			It("should return error for IPv6 address when no IPv6 subnets configured", func() {
				primaryNet.Subnets = "192.168.0.0/16" // Only IPv4
				ipRequests := map[string][]string{
					"podnet": {"fd20:1234::200"},
				}
				rawRequests, _ := json.Marshal(ipRequests)
				vmi.Annotations = map[string]string{
					config.IPRequestsAnnotation: string(rawRequests),
				}

				result, err := VmiInterfaceIPRequests(vmi, "podnet", primaryNet)
				Expect(err).To(MatchError(ContainSubstring("no IPv6 subnet configured")))
				Expect(result).To(BeNil())
			})

			It("should return error for invalid IP address format", func() {
				ipRequests := map[string][]string{
					"podnet": {"not.an.ip.address"},
				}
				rawRequests, _ := json.Marshal(ipRequests)
				vmi.Annotations = map[string]string{
					config.IPRequestsAnnotation: string(rawRequests),
				}

				result, err := VmiInterfaceIPRequests(vmi, "podnet", primaryNet)
				Expect(err).To(MatchError(ContainSubstring("invalid IP address format")))
				Expect(result).To(BeNil())
			})

			It("should return error for invalid subnet format", func() {
				primaryNet.Subnets = "192.168.0.0" // Missing CIDR notation
				ipRequests := map[string][]string{
					"podnet": {"192.168.1.10"},
				}
				rawRequests, _ := json.Marshal(ipRequests)
				vmi.Annotations = map[string]string{
					config.IPRequestsAnnotation: string(rawRequests),
				}

				result, err := VmiInterfaceIPRequests(vmi, "podnet", primaryNet)
				Expect(err).To(MatchError(ContainSubstring("invalid subnet format")))
				Expect(result).To(BeNil())
			})
		})

		Context("with multiple subnets of same family", func() {
			BeforeEach(func() {
				primaryNet.Subnets = "192.168.0.0/16,10.0.0.0/8,fd12:1234::/64,fd34:5678::/48"
			})

			It("should use the first IPv4 subnet for IPv4 addresses", func() {
				ipRequests := map[string][]string{
					"podnet": {"192.168.1.10"},
				}
				rawRequests, _ := json.Marshal(ipRequests)
				vmi.Annotations = map[string]string{
					config.IPRequestsAnnotation: string(rawRequests),
				}

				result, err := VmiInterfaceIPRequests(vmi, "podnet", primaryNet)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(ConsistOf("192.168.1.10/16"))
			})

			It("should use the first IPv6 subnet for IPv6 addresses", func() {
				ipRequests := map[string][]string{
					"podnet": {"fd20:1234::200"},
				}
				rawRequests, _ := json.Marshal(ipRequests)
				vmi.Annotations = map[string]string{
					config.IPRequestsAnnotation: string(rawRequests),
				}

				result, err := VmiInterfaceIPRequests(vmi, "podnet", primaryNet)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(ConsistOf("fd20:1234::200/64"))
			})
		})
	})
})

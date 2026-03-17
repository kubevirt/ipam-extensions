/*
Copyright 2026.

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

package config_test

import (
	"crypto/tls"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kubevirt/ipam-extensions/pkg/config"
)

func TestConfigTLS(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "config TLS suite")
}

type flags struct {
	minVersion string
	ciphers    string
	curves     string
}

var _ = Describe("ParseTLSOptions", func() {
	DescribeTable("should fail, given",
		func(i flags) {
			optsFn, err := config.ParseTLSOptions(i.minVersion, i.ciphers, i.curves)
			Expect(err).To(HaveOccurred())
			Expect(optsFn).To(BeNil())
		},
		Entry("invalid TLS min version",
			flags{
				minVersion: "9.0",
				ciphers:    "TLS_AES_128_GCM_SHA256",
				curves:     "X25519",
			},
		),
		Entry("invalid TLS cipher suite",
			flags{
				ciphers:    "horcrux",
				minVersion: "VersionTLS12",
				curves:     "X25519",
			},
		),
		Entry("invalid TLS curve preference",
			flags{
				curves:     "straight",
				minVersion: "VersionTLS12",
				ciphers:    "TLS_AES_128_GCM_SHA256",
			},
		),
	)

	DescribeTable("should succeed, given",
		func(i flags, expectedTLSConf *tls.Config) {
			optsFn, err := config.ParseTLSOptions(i.minVersion, i.ciphers, i.curves)
			Expect(err).ToNot(HaveOccurred())
			testTlsConfig := &tls.Config{}
			optsFn(testTlsConfig)
			Expect(testTlsConfig).To(Equal(expectedTLSConf))
		},
		Entry("no input", flags{}, &tls.Config{}),
		Entry("min version",
			flags{minVersion: "VersionTLS12"},
			&tls.Config{MinVersion: tls.VersionTLS12},
		),
		Entry("cipher suites",
			flags{ciphers: "TLS_AES_128_GCM_SHA256"},
			&tls.Config{CipherSuites: []uint16{tls.TLS_AES_128_GCM_SHA256}},
		),
		Entry("curve preferences",
			flags{curves: "X25519"},
			&tls.Config{CurvePreferences: []tls.CurveID{tls.X25519}},
		),
		Entry("min version & ciphers",
			flags{
				minVersion: "VersionTLS12",
				ciphers:    "TLS_AES_128_GCM_SHA256",
			},
			&tls.Config{
				MinVersion:   tls.VersionTLS12,
				CipherSuites: []uint16{tls.TLS_AES_128_GCM_SHA256},
			},
		),
		Entry("min version & curve preferences",
			flags{
				minVersion: "VersionTLS12",
				curves:     "X25519",
			},
			&tls.Config{
				MinVersion:       tls.VersionTLS12,
				CurvePreferences: []tls.CurveID{tls.X25519},
			},
		),
		Entry("ciphers & curves preferences",
			flags{
				ciphers: "TLS_AES_128_GCM_SHA256",
				curves:  "X25519",
			},
			&tls.Config{
				CipherSuites:     []uint16{tls.TLS_AES_128_GCM_SHA256},
				CurvePreferences: []tls.CurveID{tls.X25519},
			},
		),
		Entry("min version, ciphers and curves preferences",
			flags{
				minVersion: "VersionTLS12",
				ciphers:    "TLS_AES_128_GCM_SHA256",
				curves:     "X25519",
			},
			&tls.Config{
				MinVersion:       tls.VersionTLS12,
				CipherSuites:     []uint16{tls.TLS_AES_128_GCM_SHA256},
				CurvePreferences: []tls.CurveID{tls.X25519},
			},
		),
		Entry("min version, multiple cipher suites and multiple ciphers",
			flags{
				minVersion: "VersionTLS12",
				ciphers:    "  TLS_AES_128_GCM_SHA256,  TLS_AES_256_GCM_SHA384   ",
				curves:     "X25519,CurveP256",
			},
			&tls.Config{
				MinVersion:       tls.VersionTLS12,
				CipherSuites:     []uint16{tls.TLS_AES_128_GCM_SHA256, tls.TLS_AES_256_GCM_SHA384},
				CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256},
			},
		),
	)
})

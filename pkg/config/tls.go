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

package config

import (
	"crypto/tls"
	"fmt"
	"strings"
)

// TODO: Unfortunately Go's 'tls' package doesn't provide APIs for translating human readable
// TLS version and curve names to corresponding uint16 values that can be used in tls.Config.
// There is no equivalent of tls.CipherSuites() for TLS versions and curves.
// Until such APIs are available, the following are required and should be maintained.
// Tracking issue: https://github.com/golang/go/issues/77712
var tlsVersionByName = map[string]uint16{
	"VersionTLS13": tls.VersionTLS13,
	"VersionTLS12": tls.VersionTLS12,
	"VersionTLS11": tls.VersionTLS11,
	"VersionTLS10": tls.VersionTLS10,
}
var tlsCurveIDByName = map[string]tls.CurveID{
	"X25519":    tls.X25519,
	"CurveP256": tls.CurveP256,
	"CurveP384": tls.CurveP384,
	"CurveP521": tls.CurveP521,
}

var tlsCipherSuiteIDByName = map[string]uint16{}

var indexedInsecureCipherSuiteNames = map[string]struct{}{}

func init() {
	for _, cipherSuite := range tls.CipherSuites() {
		tlsCipherSuiteIDByName[cipherSuite.Name] = cipherSuite.ID
	}

	for _, insecureCipherSuite := range tls.InsecureCipherSuites() {
		indexedInsecureCipherSuiteNames[insecureCipherSuite.Name] = struct{}{}
	}
}

func ParseTLSOptions(
	tlsMinVersionRaw string,
	tlsCipherSuitesRaw string,
	tlsCurvePreferencesRaw string,
) (
	func(*tls.Config),
	error,
) {
	tlsMinVersion, err := toTLSVersion(tlsMinVersionRaw)
	if err != nil {
		return nil, err
	}

	cipherSuiteNames := parseStringSlice(tlsCipherSuitesRaw)
	if err := validateSafeCipherSuite(cipherSuiteNames); err != nil {
		return nil, err
	}
	if err := validateTLSVersionConfigurableCiphers(tlsMinVersion, cipherSuiteNames); err != nil {
		return nil, err
	}
	cipherSuiteIDs, err := toCipherSuiteIDs(cipherSuiteNames)
	if err != nil {
		return nil, err
	}

	curvePreferenceNames := parseStringSlice(tlsCurvePreferencesRaw)
	curvePreferenceIDs, err := toCurveIDs(curvePreferenceNames)
	if err != nil {
		return nil, err
	}

	tlsOpts := func(c *tls.Config) {
		if tlsMinVersion > 0 {
			c.MinVersion = tlsMinVersion
		}
		if len(cipherSuiteIDs) > 0 {
			c.CipherSuites = cipherSuiteIDs
		}
		if len(curvePreferenceIDs) > 0 {
			c.CurvePreferences = curvePreferenceIDs
		}
	}
	return tlsOpts, nil
}

func toTLSVersion(tlsVersionName string) (uint16, error) {
	if tlsVersionName == "" {
		return 0, nil
	}
	tlsVersion, exist := tlsVersionByName[tlsVersionName]
	if !exist {
		return 0, fmt.Errorf("TLS version not found for %q", tlsVersionName)
	}
	return tlsVersion, nil
}

func validateSafeCipherSuite(cipherSuiteNames []string) error {
	for _, cipherSuiteName := range cipherSuiteNames {
		if _, exist := indexedInsecureCipherSuiteNames[cipherSuiteName]; exist {
			return fmt.Errorf("using insecure cipher suite %q is not allowed", cipherSuiteName)
		}
	}
	return nil
}

func validateTLSVersionConfigurableCiphers(versionID uint16, cipherSuiteNames []string) error {
	if versionID == tls.VersionTLS13 && len(cipherSuiteNames) > 0 {
		return fmt.Errorf("configuring cipher suites for TLS 1.3 is not allowed")
	}
	return nil
}

func toCipherSuiteIDs(cipherSuiteNames []string) ([]uint16, error) {
	ids, err := getValuesByKeys(tlsCipherSuiteIDByName, cipherSuiteNames)
	if err != nil {
		return nil, fmt.Errorf("unable to find cipher suite IDs: %w", err)
	}
	return ids, nil
}

func toCurveIDs(curveNames []string) ([]tls.CurveID, error) {
	ids, err := getValuesByKeys(tlsCurveIDByName, curveNames)
	if err != nil {
		return nil, fmt.Errorf("unable to find curve preference IDs: %w", err)
	}
	return ids, nil
}

// getValuesByKeys returns the values for the given keys from the map.
// Returns an error if a given key is not found in the map.
func getValuesByKeys[K, V comparable](m map[K]V, keys []K) ([]V, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	values := []V{}
	for _, k := range keys {
		v, exist := m[k]
		if !exist {
			return nil, fmt.Errorf("key not found %v", k)
		}
		values = append(values, v)
	}
	return values, nil
}

// parseStringSlice parses a comma-separated list of strings (e.g.: "a,b,c") to string slice.
func parseStringSlice(raw string) []string {
	raw = strings.TrimSpace(raw)
	var elements []string
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			elements = append(elements, name)
		}
	}
	return elements
}

/*
Copyright 2026 The Kubernetes Authors.

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

package visibility

import (
	"crypto/tls"
	"reflect"
	"testing"

	kueuetls "sigs.k8s.io/kueue/pkg/tls"
)

func TestSecureServingTLS(t *testing.T) {
	cases := map[string]struct {
		policy      *kueuetls.Policy
		wantVersion string
		wantCiphers []string
	}{
		"honored policy is applied": {
			policy: &kueuetls.Policy{
				Honored:        true,
				MinVersionName: "VersionTLS12",
				CipherNames:    []string{"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"},
			},
			wantVersion: "VersionTLS12",
			wantCiphers: []string{"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"},
		},
		"honored TLS 1.3 policy carries no ciphers": {
			policy:      &kueuetls.Policy{Honored: true, MinVersionName: "VersionTLS13"},
			wantVersion: "VersionTLS13",
		},
		"policy not honored keeps defaults": {
			policy: &kueuetls.Policy{MinVersionName: "VersionTLS13", CipherNames: []string{"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"}},
		},
		"nil policy keeps defaults": {},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotVersion, gotCiphers := secureServingTLS(tc.policy)
			if gotVersion != tc.wantVersion {
				t.Errorf("version = %q, want %q", gotVersion, tc.wantVersion)
			}
			if !reflect.DeepEqual(gotCiphers, tc.wantCiphers) {
				t.Errorf("ciphers = %v, want %v", gotCiphers, tc.wantCiphers)
			}
		})
	}
}

func TestSecureServingHTTP2Disabled(t *testing.T) {
	cases := map[string]struct {
		policy *kueuetls.Policy
		want   bool
	}{
		"nil policy keeps HTTP/2":                  {},
		"policy not honored keeps HTTP/2":          {policy: &kueuetls.Policy{Honored: false, MinVersion: tls.VersionTLS12, CipherIDs: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384}}},
		"TLS 1.3 policy keeps HTTP/2":              {policy: &kueuetls.Policy{Honored: true, MinVersion: tls.VersionTLS13}},
		"TLS 1.2 with AES-128-GCM keeps HTTP/2":    {policy: &kueuetls.Policy{Honored: true, MinVersion: tls.VersionTLS12, CipherIDs: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}}},
		"AES-256-only TLS 1.2 disables HTTP/2":     {want: true, policy: &kueuetls.Policy{Honored: true, MinVersion: tls.VersionTLS12, CipherIDs: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384}}},
		"CBC-only TLS 1.0 minimum disables HTTP/2": {want: true, policy: &kueuetls.Policy{Honored: true, MinVersion: tls.VersionTLS10, CipherIDs: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := secureServingHTTP2Disabled(tc.policy); got != tc.want {
				t.Errorf("secureServingHTTP2Disabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCipherSuitesAllECDSA(t *testing.T) {
	cases := []struct {
		names []string
		want  bool
	}{
		{nil, false},
		{[]string{"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256"}, true},
		{[]string{"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256", "TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384"}, true},
		{[]string{"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256", "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"}, false},
		{[]string{"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"}, false},
	}
	for _, tc := range cases {
		if got := cipherSuitesAllECDSA(tc.names); got != tc.want {
			t.Errorf("cipherSuitesAllECDSA(%v) = %v, want %v", tc.names, got, tc.want)
		}
	}
}

func TestValidateVisibilityTLSCipherSuites(t *testing.T) {
	cases := map[string]struct {
		policy  *kueuetls.Policy
		names   []string
		wantErr bool
	}{
		"honored TLS 1.2 ECDSA-only list is rejected": {
			policy:  &kueuetls.Policy{Honored: true, MinVersion: tls.VersionTLS12},
			names:   []string{"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256"},
			wantErr: true,
		},
		"honored TLS 1.2 mixed list can use RSA certificate": {
			policy: &kueuetls.Policy{Honored: true, MinVersion: tls.VersionTLS12},
			names: []string{
				"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256",
				"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
			},
		},
		"TLS 1.3 cipher negotiation is independent of certificate type": {
			policy: &kueuetls.Policy{Honored: true, MinVersion: tls.VersionTLS13},
			names:  []string{"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256"},
		},
		"unhonored policy keeps defaults": {
			policy: &kueuetls.Policy{Honored: false, MinVersion: tls.VersionTLS12},
			names:  []string{"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256"},
		},
		"nil policy keeps defaults": {
			names: []string{"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := validateVisibilityTLSCipherSuites(tc.policy, tc.names)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateVisibilityTLSCipherSuites() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

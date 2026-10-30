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

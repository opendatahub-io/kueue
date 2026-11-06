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

package tls

import (
	"crypto/tls"
	"errors"
	"slices"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func apiServerObject(spec map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "config.openshift.io/v1",
		"kind":       "APIServer",
		"metadata":   map[string]interface{}{"name": apiServerName},
		"spec":       spec,
	}}
}

func fakeClient(t *testing.T, obj *unstructured.Unstructured, failGet error) *dynamicfake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{apiServerGVR: "APIServerList"})
	if obj != nil {
		if err := client.Tracker().Add(obj); err != nil {
			t.Fatalf("adding APIServer object: %v", err)
		}
	}
	if failGet != nil {
		client.PrependReactor("get", "apiservers", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, failGet
		})
	}
	return client
}

func strictSpec(profile map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"tlsSecurityProfile": profile,
		"tlsAdherence":       "StrictAllComponents",
	}
}

func customProfile(minVersion string, ciphers []string) map[string]interface{} {
	return customProfileWithGroups(minVersion, ciphers, nil)
}

func customProfileWithGroups(minVersion string, ciphers, groups []string) map[string]interface{} {
	custom := map[string]interface{}{"minTLSVersion": minVersion}
	if len(ciphers) > 0 {
		raw := make([]interface{}, 0, len(ciphers))
		for _, c := range ciphers {
			raw = append(raw, c)
		}
		custom["ciphers"] = raw
	}
	if len(groups) > 0 {
		raw := make([]interface{}, 0, len(groups))
		for _, group := range groups {
			raw = append(raw, group)
		}
		custom["groups"] = raw
	}
	return map[string]interface{}{"type": "Custom", "custom": custom}
}

func applyOpts(policy *Policy) *tls.Config {
	cfg := &tls.Config{}
	for _, opt := range policy.TLSOpts {
		opt(cfg)
	}
	return cfg
}

func TestResolveStrictCustomProfileApplied(t *testing.T) {
	obj := apiServerObject(strictSpec(customProfile(versionTLS12, []string{
		"ECDHE-RSA-AES128-GCM-SHA256", "ECDHE-RSA-AES256-GCM-SHA384",
	})))
	policy, err := resolveFromClient(t.Context(), fakeClient(t, obj, nil))
	if err != nil {
		t.Fatalf("resolveFromClient() error = %v", err)
	}
	if !policy.Honored {
		t.Fatal("expected policy to be honored under Strict")
	}
	if policy.MinVersion != tls.VersionTLS12 || policy.MinVersionName != versionTLS12 {
		t.Errorf("min version = %x (%q), want TLS 1.2", policy.MinVersion, policy.MinVersionName)
	}
	if len(policy.CipherIDs) != 2 {
		t.Fatalf("CipherIDs = %v, want 2 suites", policy.CipherIDs)
	}
	if policy.CipherNames[0] != "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256" {
		t.Errorf("CipherNames[0] = %q, want the IANA name", policy.CipherNames[0])
	}
	applied := applyOpts(policy)
	if applied.MinVersion != tls.VersionTLS12 || len(applied.CipherSuites) != 2 {
		t.Errorf("applied config = min %x ciphers %v", applied.MinVersion, applied.CipherSuites)
	}
	if len(applied.NextProtos) == 0 {
		t.Error("expected ALPN protocols to be set")
	}
}

func TestResolveCustomProfileGroupsApplied(t *testing.T) {
	groups := []string{"secp256r1", "X25519MLKEM768"}
	obj := apiServerObject(strictSpec(customProfileWithGroups(versionTLS12, nil, groups)))
	policy, err := resolveFromClient(t.Context(), fakeClient(t, obj, nil))
	if err != nil {
		t.Fatalf("resolveFromClient() error = %v", err)
	}
	want := []tls.CurveID{tls.CurveP256, tls.X25519MLKEM768}
	if !slices.Equal(policy.CurveIDs, want) {
		t.Errorf("CurveIDs = %v, want %v", policy.CurveIDs, want)
	}
	if !slices.Equal(policy.EffectiveProfile.Groups, groups) {
		t.Errorf("EffectiveProfile.Groups = %v, want %v", policy.EffectiveProfile.Groups, groups)
	}
	applied := applyOpts(policy)
	if !slices.Equal(applied.CurvePreferences, want) {
		t.Errorf("CurvePreferences = %v, want %v", applied.CurvePreferences, want)
	}
}

func TestResolveCustomProfileRejectsAllUnusableGroups(t *testing.T) {
	obj := apiServerObject(strictSpec(customProfileWithGroups(versionTLS12, nil,
		[]string{"unknown-group", "unsupported-group"})))
	if _, err := resolveFromClient(t.Context(), fakeClient(t, obj, nil)); err == nil {
		t.Fatal("expected all-unusable group list to fail before startup")
	}
}

func TestResolveCustomProfileEmptyGroupsLeaveCurvePreferencesUnset(t *testing.T) {
	obj := apiServerObject(strictSpec(customProfileWithGroups(versionTLS12, nil, nil)))
	policy, err := resolveFromClient(t.Context(), fakeClient(t, obj, nil))
	if err != nil {
		t.Fatalf("resolveFromClient() error = %v", err)
	}
	if policy.CurveIDs != nil {
		t.Errorf("CurveIDs = %v, want nil", policy.CurveIDs)
	}
	if applied := applyOpts(policy); applied.CurvePreferences != nil {
		t.Errorf("CurvePreferences = %v, want nil", applied.CurvePreferences)
	}
}

func TestResolveCustomProfileKeepsUsableGroupsFromMixedList(t *testing.T) {
	obj := apiServerObject(strictSpec(customProfileWithGroups(versionTLS12, nil,
		[]string{"unknown-group", "secp384r1", "unsupported-group"})))
	policy, err := resolveFromClient(t.Context(), fakeClient(t, obj, nil))
	if err != nil {
		t.Fatalf("resolveFromClient() error = %v", err)
	}
	want := []tls.CurveID{tls.CurveP384}
	if !slices.Equal(policy.CurveIDs, want) {
		t.Errorf("CurveIDs = %v, want usable subset %v", policy.CurveIDs, want)
	}
	if applied := applyOpts(policy); !slices.Equal(applied.CurvePreferences, want) {
		t.Errorf("CurvePreferences = %v, want usable subset %v", applied.CurvePreferences, want)
	}
}

func TestResolveModernMinimum(t *testing.T) {
	obj := apiServerObject(strictSpec(map[string]interface{}{"type": "Modern"}))
	policy, err := resolveFromClient(t.Context(), fakeClient(t, obj, nil))
	if err != nil {
		t.Fatalf("resolveFromClient() error = %v", err)
	}
	if policy.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %x, want TLS 1.3", policy.MinVersion)
	}
	if len(policy.CipherIDs) != 0 {
		t.Errorf("CipherIDs = %v, want none for TLS 1.3", policy.CipherIDs)
	}
}

func TestResolveOldProfileHonorsLegacySuites(t *testing.T) {
	obj := apiServerObject(strictSpec(map[string]interface{}{"type": "Old"}))
	policy, err := resolveFromClient(t.Context(), fakeClient(t, obj, nil))
	if err != nil {
		t.Fatalf("resolveFromClient() error = %v", err)
	}
	if policy.MinVersion != tls.VersionTLS10 {
		t.Errorf("MinVersion = %x, want TLS 1.0 for the Old profile", policy.MinVersion)
	}
	if len(policy.CipherIDs) == 0 {
		t.Error("expected Old profile cipher suites")
	}
}

func TestResolveNilProfileUnderStrictResolvesIntermediate(t *testing.T) {
	obj := apiServerObject(strictSpec(nil))
	policy, err := resolveFromClient(t.Context(), fakeClient(t, obj, nil))
	if err != nil {
		t.Fatalf("resolveFromClient() error = %v", err)
	}
	if !policy.Honored {
		t.Fatal("expected policy honored under Strict")
	}
	if policy.MinVersion != tls.VersionTLS12 || len(policy.CipherIDs) == 0 {
		t.Errorf("profile = min %x ciphers %v, want Intermediate defaults", policy.MinVersion, policy.CipherIDs)
	}
}

func TestResolveNoOpinionPreservesExistingSettings(t *testing.T) {
	// NoOpinion is the empty string; LegacyAdheringComponentsOnly is the
	// only explicit value that preserves existing listener settings.
	for _, adherence := range []string{"", "LegacyAdheringComponentsOnly"} {
		t.Run("adherence="+adherence, func(t *testing.T) {
			obj := apiServerObject(map[string]interface{}{
				"tlsSecurityProfile": customProfile(versionTLS13, nil),
				"tlsAdherence":       adherence,
			})
			policy, err := resolveFromClient(t.Context(), fakeClient(t, obj, nil))
			if err != nil {
				t.Fatalf("resolveFromClient() error = %v", err)
			}
			if policy.Honored {
				t.Fatal("expected policy not honored")
			}
			if len(policy.TLSOpts) != 0 {
				t.Error("expected no TLSOpts so existing listener settings are preserved")
			}
		})
	}
}

func TestResolveUnknownAdherenceHonorsProfile(t *testing.T) {
	obj := apiServerObject(map[string]interface{}{
		"tlsSecurityProfile": customProfile(versionTLS13, nil),
		"tlsAdherence":       "FutureAdherence",
	})
	policy, err := resolveFromClient(t.Context(), fakeClient(t, obj, nil))
	if err != nil {
		t.Fatalf("resolveFromClient() error = %v", err)
	}
	if !policy.Honored {
		t.Fatal("expected unknown adherence to honor the profile")
	}
	if policy.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %x, want TLS 1.3", policy.MinVersion)
	}
}

func TestResolveForbiddenFailsClosed(t *testing.T) {
	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Group: "config.openshift.io", Resource: "apiservers"},
		apiServerName, errors.New("denied"),
	)
	if _, err := resolveFromClient(t.Context(), fakeClient(t, nil, forbidden)); err == nil {
		t.Fatal("expected Forbidden read to fail closed")
	}
}

func TestResolveTransientAppliesIntermediateAndStaysWatchable(t *testing.T) {
	unavailable := apierrors.NewServiceUnavailable("apiserver unavailable")
	policy, err := resolveFromClient(t.Context(), fakeClient(t, nil, unavailable))
	if err != nil {
		t.Fatalf("resolveFromClient() error = %v", err)
	}
	if !policy.UsedTransientFallback || !policy.APIAvailable {
		t.Fatalf("policy = %+v, want transient fallback and a watchable API", policy)
	}
	if policy.MinVersion != tls.VersionTLS12 || len(policy.CipherIDs) == 0 {
		t.Errorf("transient fallback = min %x ciphers %v, want Intermediate defaults", policy.MinVersion, policy.CipherIDs)
	}
}

func TestResolveAPINotAvailablePreservesExistingSettings(t *testing.T) {
	noMatch := &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "config.openshift.io", Kind: "APIServer"}}
	policy, err := resolveFromClient(t.Context(), fakeClient(t, nil, noMatch))
	if err != nil {
		t.Fatalf("resolveFromClient() error = %v", err)
	}
	if policy.APIAvailable || policy.Honored || len(policy.TLSOpts) != 0 {
		t.Errorf("policy = %+v, want preserved existing settings outside OpenShift", policy)
	}
}

func TestResolveAPIServerNotFoundPreservesExistingSettings(t *testing.T) {
	notFound := apierrors.NewNotFound(
		schema.GroupResource{Group: "config.openshift.io", Resource: "apiservers"}, apiServerName,
	)
	policy, err := resolveFromClient(t.Context(), fakeClient(t, nil, notFound))
	if err != nil {
		t.Fatalf("resolveFromClient() error = %v", err)
	}
	if policy.APIAvailable || policy.Honored || len(policy.TLSOpts) != 0 {
		t.Errorf("policy = %+v, want preserved existing settings", policy)
	}
}

func TestResolveAllUnusableCiphersFail(t *testing.T) {
	obj := apiServerObject(strictSpec(customProfile(versionTLS12, []string{"DHE-RSA-AES128-SHA256"})))
	if _, err := resolveFromClient(t.Context(), fakeClient(t, obj, nil)); err == nil {
		t.Fatal("expected all-unusable cipher list to fail before startup")
	}
}

func TestResolveTLS13OnlySuitesAreUnusableBelowTLS13(t *testing.T) {
	// A sub-1.3 minimum cannot negotiate TLS 1.3 suites. A profile naming only
	// those must fail instead of leaving the listener without a usable cipher.
	obj := apiServerObject(strictSpec(customProfile(versionTLS12, []string{"TLS_AES_128_GCM_SHA256"})))
	if _, err := resolveFromClient(t.Context(), fakeClient(t, obj, nil)); err == nil {
		t.Fatal("expected a TLS 1.2 profile listing only TLS 1.3 suites to fail before startup")
	}
}

func TestResolveTLS13OnlySuitesAreDroppedBelowTLS13(t *testing.T) {
	obj := apiServerObject(strictSpec(customProfile(versionTLS12, []string{
		"TLS_AES_128_GCM_SHA256",
		"ECDHE-RSA-AES128-GCM-SHA256",
	})))
	policy, err := resolveFromClient(t.Context(), fakeClient(t, obj, nil))
	if err != nil {
		t.Fatalf("resolveFromClient() error = %v", err)
	}
	if len(policy.CipherIDs) != 1 || policy.CipherIDs[0] != tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 {
		t.Errorf("CipherIDs = %v, want only the TLS 1.2 suite", policy.CipherIDs)
	}
}

func TestResolveTLS13SuitesAreLeftToTheRuntime(t *testing.T) {
	obj := apiServerObject(strictSpec(customProfile(versionTLS13, []string{
		"TLS_AES_128_GCM_SHA256",
		"ECDHE-RSA-AES128-GCM-SHA256",
	})))
	policy, err := resolveFromClient(t.Context(), fakeClient(t, obj, nil))
	if err != nil {
		t.Fatalf("resolveFromClient() error = %v", err)
	}
	if policy.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %x, want TLS 1.3", policy.MinVersion)
	}
	if len(policy.CipherIDs) != 0 || len(policy.CipherNames) != 0 {
		t.Errorf("ciphers = %v, want none for a TLS 1.3 minimum", policy.CipherIDs)
	}
}

func TestResolveInvalidMinimumVersionFails(t *testing.T) {
	obj := apiServerObject(strictSpec(customProfile("VersionTLS99", nil)))
	if _, err := resolveFromClient(t.Context(), fakeClient(t, obj, nil)); err == nil {
		t.Fatal("expected invalid minimum version to fail before startup")
	}
}

func TestResolveCustomProfileWithoutSettingsFallsBackToIntermediateMinimum(t *testing.T) {
	obj := apiServerObject(strictSpec(map[string]interface{}{
		"type":   "Custom",
		"custom": map[string]interface{}{"ciphers": []interface{}{"ECDHE-RSA-AES128-GCM-SHA256"}},
	}))
	policy, err := resolveFromClient(t.Context(), fakeClient(t, obj, nil))
	if err != nil {
		t.Fatalf("resolveFromClient() error = %v", err)
	}
	if policy.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want TLS 1.2 when minTLSVersion is omitted", policy.MinVersion)
	}
}

func TestProfileSpecEqual(t *testing.T) {
	base := profileSpec{
		Type: profileCustom, MinTLSVersion: versionTLS12,
		Ciphers: []string{"a", "b"}, Groups: []string{"secp256r1"},
	}
	if !profileSpecEqual(base, base) {
		t.Error("expected identical specs to compare equal")
	}
	if profileSpecEqual(base, profileSpec{Type: profileCustom, MinTLSVersion: versionTLS13, Ciphers: []string{"a", "b"}}) {
		t.Error("expected different minimum versions to compare unequal")
	}
	if profileSpecEqual(base, profileSpec{Type: profileCustom, MinTLSVersion: versionTLS12, Ciphers: []string{"a"}}) {
		t.Error("expected different cipher lists to compare unequal")
	}
	if profileSpecEqual(base, profileSpec{
		Type: profileCustom, MinTLSVersion: versionTLS12,
		Ciphers: []string{"a", "b"}, Groups: []string{"secp384r1"},
	}) {
		t.Error("expected different group lists to compare unequal")
	}
	if profileSpecEqual(base, profileSpec{Type: profileModern}) {
		t.Error("expected different profile types to compare unequal")
	}
}

func TestOpenSSLToIANAEveryAliasResolves(t *testing.T) {
	for alias, iana := range openSSLToIANA {
		if _, ok := ianaCipherSuites[iana]; !ok {
			t.Errorf("OpenSSL alias %q maps to %q, which is not a cipher suite in this Go runtime", alias, iana)
		}
	}
}

func TestOldCiphersSupersetOfIntermediate(t *testing.T) {
	for _, id := range intermediateCiphers {
		if !slices.Contains(oldCiphers, id) {
			t.Errorf("oldCiphers is missing intermediate suite %s", tls.CipherSuiteName(id))
		}
	}
}

func TestCipherIDResolvesIANAAndOpenSSL(t *testing.T) {
	tests := []struct {
		name string
		want uint16
	}{
		{name: "ECDHE-RSA-AES128-GCM-SHA256", want: tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
		{name: "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", want: tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
		{name: "ECDHE-ECDSA-AES256-GCM-SHA384", want: tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := cipherID(tt.name, tls.VersionTLS12)
			if !ok || got != tt.want {
				t.Errorf("cipherID(%q) = %x, %v; want %x, true", tt.name, got, ok, tt.want)
			}
		})
	}
}

func TestCheckTLSProfileChange(t *testing.T) {
	base := apiServerObject(strictSpec(map[string]interface{}{"type": "Intermediate"}))
	policy, err := resolveFromClient(t.Context(), fakeClient(t, base, nil))
	if err != nil {
		t.Fatalf("resolveFromClient() error = %v", err)
	}

	tests := []struct {
		name string
		spec map[string]interface{}
		want tlsProfileChangeKind
	}{
		{
			name: "unchanged",
			spec: strictSpec(map[string]interface{}{"type": "Intermediate"}),
			want: tlsProfileUnchanged,
		},
		{
			name: "profile changed",
			spec: strictSpec(map[string]interface{}{"type": "Old"}),
			want: tlsProfileSpecChanged,
		},
		{
			name: "adherence label changed",
			spec: map[string]interface{}{
				"tlsSecurityProfile": map[string]interface{}{"type": "Intermediate"},
				"tlsAdherence":       "FuturePolicy",
			},
			want: tlsProfileAdherenceChanged,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tlsSpec, err := extractAPIServerTLS(apiServerObject(tt.spec))
			if err != nil {
				t.Fatalf("extractAPIServerTLS() error = %v", err)
			}
			if got := checkTLSProfileChange(policy, tlsSpec); got != tt.want {
				t.Errorf("checkTLSProfileChange() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckTLSProfileChangeDetectsTransientFallbackRecovery(t *testing.T) {
	policy, err := resolveFromClient(t.Context(), fakeClient(t, nil,
		apierrors.NewServiceUnavailable("apiserver unavailable")))
	if err != nil {
		t.Fatalf("resolveFromClient() error = %v", err)
	}
	if !policy.UsedTransientFallback || !policy.Honored {
		t.Fatalf("policy = %+v, want a transient fallback with honored Intermediate defaults", policy)
	}

	tlsSpec, err := extractAPIServerTLS(apiServerObject(map[string]interface{}{}))
	if err != nil {
		t.Fatalf("extractAPIServerTLS() error = %v", err)
	}
	if got := checkTLSProfileChange(policy, tlsSpec); got != tlsProfileHonoredChanged {
		t.Errorf("checkTLSProfileChange() = %v, want honored change", got)
	}
}

func TestIanaCipherSuitesExcludesRC4(t *testing.T) {
	for _, name := range []string{
		"TLS_RSA_WITH_RC4_128_SHA",
		"TLS_ECDHE_ECDSA_WITH_RC4_128_SHA",
		"TLS_ECDHE_RSA_WITH_RC4_128_SHA",
	} {
		if _, ok := ianaCipherSuites[name]; ok {
			t.Errorf("ianaCipherSuites contains %s", name)
		}
		if _, ok := cipherID(name, tls.VersionTLS12); ok {
			t.Errorf("cipherID(%q) resolved an RC4 suite", name)
		}
	}
}

func TestCanServeHTTP2(t *testing.T) {
	cases := map[string]struct {
		policy *Policy
		want   bool
	}{
		"nil policy uses defaults":         {nil, true},
		"policy not honored uses defaults": {&Policy{Honored: false, MinVersion: tls.VersionTLS12, CipherIDs: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384}}, true},
		"TLS 1.3 always serves HTTP/2":     {&Policy{Honored: true, MinVersion: tls.VersionTLS13, CipherIDs: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384}}, true},
		"TLS 1.2 with empty cipher list":   {&Policy{Honored: true, MinVersion: tls.VersionTLS12}, true},
		"TLS 1.2 with AES-128-GCM suite":   {&Policy{Honored: true, MinVersion: tls.VersionTLS12, CipherIDs: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384, tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}}, true},
		"TLS 1.2 AES-256-GCM only":         {&Policy{Honored: true, MinVersion: tls.VersionTLS12, CipherIDs: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384}}, false},
		"TLS 1.2 CBC only":                 {&Policy{Honored: true, MinVersion: tls.VersionTLS10, CipherIDs: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA}}, false},
		"TLS 1.2 ChaCha20 only":            {&Policy{Honored: true, MinVersion: tls.VersionTLS12, CipherIDs: []uint16{tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256}}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.policy.CanServeHTTP2(); got != tc.want {
				t.Errorf("CanServeHTTP2() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSecPMLKEMGroupsValidateOnGo126(t *testing.T) {
	// The SecP*MLKEM* groups are implemented by crypto/tls from Go 1.26;
	// the module requires that toolchain, so a profile listing only them
	// must resolve instead of leaving the listener with no usable group.
	curves := curveIDs([]string{"SecP256r1MLKEM768", "SecP384r1MLKEM1024"})
	want := []tls.CurveID{tls.SecP256r1MLKEM768, tls.SecP384r1MLKEM1024}
	if !slices.Equal(curves, want) {
		t.Fatalf("curveIDs() = %v, want %v", curves, want)
	}
}

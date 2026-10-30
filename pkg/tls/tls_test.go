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
	custom := map[string]interface{}{"minTLSVersion": minVersion}
	if len(ciphers) > 0 {
		raw := make([]interface{}, 0, len(ciphers))
		for _, c := range ciphers {
			raw = append(raw, c)
		}
		custom["ciphers"] = raw
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
	base := profileSpec{Type: profileCustom, MinTLSVersion: versionTLS12, Ciphers: []string{"a", "b"}}
	if !profileSpecEqual(base, base) {
		t.Error("expected identical specs to compare equal")
	}
	if profileSpecEqual(base, profileSpec{Type: profileCustom, MinTLSVersion: versionTLS13, Ciphers: []string{"a", "b"}}) {
		t.Error("expected different minimum versions to compare unequal")
	}
	if profileSpecEqual(base, profileSpec{Type: profileCustom, MinTLSVersion: versionTLS12, Ciphers: []string{"a"}}) {
		t.Error("expected different cipher lists to compare unequal")
	}
	if profileSpecEqual(base, profileSpec{Type: profileModern}) {
		t.Error("expected different profile types to compare unequal")
	}
}

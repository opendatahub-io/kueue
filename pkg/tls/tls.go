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
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	apiServerName  = "cluster"
	resolveTimeout = 10 * time.Second
)

// TLS adherence policies as stored on APIServer.spec.tlsAdherence.
// NoOpinion is the empty string and behaves like LegacyAdheringComponentsOnly.
const adherenceLegacyAdheringComponentsOnly = "LegacyAdheringComponentsOnly"

var apiServerGVR = schema.GroupVersionResource{
	Group:    "config.openshift.io",
	Version:  "v1",
	Resource: "apiservers",
}

// Policy is the cluster TLS security profile resolved before the manager
// starts. It drives the metrics listener, the webhook server and the
// visibility API server.
type Policy struct {
	// Honored reports whether the cluster profile is applied. It is true
	// under Strict and unknown adherence policies. Under NoOpinion (unset)
	// and LegacyAdheringComponentsOnly adherence, and outside OpenShift, Kueue preserves its
	// historical listener settings and Honored is false with empty TLSOpts.
	Honored bool
	// Adherence is the cluster TLS adherence policy read at startup.
	Adherence string
	// APIAvailable reports whether the APIServer object can be watched on
	// this cluster. It is false outside OpenShift.
	APIAvailable bool
	// UsedTransientFallback reports that the profile could not be read due
	// to a transient error and hardened Intermediate defaults are applied
	// until the watcher observes the real profile.
	UsedTransientFallback bool
	// ClusterProfile is the profile as stored on the cluster, resolved from
	// its named or custom type. It seeds the profile watcher's initial state.
	ClusterProfile profileSpec
	// EffectiveProfile is the profile applied to the listeners: the cluster
	// profile when honored, Intermediate defaults on fallback.
	EffectiveProfile profileSpec
	// MinVersion and CipherIDs are the parsed effective values.
	MinVersion uint16
	CipherIDs  []uint16
	// MinVersionName and CipherNames hold the OpenShift and IANA names of the
	// effective values, for servers configured with names instead of
	// tls.Config options.
	MinVersionName string
	CipherNames    []string
	// TLSOpts applies the effective policy to controller-runtime servers
	// (metrics, webhook). Empty when the policy is not honored.
	TLSOpts []func(*tls.Config)
}

// profileSpec mirrors the fields of an OpenShift TLS profile spec that
// determine the listener configuration.
type profileSpec struct {
	Type          string
	MinTLSVersion string
	Ciphers       []string
}

// tlsCustomProfile is the custom TLS profile of an APIServer.
type tlsCustomProfile struct {
	MinTLSVersion string   `json:"minTLSVersion"`
	Ciphers       []string `json:"ciphers"`
}

// tlsSecurityProfile is the APIServer's tlsSecurityProfile field.
type tlsSecurityProfile struct {
	Type   string            `json:"type"`
	Custom *tlsCustomProfile `json:"custom"`
}

// apiServerTLS is the subset of APIServer.spec read by the resolver.
type apiServerTLS struct {
	TLSSecurityProfile *tlsSecurityProfile `json:"tlsSecurityProfile"`
	TLSAdherence       string              `json:"tlsAdherence"`
}

// Resolve reads the OpenShift cluster-wide TLS security profile and adherence
// policy and returns the policy to apply to Kueue's secure listeners.
//
// Error handling:
//   - Forbidden/Unauthorized reads fail: starting with an unverified TLS
//     configuration would silently weaken the policy (fail closed).
//   - Invalid or unsupported profiles fail: an explicit misconfiguration must
//     not be silently replaced.
//   - The APIServer API being absent (non-OpenShift cluster) or the object
//     missing preserves existing behavior.
//   - Transient read failures apply hardened Intermediate defaults and mark
//     the policy watchable so the process self-heals on the next event.
func Resolve(ctx context.Context, cfg *rest.Config) (*Policy, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client for TLS profile: %w", err)
	}
	return resolveFromClient(ctx, dyn)
}

// resolveFromClient resolves the policy using the provided dynamic client. It
// is separated from Resolve so tests can inject a fake client.
func resolveFromClient(ctx context.Context, dyn dynamic.Interface) (*Policy, error) {
	logger := log.FromContext(ctx).WithName("tls")
	resolveCtx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	policy := &Policy{}

	obj, err := dyn.Resource(apiServerGVR).Get(resolveCtx, apiServerName, metav1.GetOptions{})
	if err != nil {
		switch {
		case meta.IsNoMatchError(err):
			logger.Info("TLS profile API not available, preserving existing listener settings")
			return policy, nil
		case apierrors.IsNotFound(err):
			logger.Info("APIServer object not found, preserving existing listener settings")
			return policy, nil
		case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
			return nil, fmt.Errorf("not authorized to read the APIServer TLS profile (grant get/list/watch on apiservers.config.openshift.io and restart): %w", err)
		case isTransient(err):
			logger.Info("Transient error reading TLS profile, applying Intermediate defaults until the watcher recovers", "error", err)
			policy.APIAvailable = true
			policy.UsedTransientFallback = true
			policy.ClusterProfile = profileSpec{Type: profileIntermediate}
			return policy.withEffectiveProfile(policy.ClusterProfile)
		default:
			return nil, fmt.Errorf("reading APIServer TLS profile: %w", err)
		}
	}

	tlsSpec, err := extractAPIServerTLS(obj)
	if err != nil {
		return nil, fmt.Errorf("parsing APIServer TLS profile: %w", err)
	}

	policy.APIAvailable = true
	policy.Adherence = tlsSpec.TLSAdherence
	policy.ClusterProfile = clusterProfile(tlsSpec.TLSSecurityProfile)

	if !shouldHonorClusterTLSProfile(policy.Adherence) {
		logger.Info("TLS adherence policy does not include Kueue, preserving existing listener settings", "adherence", policy.Adherence)
		return policy, nil
	}

	return policy.withEffectiveProfile(policy.ClusterProfile)
}

func extractAPIServerTLS(obj *unstructured.Unstructured) (*apiServerTLS, error) {
	spec, found, err := unstructured.NestedMap(obj.Object, "spec")
	if err != nil {
		return nil, fmt.Errorf("reading APIServer spec: %w", err)
	}
	if !found || spec == nil {
		return &apiServerTLS{}, nil
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("encoding APIServer spec: %w", err)
	}
	out := &apiServerTLS{}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, fmt.Errorf("decoding APIServer spec: %w", err)
	}
	return out, nil
}

// clusterProfile resolves the APIServer's TLS security profile to a concrete
// profile. A nil or unknown profile resolves to Intermediate defaults, as the
// OpenShift API server does.
func clusterProfile(profile *tlsSecurityProfile) profileSpec {
	if profile == nil {
		return profileSpec{Type: profileIntermediate}
	}
	switch profile.Type {
	case profileIntermediate, "":
		return profileSpec{Type: profileIntermediate}
	case profileModern, profileOld:
		return profileSpec{Type: profile.Type}
	case profileCustom:
		if profile.Custom == nil {
			return profileSpec{Type: profileIntermediate}
		}
		return profileSpec{
			Type:          profileCustom,
			MinTLSVersion: profile.Custom.MinTLSVersion,
			Ciphers:       profile.Custom.Ciphers,
		}
	default:
		return profileSpec{Type: profileIntermediate}
	}
}

// shouldHonorClusterTLSProfile reports whether the cluster TLS profile
// should be applied under the given adherence policy. Strict and unknown
// (including newer, unrecognized) values honor the profile; NoOpinion
// (unset) and LegacyAdheringComponentsOnly preserve Kueue's historical
// listener settings.
func shouldHonorClusterTLSProfile(adherence string) bool {
	switch adherence {
	case "", adherenceLegacyAdheringComponentsOnly:
		return false
	default:
		return true
	}
}

// withEffectiveProfile validates the profile and completes the policy with the
// parsed values and TLS options. An invalid or unusable profile is an error:
// the callers must treat it as fatal to startup.
func (p *Policy) withEffectiveProfile(spec profileSpec) (*Policy, error) {
	minVersion, minVersionName, ciphers, err := spec.toGo()
	if err != nil {
		return nil, err
	}
	// crypto/tls applies CipherSuites to TLS 1.2 and below only; TLS 1.3
	// suites are negotiated by the runtime.
	if minVersion >= tls.VersionTLS13 {
		ciphers = nil
	}
	// A profile that names cipher suites but leaves none usable by this
	// runtime must not silently enable Go's default suites.
	if minVersion < tls.VersionTLS13 && len(spec.Ciphers) > 0 && len(ciphers) == 0 {
		return nil, fmt.Errorf("TLS profile with minimum %s has no cipher suites usable by this runtime: %v",
			spec.MinTLSVersion, spec.Ciphers)
	}

	p.Honored = true
	p.EffectiveProfile = spec
	p.MinVersion = minVersion
	p.MinVersionName = minVersionName
	p.CipherIDs = ciphers
	for _, id := range ciphers {
		p.CipherNames = append(p.CipherNames, tls.CipherSuiteName(id))
	}
	p.TLSOpts = []func(*tls.Config){
		func(c *tls.Config) {
			c.MinVersion = minVersion
			if len(ciphers) > 0 {
				c.CipherSuites = ciphers
			}
			c.NextProtos = []string{"h2", "http/1.1"}
		},
	}
	return p, nil
}

// toGo resolves a profile spec to the Go minimum version and the cipher suites
// the runtime can negotiate at that version.
func (s profileSpec) toGo() (uint16, string, []uint16, error) {
	switch s.Type {
	case "", profileIntermediate:
		return tls.VersionTLS12, versionTLS12, intermediateCiphers, nil
	case profileModern:
		return tls.VersionTLS13, versionTLS13, nil, nil
	case profileOld:
		return tls.VersionTLS10, versionTLS10, oldCiphers, nil
	case profileCustom:
		minVersionName := s.MinTLSVersion
		if minVersionName == "" {
			minVersionName = versionTLS12
		}
		minVersion, ok := tlsVersionMap[minVersionName]
		if !ok {
			return 0, "", nil, fmt.Errorf("unsupported minimum TLS version %q", s.MinTLSVersion)
		}
		var ciphers []uint16
		for _, name := range s.Ciphers {
			if id, ok := cipherID(name, minVersion); ok {
				ciphers = append(ciphers, id)
			}
		}
		return minVersion, minVersionName, ciphers, nil
	default:
		return 0, "", nil, fmt.Errorf("unsupported TLS profile type %q", s.Type)
	}
}

func isTransient(err error) bool {
	return apierrors.IsServiceUnavailable(err) ||
		apierrors.IsTimeout(err) ||
		apierrors.IsServerTimeout(err) ||
		apierrors.IsTooManyRequests(err) ||
		errors.Is(err, context.DeadlineExceeded)
}

const (
	profileIntermediate = "Intermediate"
	profileModern       = "Modern"
	profileOld          = "Old"
	profileCustom       = "Custom"

	versionTLS10 = "VersionTLS10"
	versionTLS11 = "VersionTLS11"
	versionTLS12 = "VersionTLS12"
	versionTLS13 = "VersionTLS13"
)

var tlsVersionMap = map[string]uint16{
	versionTLS10: tls.VersionTLS10,
	versionTLS11: tls.VersionTLS11,
	versionTLS12: tls.VersionTLS12,
	versionTLS13: tls.VersionTLS13,
}

// ianaCipherSuites maps IANA cipher suite names to Go suite IDs, built from the
// crypto/tls runtime tables so suites this build cannot negotiate are never
// offered. InsecureCipherSuites is included because the Old profile names
// suites (3DES and RSA key exchange) that only appear there.
var ianaCipherSuites = func() map[string]uint16 {
	m := make(map[string]uint16, 32)
	for _, suite := range tls.CipherSuites() {
		m[suite.Name] = suite.ID
	}
	for _, suite := range tls.InsecureCipherSuites() {
		m[suite.Name] = suite.ID
	}
	return m
}()

// openSSLToIANA maps the OpenSSL cipher names used by OpenShift TLS profiles to
// the IANA names reported by crypto/tls. IANA names, including the TLS 1.3
// suite names, pass through unchanged and are not listed here.
var openSSLToIANA = map[string]string{
	"ECDHE-ECDSA-AES128-GCM-SHA256":        "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256",
	"ECDHE-RSA-AES128-GCM-SHA256":          "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
	"ECDHE-ECDSA-AES256-GCM-SHA384":        "TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384",
	"ECDHE-RSA-AES256-GCM-SHA384":          "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
	"ECDHE-ECDSA-CHACHA20-POLY1305":        "TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256",
	"ECDHE-RSA-CHACHA20-POLY1305":          "TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256",
	"ECDHE-ECDSA-CHACHA20-POLY1305-SHA256": "TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256",
	"ECDHE-RSA-CHACHA20-POLY1305-SHA256":   "TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256",
	"ECDHE-ECDSA-AES128-SHA256":            "TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256",
	"ECDHE-RSA-AES128-SHA256":              "TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256",
	"ECDHE-ECDSA-AES128-SHA":               "TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA",
	"ECDHE-RSA-AES128-SHA":                 "TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA",
	"ECDHE-ECDSA-AES256-SHA":               "TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA",
	"ECDHE-RSA-AES256-SHA":                 "TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA",
	"AES128-GCM-SHA256":                    "TLS_RSA_WITH_AES_128_GCM_SHA256",
	"AES256-GCM-SHA384":                    "TLS_RSA_WITH_AES_256_GCM_SHA384",
	"AES128-SHA256":                        "TLS_RSA_WITH_AES_128_CBC_SHA256",
	"AES128-SHA":                           "TLS_RSA_WITH_AES_128_CBC_SHA",
	"AES256-SHA":                           "TLS_RSA_WITH_AES_256_CBC_SHA",
	"DES-CBC3-SHA":                         "TLS_RSA_WITH_3DES_EDE_CBC_SHA",
}

// intermediateCiphers is the Go-negotiable subset of the OpenShift Intermediate
// profile cipher list.
var intermediateCiphers = []uint16{
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
}

// oldCiphers is the Go-negotiable subset of the OpenShift Old profile cipher
// list: the Intermediate suites plus the CBC and 3DES suites Go still supports.
// It is built from intermediateCiphers so the Old profile remains a strict
// superset by construction.
var oldCiphers = func() []uint16 {
	ciphers := make([]uint16, 0, len(intermediateCiphers)+12)
	ciphers = append(ciphers, intermediateCiphers...)
	return append(ciphers,
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256,
		tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
		tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
		tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
		tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_RSA_WITH_AES_128_CBC_SHA256,
		tls.TLS_RSA_WITH_AES_128_CBC_SHA,
		tls.TLS_RSA_WITH_AES_256_CBC_SHA,
		tls.TLS_RSA_WITH_3DES_EDE_CBC_SHA,
	)
}()

// tls13CipherIDs holds the suite IDs the runtime only negotiates with TLS 1.3.
// They are excluded from a profile whose minimum version is below 1.3 so a
// profile that lists only TLS 1.3 suites is reported as unusable instead of
// leaving the listener with no negotiable cipher.
var tls13CipherIDs = func() map[uint16]bool {
	ids := make(map[uint16]bool)
	for _, suite := range tls.CipherSuites() {
		tls13Only := true
		for _, version := range suite.SupportedVersions {
			if version < tls.VersionTLS13 {
				tls13Only = false
				break
			}
		}
		if tls13Only {
			ids[suite.ID] = true
		}
	}
	return ids
}()

// cipherID resolves a profile cipher name (OpenSSL or IANA) to a Go cipher ID
// usable at minVersion.
func cipherID(name string, minVersion uint16) (uint16, bool) {
	iana := name
	if _, ok := ianaCipherSuites[iana]; !ok {
		alias, ok := openSSLToIANA[name]
		if !ok {
			return 0, false
		}
		iana = alias
	}
	id, ok := ianaCipherSuites[iana]
	if !ok {
		return 0, false
	}
	if minVersion < tls.VersionTLS13 && tls13CipherIDs[id] {
		return 0, false
	}
	return id, true
}

type tlsProfileChangeKind int

const (
	tlsProfileUnchanged tlsProfileChangeKind = iota
	tlsProfileHonoredChanged
	tlsProfileAdherenceChanged
	tlsProfileSpecChanged
)

// checkTLSProfileChange classifies how an APIServer update changes the
// effective listener TLS configuration. The honored state is checked first so
// recovery from a transient fallback is detected even when the raw adherence
// and profile match the fallback values.
func checkTLSProfileChange(policy *Policy, tlsSpec *apiServerTLS) tlsProfileChangeKind {
	honored := shouldHonorClusterTLSProfile(tlsSpec.TLSAdherence)
	switch {
	case honored != policy.Honored:
		return tlsProfileHonoredChanged
	case tlsSpec.TLSAdherence != policy.Adherence:
		return tlsProfileAdherenceChanged
	case !profileSpecEqual(clusterProfile(tlsSpec.TLSSecurityProfile), policy.ClusterProfile):
		return tlsProfileSpecChanged
	default:
		return tlsProfileUnchanged
	}
}

// +kubebuilder:rbac:groups=config.openshift.io,resources=apiservers,verbs=get;list;watch

// SetupProfileWatcher registers a watcher that shuts the process down when
// the cluster TLS profile or adherence policy changes, so the listeners are
// rebuilt with the new policy on restart. It is a no-op outside OpenShift.
func SetupProfileWatcher(mgr manager.Manager, policy *Policy, shutdown func()) error {
	if policy == nil || !policy.APIAvailable {
		return nil
	}
	logger := log.Log.WithName("tls")

	dyn, err := dynamic.NewForConfig(mgr.GetConfig())
	if err != nil {
		return fmt.Errorf("creating dynamic client for TLS profile watcher: %w", err)
	}
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, 0, metav1.NamespaceAll,
		func(options *metav1.ListOptions) {
			options.FieldSelector = "metadata.name=" + apiServerName
		})
	informer := factory.ForResource(apiServerGVR).Informer()

	// The handler runs for the initial list as well as updates, so a change
	// made between the startup resolution and the watch being established is
	// still detected.
	check := func(obj interface{}) {
		apiServer, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return
		}
		tlsSpec, err := extractAPIServerTLS(apiServer)
		if err != nil {
			logger.Error(err, "Failed to parse APIServer TLS profile, ignoring the event")
			return
		}
		switch checkTLSProfileChange(policy, tlsSpec) {
		case tlsProfileHonoredChanged:
			logger.Info("TLS honored state changed, shutting down to rebuild listeners",
				"old", policy.Honored, "new", shouldHonorClusterTLSProfile(tlsSpec.TLSAdherence))
			shutdown()
		case tlsProfileAdherenceChanged:
			logger.Info("TLS adherence policy changed, shutting down to rebuild listeners",
				"old", policy.Adherence, "new", tlsSpec.TLSAdherence)
			shutdown()
		case tlsProfileSpecChanged:
			logger.Info("TLS profile changed, shutting down to rebuild listeners")
			shutdown()
		}
	}
	_, err = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    check,
		UpdateFunc: func(_, newObj interface{}) { check(newObj) },
	})
	if err != nil {
		return fmt.Errorf("registering TLS profile event handler: %w", err)
	}

	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		factory.Start(ctx.Done())
		factory.WaitForCacheSync(ctx.Done())
		<-ctx.Done()
		return nil
	}))
}

func profileSpecEqual(a, b profileSpec) bool {
	if a.Type != b.Type || a.MinTLSVersion != b.MinTLSVersion || len(a.Ciphers) != len(b.Ciphers) {
		return false
	}
	for i := range a.Ciphers {
		if a.Ciphers[i] != b.Ciphers[i] {
			return false
		}
	}
	return true
}

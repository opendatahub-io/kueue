/*
Copyright The Kubernetes Authors.

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
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"strings"

	validatingadmissionpolicy "k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	"k8s.io/apiserver/pkg/admission/plugin/resourcequota"
	mutatingwebhook "k8s.io/apiserver/pkg/admission/plugin/webhook/mutating"
	validatingwebhook "k8s.io/apiserver/pkg/admission/plugin/webhook/validating"
	openapinamer "k8s.io/apiserver/pkg/endpoints/openapi"
	genericapiserver "k8s.io/apiserver/pkg/server"
	genericoptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/client-go/pkg/version"
	utilversion "k8s.io/component-base/version"
	ctrl "sigs.k8s.io/controller-runtime"

	generatedopenapi "sigs.k8s.io/kueue/apis/visibility/openapi"
	visibilityv1beta1 "sigs.k8s.io/kueue/apis/visibility/v1beta1"
	"sigs.k8s.io/kueue/pkg/queue"
	kueuetls "sigs.k8s.io/kueue/pkg/tls"
	"sigs.k8s.io/kueue/pkg/visibility/api"

	_ "k8s.io/component-base/metrics/prometheus/restclient" // for client-go metrics registration
)

var (
	setupLog = ctrl.Log.WithName("visibility-server")
	// Admission plugins that are enabled by default in the kubeapi server
	// but are not required for the visibility server.
	disabledPlugins = []string{
		validatingadmissionpolicy.PluginName,
		resourcequota.PluginName,
		validatingwebhook.PluginName,
		mutatingwebhook.PluginName,
	}
)

// +kubebuilder:rbac:groups=flowcontrol.apiserver.k8s.io,resources=prioritylevelconfigurations,verbs=list;watch
// +kubebuilder:rbac:groups=flowcontrol.apiserver.k8s.io,resources=flowschemas,verbs=list;watch
// +kubebuilder:rbac:groups=flowcontrol.apiserver.k8s.io,resources=flowschemas/status,verbs=patch

// CreateAndStartVisibilityServer creates visibility server injecting KueueManager and starts it.
// The cluster TLS policy is applied to the secure listener when it is honored.
func CreateAndStartVisibilityServer(ctx context.Context, kueueMgr *queue.Manager, tlsPolicy *kueuetls.Policy) {
	config := newVisibilityServerConfig()
	if err := applyVisibilityServerOptions(config, tlsPolicy); err != nil {
		setupLog.Error(err, "Unable to apply VisibilityServerOptions")
		os.Exit(1)
	}

	visibilityServer, err := config.Complete().New("visibility-server", genericapiserver.NewEmptyDelegate())
	if err != nil {
		setupLog.Error(err, "Unable to create visibility server")
		os.Exit(1)
	}

	if err := api.Install(visibilityServer, kueueMgr); err != nil {
		setupLog.Error(err, "Unable to install visibility.kueue.x-k8s.io API")
		os.Exit(1)
	}

	if err := visibilityServer.PrepareRun().RunWithContext(ctx); err != nil {
		setupLog.Error(err, "Error running visibility server")
		os.Exit(1)
	}
}

func applyVisibilityServerOptions(config *genericapiserver.RecommendedConfig, tlsPolicy *kueuetls.Policy) error {
	o := genericoptions.NewRecommendedOptions("", api.Codecs.LegacyCodec(visibilityv1beta1.SchemeGroupVersion))
	o.Etcd = nil
	o.SecureServing.BindPort = 8082
	// The directory where TLS certs will be created
	o.SecureServing.ServerCert.CertDirectory = "/tmp"
	o.Admission.DisablePlugins = disabledPlugins
	o.SecureServing.MinTLSVersion, o.SecureServing.CipherSuites = secureServingTLS(tlsPolicy)
	if err := validateVisibilityTLSCipherSuites(tlsPolicy, o.SecureServing.CipherSuites); err != nil {
		return err
	}
	if secureServingHTTP2Disabled(tlsPolicy) {
		// The apiserver secure serving path configures HTTP/2 explicitly and
		// fails the listener at startup when the TLS 1.2 cipher list has no
		// ECDHE AES-128-GCM suite. Serve HTTP/1.1 only for such profiles
		// instead of failing to start.
		o.SecureServing.DisableHTTP2Serving = true
	}
	if err := o.SecureServing.MaybeDefaultWithSelfSignedCerts("localhost", nil, []net.IP{net.ParseIP("127.0.0.1")}); err != nil {
		return fmt.Errorf("error creating self-signed certificates: %v", err)
	}
	// NOTE: the profile's curve preferences (tlsPolicy.CurveIDs) are not
	// applied here. The apiserver's SecureServingInfo exposes only
	// MinTLSVersion and CipherSuites; its tls.Config is constructed
	// internally during Serve with no curve-preferences hook. The manager's
	// webhook and metrics listeners apply the full profile including curves;
	// the visibility listener's curve set stays at the Go defaults until the
	// apiserver serving path gains a curve knob.
	return o.ApplyTo(config)
}

// cipherSuitesAllECDSA reports whether every IANA cipher suite name uses
// ECDSA authentication, which an RSA certificate cannot negotiate.
func cipherSuitesAllECDSA(names []string) bool {
	if len(names) == 0 {
		return false
	}
	for _, name := range names {
		if !strings.Contains(name, "ECDSA") {
			return false
		}
	}
	return true
}

func validateVisibilityTLSCipherSuites(tlsPolicy *kueuetls.Policy, names []string) error {
	if tlsPolicy != nil && tlsPolicy.Honored && tlsPolicy.MinVersion < tls.VersionTLS13 &&
		len(names) > 0 && cipherSuitesAllECDSA(names) {
		return fmt.Errorf("honored TLS profile allows only ECDSA TLS 1.2 cipher suites, but the visibility listener uses an RSA self-signed certificate")
	}
	return nil
}

// secureServingTLS returns the OpenShift TLS version and IANA cipher suite
// names to configure on the secure listener. Empty values keep the
// component defaults, which is what a policy that is not honored requires.
func secureServingTLS(tlsPolicy *kueuetls.Policy) (string, []string) {
	if tlsPolicy == nil || !tlsPolicy.Honored {
		return "", nil
	}
	return tlsPolicy.MinVersionName, tlsPolicy.CipherNames
}

// secureServingHTTP2Disabled reports whether the visibility server must
// disable HTTP/2 serving for the policy. The apiserver secure serving path
// configures HTTP/2 explicitly and fails the listener at startup when the
// TLS 1.2 cipher list has no ECDHE AES-128-GCM suite, so such policies are
// served over HTTP/1.1 only instead of failing to start.
func secureServingHTTP2Disabled(tlsPolicy *kueuetls.Policy) bool {
	return tlsPolicy != nil && tlsPolicy.Honored && !tlsPolicy.CanServeHTTP2()
}

func newVisibilityServerConfig() *genericapiserver.RecommendedConfig {
	c := genericapiserver.NewRecommendedConfig(api.Codecs)
	versionInfo := version.Get()
	version := strings.Split(versionInfo.String(), "-")[0]
	// enable OpenAPI schemas
	c.Config.EffectiveVersion = utilversion.NewEffectiveVersion(version)
	c.Config.OpenAPIConfig = genericapiserver.DefaultOpenAPIConfig(generatedopenapi.GetOpenAPIDefinitions, openapinamer.NewDefinitionNamer(api.Scheme))
	c.Config.OpenAPIV3Config = genericapiserver.DefaultOpenAPIV3Config(generatedopenapi.GetOpenAPIDefinitions, openapinamer.NewDefinitionNamer(api.Scheme))
	c.Config.OpenAPIConfig.Info.Title = "Kueue visibility-server"
	c.Config.OpenAPIV3Config.Info.Title = "Kueue visibility-server"
	c.Config.OpenAPIConfig.Info.Version = version
	c.Config.OpenAPIV3Config.Info.Version = version

	c.EnableMetrics = true

	return c
}

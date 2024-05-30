/*
Copyright 2024 The Kubernetes Authors.

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

package jobframework

import (
	"context"
	"errors"
	"fmt"
	"os"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/go-logr/logr"
	"golang.org/x/exp/slices"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/watch"
	retrywatch "k8s.io/client-go/tools/watch"

	"sigs.k8s.io/kueue/pkg/controller/jobs/noop"
)

const (
	pytorchjobAPI = "pytorchjobs.kubeflow.org"
	rayclusterAPI = "rayclusters.ray.io"
)

var (
	errFailedMappingResource = errors.New("restMapper failed mapping resource")
)

// +kubebuilder:rbac:groups="apiextensions.k8s.io",resources=customresourcedefinitions,verbs=get;list;watch

// SetupControllers setups all controllers and webhooks for integrations.
// When the platform developers implement a separate kueue-manager to manage the in-house custom jobs,
// they can easily setup controllers and webhooks for the in-house custom jobs.
//
// Note that the first argument, "mgr" must be initialized on the outside of this function.
// In addition, if the manager uses the kueue's internal cert management for the webhooks,
// this function needs to be called after the certs get ready because the controllers won't work
// until the webhooks are operating, and the webhook won't work until the
// certs are all in place.
func SetupControllers(mgr ctrl.Manager, log logr.Logger, opts ...Option) error {
	options := ProcessOptions(opts...)

	return ForEachIntegration(func(name string, cb IntegrationCallbacks) error {
		logger := log.WithValues("jobFrameworkName", name)
		fwkNamePrefix := fmt.Sprintf("jobFrameworkName %q", name)

		if options.EnabledFrameworks.Has(name) {
			if cb.CanSupportIntegration != nil {
				if canSupport, err := cb.CanSupportIntegration(opts...); !canSupport || err != nil {
					log.Error(err, "Failed to configure reconcilers")
					os.Exit(1)
				}
			}
			gvk, err := apiutil.GVKForObject(cb.JobType, mgr.GetScheme())
			if err != nil {
				return fmt.Errorf("%s: %w: %w", fwkNamePrefix, errFailedMappingResource, err)
			}
			if !isAPIAvailable(context.TODO(), mgr, rayclusterAPI) {
				logger.Info("API not available, waiting for it to become available... - Skipping setup of controller and webhook")
				waitForAPI(context.TODO(), logger, mgr, rayclusterAPI, func() {
					setupComponents(mgr, logger, gvk, fwkNamePrefix, cb, opts...)
				})
			} else {
				logger.Info("API is available, setting up components...")
				setupComponents(mgr, logger, gvk, fwkNamePrefix, cb, opts...)
			}
		}
		if err := noop.SetupWebhook(mgr, cb.JobType); err != nil {
			return fmt.Errorf("%s: unable to create noop webhook: %w", fwkNamePrefix, err)
		}
		return nil
	})
}

func setupComponents(mgr ctrl.Manager, log logr.Logger, gvk schema.GroupVersionKind, fwkNamePrefix string, cb IntegrationCallbacks, opts ...Option) {
    // Attempt to get the REST mapping for the GVK
    if _, err := mgr.GetRESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version); err != nil {
        if !meta.IsNoMatchError(err) {
            log.Error(err, fmt.Sprintf("%s: unable to get REST mapping", fwkNamePrefix))
            return
        }
        log.Info("No matching API in the server for job framework, skipped setup of controller and webhook")
    } else {
        if err := setupControllerAndWebhook(mgr, gvk, fwkNamePrefix, cb, opts...); err != nil {
            log.Error(err, "Failed to set up controller and webhook")
        } else {
            log.Info("Set up controller and webhook for job framework")
        }
    }
}

func setupControllerAndWebhook(mgr ctrl.Manager, gvk schema.GroupVersionKind, fwkNamePrefix string, cb IntegrationCallbacks, opts ...Option) error {
    if err := cb.NewReconciler(
        mgr.GetClient(),
        mgr.GetEventRecorderFor(fmt.Sprintf("%s-%s-controller", gvk.Kind, "managerName")), // Ensure managerName is defined or fetched
        opts...,
    ).SetupWithManager(mgr); err != nil {
        return fmt.Errorf("%s: %w", fwkNamePrefix, err)
    }

    if err := cb.SetupWebhook(mgr, opts...); err != nil {
        return fmt.Errorf("%s: unable to create webhook: %w", fwkNamePrefix, err)
    }

    return nil
}

// SetupIndexes setups the indexers for integrations.
// When the platform developers implement a separate kueue-manager to manage the in-house custom jobs,
// they can easily setup indexers for the in-house custom jobs.
//
// Note that the second argument, "indexer" needs to be the fieldIndexer obtained from the Manager.
func SetupIndexes(ctx context.Context, indexer client.FieldIndexer, opts ...Option) error {
	options := ProcessOptions(opts...)
	return ForEachIntegration(func(name string, cb IntegrationCallbacks) error {
		if options.EnabledFrameworks.Has(name) {
			if err := cb.SetupIndexes(ctx, indexer); err != nil {
				return fmt.Errorf("jobFrameworkName %q: %w", name, err)
			}
		}
		return nil
	})
}

func isAPIAvailable(ctx context.Context, mgr ctrl.Manager, apiName string) bool {
	crdClient, err := apiextensionsclientset.NewForConfig(mgr.GetConfig())
	exitOnError(err, "unable to create CRD client")

	crdList, err := crdClient.ApiextensionsV1().CustomResourceDefinitions().List(ctx, metav1.ListOptions{})
	exitOnError(err, "unable to list CRDs")

	return slices.ContainsFunc(crdList.Items, func(crd apiextensionsv1.CustomResourceDefinition) bool {
		return crd.Name == apiName
	})
}

func waitForAPI(ctx context.Context, log logr.Logger, mgr ctrl.Manager, apiName string, action func()) {
	crdClient, err := apiextensionsclientset.NewForConfig(mgr.GetConfig())
	exitOnError(err, "unable to create CRD client")

	crdList, err := crdClient.ApiextensionsV1().CustomResourceDefinitions().List(ctx, metav1.ListOptions{})
	exitOnError(err, "unable to list CRDs")

	// If API is already available, just invoke action
	if slices.ContainsFunc(crdList.Items, func(crd apiextensionsv1.CustomResourceDefinition) bool {
		return crd.Name == apiName
	}) {
		action()
		return
	}

	// Wait for the API to become available then invoke action
	log.Info(fmt.Sprintf("API %v not available, setting up retry watcher", apiName))
	retryWatcher, err := retrywatch.NewRetryWatcher(crdList.ResourceVersion, &cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			return crdClient.ApiextensionsV1().CustomResourceDefinitions().List(ctx, metav1.ListOptions{})
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			return crdClient.ApiextensionsV1().CustomResourceDefinitions().Watch(ctx, metav1.ListOptions{})
		},
	})
	exitOnError(err, "unable to create retry watcher")

	defer retryWatcher.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-retryWatcher.ResultChan():
			switch event.Type {
			case watch.Error:
				exitOnError(apierrors.FromObject(event.Object), fmt.Sprintf("error watching for API %v", apiName))

			case watch.Added, watch.Modified:
				if crd := event.Object.(*apiextensionsv1.CustomResourceDefinition); crd.Name == apiName &&
					slices.ContainsFunc(crd.Status.Conditions, func(condition apiextensionsv1.CustomResourceDefinitionCondition) bool {
						return condition.Type == apiextensionsv1.Established && condition.Status == apiextensionsv1.ConditionTrue
					}) {
					log.Info(fmt.Sprintf("API %v installed, invoking deferred action", apiName))
					action()
					return
				}
			}
		}
	}
}

func exitOnError(err error, msg string) {
	if err != nil {
		fmt.Sprint(err, msg)
		os.Exit(1)
	}
}

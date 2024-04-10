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

package raycluster

import (
	"context"
	"fmt"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

type RayClusterWebhook struct {
	manageJobsWithoutQueueName bool
}

// SetupRayClusterWebhook configures the webhook for rayv1 RayCluster.
func SetupRayClusterWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	for _, opt := range opts {
		opt(&options)
	}
	wh := &RayClusterWebhook{
		manageJobsWithoutQueueName: options.ManageJobsWithoutQueueName,
	}
	return ctrl.NewWebhookManagedBy(mgr).
		For(&rayv1.RayCluster{}).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-ray-io-v1-raycluster,mutating=true,failurePolicy=fail,sideEffects=None,groups=ray.io,resources=rayclusters,verbs=create,versions=v1,name=mraycluster.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &RayClusterWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *RayClusterWebhook) Default(ctx context.Context, obj runtime.Object) error {
	job := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("raycluster-webhook")
	log.V(10).Info("Applying defaults", "job", klog.KObj(job))

	// We don't want to double count for a ray cluster created by a RayJob
	if owner := metav1.GetControllerOf(job.Object()); owner != nil && jobframework.IsOwnerManagedByKueue(owner) {
		log.Info("RayCluster is owned by RayJob")
		if job.Annotations == nil {
			job.Annotations = make(map[string]string)
		}
		if pwName, err := jobframework.GetWorkloadNameForOwnerRef(owner); err != nil {
			return err
		} else {
			job.Annotations[constants.ParentWorkloadAnnotation] = pwName
		}
		return nil
	}

	jobframework.ApplyDefaultForSuspend((*RayCluster)(job), w.manageJobsWithoutQueueName)
	return nil
}

// +kubebuilder:webhook:path=/validate-ray-io-v1-raycluster,mutating=false,failurePolicy=fail,sideEffects=None,groups=ray.io,resources=rayclusters,verbs=create;update,versions=v1,name=vraycluster.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &RayClusterWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayClusterWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	job := obj.(*rayv1.RayCluster)
	log := ctrl.LoggerFrom(ctx).WithName("raycluster-webhook")
	log.V(10).Info("Validating create", "job", klog.KObj(job))
	return nil, w.validateCreate(job).ToAggregate()
}

func (w *RayClusterWebhook) validateCreate(job *rayv1.RayCluster) field.ErrorList {
	var allErrors field.ErrorList
	kueueJob := (*RayCluster)(job)
	specPath := field.NewPath("spec")
	if job.Spec.HeadGroupSpec.EnableIngress == nil || *job.Spec.HeadGroupSpec.EnableIngress {
		allErrors = append(allErrors, field.Invalid(specPath.Child("headGroupSpec").Child("enableIngress"), job.Spec.HeadGroupSpec.EnableIngress, "creating RayCluster resources with EnableIngress set to true or unspecified is not allowed"))
	}

	if w.manageJobsWithoutQueueName || jobframework.QueueName(kueueJob) != "" {
		spec := &job.Spec

		// TODO revisit once Support dynamically sized (elastic) jobs #77 is implemented
		// Should not use auto scaler. Once the resources are reserved by queue the cluster should do it's best to use them.
		if ptr.Deref(spec.EnableInTreeAutoscaling, false) {
			allErrors = append(allErrors, field.Invalid(specPath.Child("enableInTreeAutoscaling"), spec.EnableInTreeAutoscaling, "a kueue managed job should not use autoscaling"))
		}

		// Should limit the worker count to 8 - 1 (max podSets num - cluster head)
		if len(spec.WorkerGroupSpecs) > 7 {
			allErrors = append(allErrors, field.TooMany(specPath.Child("workerGroupSpecs"), len(spec.WorkerGroupSpecs), 7))
		}

		// None of the workerGroups should be named "head"
		for i := range spec.WorkerGroupSpecs {
			if spec.WorkerGroupSpecs[i].GroupName == headGroupPodSetName {
				allErrors = append(allErrors, field.Forbidden(specPath.Child("workerGroupSpecs").Index(i).Child("groupName"), fmt.Sprintf("%q is reserved for the head group", headGroupPodSetName)))
			}
		}
	}

	allErrors = append(allErrors, jobframework.ValidateCreateForQueueName(kueueJob)...)
	return allErrors
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayClusterWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldJob := oldObj.(*rayv1.RayCluster)
	newJob := newObj.(*rayv1.RayCluster)
	log := ctrl.LoggerFrom(ctx).WithName("raycluster-webhook")
	if w.manageJobsWithoutQueueName || jobframework.QueueName((*RayCluster)(newJob)) != "" {
		log.Info("Validating update", "job", klog.KObj(newJob))
		allErrors := jobframework.ValidateUpdateForQueueName((*RayCluster)(oldJob), (*RayCluster)(newJob))
		allErrors = append(allErrors, w.validateCreate(newJob)...)
		allErrors = append(allErrors, jobframework.ValidateUpdateForWorkloadPriorityClassName((*RayCluster)(oldJob), (*RayCluster)(newJob))...)
		return nil, allErrors.ToAggregate()
	}
	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayClusterWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

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
// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	"context"
	json "encoding/json"
	"fmt"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	testing "k8s.io/client-go/testing"
	v1beta1 "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	kueuev1beta1 "sigs.k8s.io/kueue/client-go/applyconfiguration/kueue/v1beta1"
)

// FakeMultiKueueClusters implements MultiKueueClusterInterface
type FakeMultiKueueClusters struct {
	Fake *FakeKueueV1beta1
}

var multikueueclustersResource = v1beta1.SchemeGroupVersion.WithResource("multikueueclusters")

var multikueueclustersKind = v1beta1.SchemeGroupVersion.WithKind("MultiKueueCluster")

// Get takes name of the multiKueueCluster, and returns the corresponding multiKueueCluster object, and an error if there is any.
func (c *FakeMultiKueueClusters) Get(ctx context.Context, name string, options v1.GetOptions) (result *v1beta1.MultiKueueCluster, err error) {
	emptyResult := &v1beta1.MultiKueueCluster{}
	obj, err := c.Fake.
		Invokes(testing.NewRootGetActionWithOptions(multikueueclustersResource, name, options), emptyResult)
	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1beta1.MultiKueueCluster), err
}

// List takes label and field selectors, and returns the list of MultiKueueClusters that match those selectors.
func (c *FakeMultiKueueClusters) List(ctx context.Context, opts v1.ListOptions) (result *v1beta1.MultiKueueClusterList, err error) {
	emptyResult := &v1beta1.MultiKueueClusterList{}
	obj, err := c.Fake.
		Invokes(testing.NewRootListActionWithOptions(multikueueclustersResource, multikueueclustersKind, opts), emptyResult)
	if obj == nil {
		return emptyResult, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &v1beta1.MultiKueueClusterList{ListMeta: obj.(*v1beta1.MultiKueueClusterList).ListMeta}
	for _, item := range obj.(*v1beta1.MultiKueueClusterList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

// Watch returns a watch.Interface that watches the requested multiKueueClusters.
func (c *FakeMultiKueueClusters) Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error) {
	return c.Fake.
		InvokesWatch(testing.NewRootWatchActionWithOptions(multikueueclustersResource, opts))
}

// Create takes the representation of a multiKueueCluster and creates it.  Returns the server's representation of the multiKueueCluster, and an error, if there is any.
func (c *FakeMultiKueueClusters) Create(ctx context.Context, multiKueueCluster *v1beta1.MultiKueueCluster, opts v1.CreateOptions) (result *v1beta1.MultiKueueCluster, err error) {
	emptyResult := &v1beta1.MultiKueueCluster{}
	obj, err := c.Fake.
		Invokes(testing.NewRootCreateActionWithOptions(multikueueclustersResource, multiKueueCluster, opts), emptyResult)
	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1beta1.MultiKueueCluster), err
}

// Update takes the representation of a multiKueueCluster and updates it. Returns the server's representation of the multiKueueCluster, and an error, if there is any.
func (c *FakeMultiKueueClusters) Update(ctx context.Context, multiKueueCluster *v1beta1.MultiKueueCluster, opts v1.UpdateOptions) (result *v1beta1.MultiKueueCluster, err error) {
	emptyResult := &v1beta1.MultiKueueCluster{}
	obj, err := c.Fake.
		Invokes(testing.NewRootUpdateActionWithOptions(multikueueclustersResource, multiKueueCluster, opts), emptyResult)
	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1beta1.MultiKueueCluster), err
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
func (c *FakeMultiKueueClusters) UpdateStatus(ctx context.Context, multiKueueCluster *v1beta1.MultiKueueCluster, opts v1.UpdateOptions) (result *v1beta1.MultiKueueCluster, err error) {
	emptyResult := &v1beta1.MultiKueueCluster{}
	obj, err := c.Fake.
		Invokes(testing.NewRootUpdateSubresourceActionWithOptions(multikueueclustersResource, "status", multiKueueCluster, opts), emptyResult)
	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1beta1.MultiKueueCluster), err
}

// Delete takes name of the multiKueueCluster and deletes it. Returns an error if one occurs.
func (c *FakeMultiKueueClusters) Delete(ctx context.Context, name string, opts v1.DeleteOptions) error {
	_, err := c.Fake.
		Invokes(testing.NewRootDeleteActionWithOptions(multikueueclustersResource, name, opts), &v1beta1.MultiKueueCluster{})
	return err
}

// DeleteCollection deletes a collection of objects.
func (c *FakeMultiKueueClusters) DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error {
	action := testing.NewRootDeleteCollectionActionWithOptions(multikueueclustersResource, opts, listOpts)

	_, err := c.Fake.Invokes(action, &v1beta1.MultiKueueClusterList{})
	return err
}

// Patch applies the patch and returns the patched multiKueueCluster.
func (c *FakeMultiKueueClusters) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1beta1.MultiKueueCluster, err error) {
	emptyResult := &v1beta1.MultiKueueCluster{}
	obj, err := c.Fake.
		Invokes(testing.NewRootPatchSubresourceActionWithOptions(multikueueclustersResource, name, pt, data, opts, subresources...), emptyResult)
	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1beta1.MultiKueueCluster), err
}

// Apply takes the given apply declarative configuration, applies it and returns the applied multiKueueCluster.
func (c *FakeMultiKueueClusters) Apply(ctx context.Context, multiKueueCluster *kueuev1beta1.MultiKueueClusterApplyConfiguration, opts v1.ApplyOptions) (result *v1beta1.MultiKueueCluster, err error) {
	if multiKueueCluster == nil {
		return nil, fmt.Errorf("multiKueueCluster provided to Apply must not be nil")
	}
	data, err := json.Marshal(multiKueueCluster)
	if err != nil {
		return nil, err
	}
	name := multiKueueCluster.Name
	if name == nil {
		return nil, fmt.Errorf("multiKueueCluster.Name must be provided to Apply")
	}
	emptyResult := &v1beta1.MultiKueueCluster{}
	obj, err := c.Fake.
		Invokes(testing.NewRootPatchSubresourceActionWithOptions(multikueueclustersResource, *name, types.ApplyPatchType, data, opts.ToPatchOptions()), emptyResult)
	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1beta1.MultiKueueCluster), err
}

// ApplyStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating ApplyStatus().
func (c *FakeMultiKueueClusters) ApplyStatus(ctx context.Context, multiKueueCluster *kueuev1beta1.MultiKueueClusterApplyConfiguration, opts v1.ApplyOptions) (result *v1beta1.MultiKueueCluster, err error) {
	if multiKueueCluster == nil {
		return nil, fmt.Errorf("multiKueueCluster provided to Apply must not be nil")
	}
	data, err := json.Marshal(multiKueueCluster)
	if err != nil {
		return nil, err
	}
	name := multiKueueCluster.Name
	if name == nil {
		return nil, fmt.Errorf("multiKueueCluster.Name must be provided to Apply")
	}
	emptyResult := &v1beta1.MultiKueueCluster{}
	obj, err := c.Fake.
		Invokes(testing.NewRootPatchSubresourceActionWithOptions(multikueueclustersResource, *name, types.ApplyPatchType, data, opts.ToPatchOptions(), "status"), emptyResult)
	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1beta1.MultiKueueCluster), err
}

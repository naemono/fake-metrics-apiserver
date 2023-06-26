/*
Copyright 2017 The Kubernetes Authors.

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

package provider

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/emicklei/go-restful/v3"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
	"k8s.io/metrics/pkg/apis/custom_metrics"
	"k8s.io/metrics/pkg/apis/external_metrics"

	"github.com/naemono/fake-metrics-apiserver/pkg/apiserver/metrics"
	"github.com/naemono/fake-metrics-apiserver/pkg/provider"
	"github.com/naemono/fake-metrics-apiserver/pkg/provider/defaults"
	"github.com/naemono/fake-metrics-apiserver/pkg/provider/helpers"
)

// CustomMetricResource wraps provider.CustomMetricInfo in a struct which stores the Name and Namespace of the resource
// So that we can accurately store and retrieve the metric as if this were an actual metrics server.
type CustomMetricResource struct {
	provider.CustomMetricInfo
	types.NamespacedName
	labels string
}

// externalMetric provides examples for metrics which would otherwise be reported from an external source
type externalMetric struct {
	info   provider.ExternalMetricInfo
	labels map[string]string
	value  external_metrics.ExternalMetricValue
}

var (
	testingExternalMetrics = []externalMetric{
		{
			info: provider.ExternalMetricInfo{
				Metric: "my-external-metric",
			},
			labels: map[string]string{"foo": "bar"},
			value: external_metrics.ExternalMetricValue{
				MetricName: "my-external-metric",
				MetricLabels: map[string]string{
					"foo": "bar",
				},
				Value: *resource.NewQuantity(42, resource.DecimalSI),
			},
		},
		{
			info: provider.ExternalMetricInfo{
				Metric: "my-external-metric",
			},
			labels: map[string]string{"foo": "baz"},
			value: external_metrics.ExternalMetricValue{
				MetricName: "my-external-metric",
				MetricLabels: map[string]string{
					"foo": "baz",
				},
				Value: *resource.NewQuantity(43, resource.DecimalSI),
			},
		},
		{
			info: provider.ExternalMetricInfo{
				Metric: "other-external-metric",
			},
			labels: map[string]string{},
			value: external_metrics.ExternalMetricValue{
				MetricName:   "other-external-metric",
				MetricLabels: map[string]string{},
				Value:        *resource.NewQuantity(44, resource.DecimalSI),
			},
		},
	}
)

type metricValue struct {
	labels    labels.Set
	value     resource.Quantity
	timestamp metav1.Time
}

var _ provider.MetricsProvider = &testingProvider{}

// testingProvider is a sample implementation of provider.MetricsProvider which stores a map of fake metrics
type testingProvider struct {
	defaults.DefaultCustomMetricsProvider
	defaults.DefaultExternalMetricsProvider
	client dynamic.Interface
	mapper apimeta.RESTMapper

	valuesLock              sync.RWMutex
	values                  map[CustomMetricResource]metricValue
	hpaTrackingLock         sync.RWMutex
	hpaTracking             map[CustomMetricResource]metav1.Time
	externalMetrics         []externalMetric
	scaling                 bool
	hpaPollDurationObserver metrics.HPAPollDurationObserver
}

// NewFakeProvider returns an instance of testingProvider, along with its restful.WebService that opens endpoints to post new fake metrics
func NewFakeProvider(client dynamic.Interface, mapper apimeta.RESTMapper, scaling bool, hpaPollDurationObserver metrics.HPAPollDurationObserver) (provider.MetricsProvider, *restful.WebService) {
	provider := &testingProvider{
		client:                  client,
		mapper:                  mapper,
		values:                  make(map[CustomMetricResource]metricValue),
		hpaTracking:             make(map[CustomMetricResource]metav1.Time),
		externalMetrics:         testingExternalMetrics,
		scaling:                 scaling,
		hpaPollDurationObserver: hpaPollDurationObserver,
	}
	return provider, provider.webService()
}

// webService creates a restful.WebService with routes set up for receiving fake metrics
// These writing routes have been set up to be identical to the format of routes which metrics are read from.
// There are 3 metric types available: namespaced, root-scoped, and namespaces.
// (Note: Namespaces, we're assuming, are themselves namespaced resources, but for consistency with how metrics are retreived they have a separate route)
func (p *testingProvider) webService() *restful.WebService {
	ws := new(restful.WebService)

	ws.Path("/write-metrics")

	// Namespaced resources
	ws.Route(ws.POST("/namespaces/{namespace}/{resourceType}/{name}/{metric}").To(p.updateMetric).
		Param(ws.BodyParameter("value", "value to set metric").DataType("integer").DefaultValue("0")))

	// Root-scoped resources
	ws.Route(ws.POST("/{resourceType}/{name}/{metric}").To(p.updateMetric).
		Param(ws.BodyParameter("value", "value to set metric").DataType("integer").DefaultValue("0")))

	// Namespaces, where {resourceType} == "namespaces" to match API
	ws.Route(ws.POST("/{resourceType}/{name}/metrics/{metric}").To(p.updateMetric).
		Param(ws.BodyParameter("value", "value to set metric").DataType("integer").DefaultValue("0")))
	return ws
}

// updateMetric writes the metric provided by a restful request and stores it in memory
func (p *testingProvider) updateMetric(request *restful.Request, response *restful.Response) {
	p.valuesLock.Lock()
	defer p.valuesLock.Unlock()

	namespace := request.PathParameter("namespace")
	resourceType := request.PathParameter("resourceType")
	namespaced := false
	if len(namespace) > 0 || resourceType == "namespaces" {
		namespaced = true
	}
	name := request.PathParameter("name")
	metricName := request.PathParameter("metric")

	value := new(resource.Quantity)
	err := request.ReadEntity(value)
	if err != nil {
		if err := response.WriteErrorString(http.StatusBadRequest, err.Error()); err != nil {
			klog.Errorf("Error writing error: %s", err)
		}
		return
	}

	groupResource := schema.ParseGroupResource(resourceType)

	metricLabels := labels.Set{}
	sel := request.QueryParameter("labels")
	if len(sel) > 0 {
		metricLabels, err = labels.ConvertSelectorToLabelsMap(sel)
		if err != nil {
			if err := response.WriteErrorString(http.StatusBadRequest, err.Error()); err != nil {
				klog.Errorf("Error writing error: %s", err)
			}
			return
		}
	}

	info := provider.CustomMetricInfo{
		GroupResource: groupResource,
		Metric:        metricName,
		Namespaced:    namespaced,
	}

	info, _, err = info.Normalized(p.mapper)
	if err != nil {
		klog.Errorf("Error normalizing info: %s", err)
	}
	namespacedName := types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}

	metricInfo := CustomMetricResource{
		CustomMetricInfo: info,
		NamespacedName:   namespacedName,
	}
	p.values[metricInfo] = metricValue{
		labels:    metricLabels,
		value:     *value,
		timestamp: metav1.Now(),
	}
}

// valueFor is a helper function to get just the value of a specific metric
func (p *testingProvider) valueFor(info provider.CustomMetricInfo, name types.NamespacedName, metricSelector labels.Selector) (metricValue, error) {
	info, _, err := info.Normalized(p.mapper)
	if err != nil {
		return metricValue{}, err
	}
	metricInfo := CustomMetricResource{
		CustomMetricInfo: info,
		NamespacedName:   name,
	}

	klog.InfoS("attempting to get values", "values", metricInfo)

	value, found := p.values[metricInfo]
	if !found {
		klog.Info("metric not found, creating new fake metric")
		set, err := labels.ConvertSelectorToLabelsMap(metricSelector.String())
		if err != nil {
			return metricValue{}, err
		}
		value := resource.MustParse("1")
		v := metricValue{
			labels:    set,
			value:     value,
			timestamp: metav1.Now(),
		}
		p.values[metricInfo] = v
		return v, nil
	}

	if !metricSelector.Matches(value.labels) {
		klog.Info("metricselector didn't match labels of metric value, returning no metric")
		return metricValue{}, provider.NewMetricNotFoundForSelectorError(info.GroupResource, info.Metric, name.Name, metricSelector)
	}

	if p.scaling {
		value.value.Add(resource.MustParse("1"))
		value.timestamp = metav1.Now()
		p.values[metricInfo] = value
	}
	return value, nil
}

// metricFor is a helper function which formats a value, metric, and object info into a MetricValue which can be returned by the metrics API
func (p *testingProvider) metricFor(value metricValue, name types.NamespacedName, _ labels.Selector, info provider.CustomMetricInfo, metricSelector labels.Selector) (*custom_metrics.MetricValue, error) {
	objRef, err := helpers.ReferenceFor(p.mapper, name, info)
	if err != nil {
		return nil, err
	}

	metric := &custom_metrics.MetricValue{
		DescribedObject: objRef,
		Metric: custom_metrics.MetricIdentifier{
			Name: info.Metric,
		},
		Timestamp: value.timestamp,
		Value:     value.value,
	}

	if len(metricSelector.String()) > 0 {
		sel, err := metav1.ParseToLabelSelector(metricSelector.String())
		if err != nil {
			return nil, err
		}
		metric.Metric.Selector = sel
	}

	return metric, nil
}

// metricsFor is a wrapper used by GetMetricBySelector to format several metrics which match a resource selector
func (p *testingProvider) metricsFor(namespace string, selector labels.Selector, info provider.CustomMetricInfo, metricSelector labels.Selector) (*custom_metrics.MetricValueList, error) {
	names, err := helpers.ListObjectNames(p.mapper, p.client, namespace, selector, info)
	if err != nil {
		return nil, err
	}
	klog.Info("got results from list", "names", names)

	res := make([]custom_metrics.MetricValue, 0, len(names))
	for _, name := range names {
		namespacedName := types.NamespacedName{Name: name, Namespace: namespace}
		value, err := p.valueFor(info, namespacedName, metricSelector)
		if err != nil {
			if apierr.IsNotFound(err) {
				continue
			}
			return nil, err
		}

		metric, err := p.metricFor(value, namespacedName, selector, info, metricSelector)
		if err != nil {
			return nil, err
		}
		res = append(res, *metric)
	}

	hpa, err := GetHPAResource(p.mapper, p.client, namespace, selector, info)
	if err != nil {
		return nil, fmt.Errorf("while retrieving hpa resource: %w", err)
	}

	if err = p.updateHPATracking(hpa); err != nil {
		return nil, err
	}

	return &custom_metrics.MetricValueList{
		Items: res,
	}, nil
}

func (p *testingProvider) updateHPATracking(hpa *CustomMetricResource) error {
	if hpa == nil {
		return fmt.Errorf("nil hpa when updating hpa tracking")
	}
	p.hpaTrackingLock.Lock()
	defer p.hpaTrackingLock.Unlock()

	now := metav1.Now()

	if prev, ok := p.hpaTracking[*hpa]; ok {
		p.hpaPollDurationObserver.Observe(hpa.Namespace, hpa.Name, hpa.GroupResource.Group, hpa.GroupResource.Resource, hpa.labels, now.Sub(prev.Time))
	}

	p.hpaTracking[*hpa] = now

	return nil
}

func (p *testingProvider) GetMetricByName(_ context.Context, name types.NamespacedName, info provider.CustomMetricInfo, metricSelector labels.Selector) (*custom_metrics.MetricValue, error) {
	p.valuesLock.RLock()
	defer p.valuesLock.RUnlock()

	value, err := p.valueFor(info, name, metricSelector)
	if err != nil {
		return nil, err
	}
	return p.metricFor(value, name, labels.Everything(), info, metricSelector)
}

func (p *testingProvider) GetMetricBySelector(_ context.Context, namespace string, selector labels.Selector, info provider.CustomMetricInfo, metricSelector labels.Selector) (*custom_metrics.MetricValueList, error) {
	p.valuesLock.RLock()
	defer p.valuesLock.RUnlock()

	return p.metricsFor(namespace, selector, info, metricSelector)
}

func (p *testingProvider) GetExternalMetric(_ context.Context, _ string, metricSelector labels.Selector, info provider.ExternalMetricInfo) (*external_metrics.ExternalMetricValueList, error) {
	p.valuesLock.RLock()
	defer p.valuesLock.RUnlock()

	matchingMetrics := []external_metrics.ExternalMetricValue{}
	for _, metric := range p.externalMetrics {
		if metric.info.Metric == info.Metric &&
			metricSelector.Matches(labels.Set(metric.labels)) {
			metricValue := metric.value
			metricValue.Timestamp = metav1.Now()
			matchingMetrics = append(matchingMetrics, metricValue)
		}
	}
	return &external_metrics.ExternalMetricValueList{
		Items: matchingMetrics,
	}, nil
}

type hpaAutoscaler struct {
	Spec hpaAutoscalerSpec `mapstructure:"spec"`
}

type hpaAutoscalerSpec struct {
	ScaleTargetRef hpaScaleTargetRef `mapstructure:"scaleTargetRef"`
}

type hpaScaleTargetRef struct {
	APIVersion string `mapstructure:"apiVersion"`
	Kind       string `mapstructure:"kind"`
	Name       string `mapstructure:"name"`
}

func GetHPAResource(mapper apimeta.RESTMapper, client dynamic.Interface, namespace string, selector labels.Selector, info provider.CustomMetricInfo) (*CustomMetricResource, error) {
	res, err := helpers.ResourceFor(mapper, provider.CustomMetricInfo{
		GroupResource: schema.GroupResource{
			Group:    "autoscaling",
			Resource: "horizontalpodautoscalers",
		},
	})
	if err != nil {
		return nil, err
	}

	var resClient dynamic.ResourceInterface
	if info.Namespaced {
		resClient = client.Resource(res).Namespace(namespace)
	} else {
		resClient = client.Resource(res)
	}

	matchingObjectsRaw, err := resClient.List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	if !apimeta.IsListType(matchingObjectsRaw) {
		return nil, fmt.Errorf("result of label selector list operation was not a list")
	}

	if matchingObjectsRaw != nil && len(matchingObjectsRaw.Items) == 0 {
		return nil, fmt.Errorf("while finding hpa in namespace %s", namespace)
	}

	// obj := matchingObjectsRaw.Items[0].Object
	// matchingObjectsRaw.Items[0].GetName()
	// var autoscaler *hpaAutoscaler

	// if err := mapstructure.Decode(obj, autoscaler); err != nil {
	// 	return nil, fmt.Errorf("couldn't translate hpa object to hpa struct.")
	// }

	// if autoscaler == nil {
	// 	return nil, fmt.Errorf("autoscaler nil after decode")
	// }

	return &CustomMetricResource{
		CustomMetricInfo: provider.CustomMetricInfo{
			GroupResource: schema.ParseGroupResource("horizontalpodautoscalers.autoscaling"),
			Namespaced:    info.Namespaced,
			Metric:        info.Metric,
		},
		NamespacedName: types.NamespacedName{
			// ASSUMPTION: currently assuming first hpa found in namespace is correct.
			// TODO (mmontgomery): don't make this assumption.
			Name:      matchingObjectsRaw.Items[0].GetName(),
			Namespace: matchingObjectsRaw.Items[0].GetNamespace(),
		},
		labels: selector.String(),
	}, nil
}

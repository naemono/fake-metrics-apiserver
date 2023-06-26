/*
Copyright 2022 The Kubernetes Authors.

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

// Package metrics provides metrics and instrumentation functions for the
// metrics API server.
package metrics

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/metrics"
	"k8s.io/utils/clock"
)

var (
	metricFreshness = metrics.NewHistogramVec(&metrics.HistogramOpts{
		Namespace:      "metrics_apiserver",
		Name:           "metric_freshness_seconds",
		Help:           "Freshness of metrics exported",
		StabilityLevel: metrics.ALPHA,
		Buckets:        metrics.ExponentialBuckets(1, 1.364, 20),
	}, []string{"group"})
	metricHPAPollDuration = metrics.NewHistogramVec(&metrics.HistogramOpts{
		Namespace:      "metrics_apiserver",
		Name:           "hpa_poll_duration_seconds",
		Help:           "Durationin seconds between polling of each hpa resource",
		StabilityLevel: metrics.ALPHA,
		Buckets:        []float64{15.0, 16.0, 17.0, 18.0, 19.0, 20.0, 30.0, 40.0, 50.0, 60.0, 90.0, 120.0, 240.0},
	}, []string{"namespace", "name", "group", "resource", "labels"})
)

// RegisterMetrics registers API server metrics, given a registration function.
func RegisterMetrics(registrationFunc func(metrics.Registerable) error) error {
	if err := registrationFunc(metricFreshness); err != nil {
		return err
	}
	return registrationFunc(metricHPAPollDuration)
}

// FreshnessObserver captures individual observations of the timestamp of
// metrics.
type FreshnessObserver interface {
	Observe(timestamp metav1.Time)
}

// NewFreshnessObserver creates a FreshnessObserver for a given metrics API group.
func NewFreshnessObserver(apiGroup string) FreshnessObserver {
	return &freshnessObserver{
		apiGroup: apiGroup,
		clock:    clock.RealClock{},
	}
}

type freshnessObserver struct {
	apiGroup string
	clock    clock.PassiveClock
}

func (o *freshnessObserver) Observe(timestamp metav1.Time) {
	metricFreshness.WithLabelValues(o.apiGroup).
		Observe(o.clock.Since(timestamp.Time).Seconds())
}

// FreshnessObserver captures individual observations of the timestamp of
// metrics.
type HPAPollDurationObserver interface {
	Observe(namespace, name, group, resource, labels string, dur time.Duration)
}

// NewFreshnessObserver creates a FreshnessObserver for a given metrics API group.
func NewHPAPollDurationObserver() HPAPollDurationObserver {
	return &hpaPollDuartionObserver{}
}

type hpaPollDuartionObserver struct {
}

func (o *hpaPollDuartionObserver) Observe(namespace, name, group, resource, labels string, dur time.Duration) {
	metricHPAPollDuration.WithLabelValues(namespace, name, group, resource, labels).
		Observe(dur.Seconds())
}

/*
Copyright 2018 The Kubernetes Authors.

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

package main

import (
	"net/http"
	"os"
	"time"

	"github.com/emicklei/go-restful/v3"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/component-base/logs"
	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"

	"github.com/naemono/fake-metrics-apiserver/pkg/apiserver/metrics"
	basecmd "github.com/naemono/fake-metrics-apiserver/pkg/cmd"
	"github.com/naemono/fake-metrics-apiserver/pkg/provider"
	fakeprov "github.com/naemono/fake-metrics-apiserver/pkg/fake-adapter/provider"
)

type SampleAdapter struct {
	basecmd.AdapterBase

	// Message is printed on successful startup
	Message string
	Scaling bool
}

func (a *SampleAdapter) makeProviderOrDie() (provider.MetricsProvider, *restful.WebService) {
	client, err := a.DynamicClient()
	if err != nil {
		klog.Fatalf("unable to construct dynamic client: %v", err)
	}

	mapper, err := a.RESTMapper()
	if err != nil {
		klog.Fatalf("unable to construct discovery REST mapper: %v", err)
	}

	hpaPollDurationObserver := metrics.NewHPAPollDurationObserver()
	klog.InfoS("getting new fake provider", "scaling", a.Scaling)
	return fakeprov.NewFakeProvider(client, mapper, a.Scaling, hpaPollDurationObserver)
}

func main() {
	logs.InitLogs()
	defer logs.FlushLogs()

	cmd := &SampleAdapter{}
	cmd.Name = "fake-adapter"

	cmd.Flags().StringVar(&cmd.Message, "msg", "starting adapter...", "startup message")
	cmd.Flags().BoolVar(&cmd.Scaling, "scaling", false, "whether to scale the resource each time it's called")
	logs.AddFlags(cmd.Flags())
	if err := cmd.Flags().Parse(os.Args); err != nil {
		klog.Fatalf("unable to parse flags: %v", err)
	}

	testProvider, webService := cmd.makeProviderOrDie()
	cmd.WithCustomMetrics(testProvider)
	cmd.WithExternalMetrics(testProvider)

	if err := metrics.RegisterMetrics(legacyregistry.Register); err != nil {
		klog.Fatal("unable to register metrics: %v", err)
	}

	klog.Infof(cmd.Message)
	// Set up POST endpoint for writing fake metric values
	restful.DefaultContainer.Add(webService)
	go func() {
		// Open port for POSTing fake metrics
		server := &http.Server{
			Addr:              ":8080",
			ReadHeaderTimeout: 3 * time.Second,
		}
		klog.Fatal(server.ListenAndServe())
	}()
	if err := cmd.Run(wait.NeverStop); err != nil {
		klog.Fatalf("unable to run custom metrics adapter: %v", err)
	}
}
/*
Copyright 2026 The kubepkg Authors.

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

package controller

import (
	"context"
	"testing"

	"github.com/kuberoot-dev/kubepkg/api/v1"
	"github.com/kuberoot-dev/kubepkg/pkg/backend"
	"github.com/kuberoot-dev/kubepkg/pkg/source"
)

func TestChartPreparerMirrorKeepsTheDigest(t *testing.T) {
	comp := &v1.Component{Name: "app", Chart: &v1.ChartRef{Repository: "https://charts.example.org", Name: "app", Version: "1.0.0"}}
	src := &v1.PackageSource{}
	var plain, mirrored backend.Component
	d1, err := ChartPreparer{}.Prepare(context.Background(), src, nil, comp, &plain)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := ChartPreparer{Mirror: "oci://registry.internal/mirror"}.Prepare(context.Background(), src, nil, comp, &mirrored)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Error("turning a mirror on changes the chart digest, so every package would get a new revision")
	}
	if mirrored.Chart.Repository != "oci://registry.internal/mirror/charts.example.org" || plain.Chart.Repository != "https://charts.example.org" {
		t.Errorf("repositories: %s, %s", plain.Chart.Repository, mirrored.Chart.Repository)
	}
}

func TestChartPreparerMarksPlainHTTPRegistries(t *testing.T) {
	p := ChartPreparer{PlainHTTPRegistries: source.NewRegistries("registry.lab:5000")}
	for repo, want := range map[string]bool{
		"oci://registry.lab:5000/charts":    true,
		"oci://ghcr.io/kuberoot-dev/charts": false,
		"http://registry.lab:5000/charts":   false, // a Helm repository: its scheme decides
	} {
		comp := &v1.Component{Name: "app", Chart: &v1.ChartRef{Repository: repo, Name: "app", Version: "1.0.0"}}
		var c backend.Component
		if _, err := p.Prepare(context.Background(), &v1.PackageSource{}, nil, comp, &c); err != nil {
			t.Fatal(err)
		}
		if c.Chart.PlainHTTP != want {
			t.Errorf("%s: plain HTTP %v", repo, c.Chart.PlainHTTP)
		}
	}
}

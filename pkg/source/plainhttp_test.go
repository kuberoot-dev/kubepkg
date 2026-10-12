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

package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRegistriesByOwner(t *testing.T) {
	var none *Registries
	if none.Has("localhost:5000") {
		t.Fatal("a nil set has nothing")
	}
	r := NewRegistries("mirror.local:5000")
	r.Set("repository/lab", []string{"registry.lab:5000"})
	r.Set("repository/edge", []string{"registry.lab:5000", "10.0.0.5:5000"})
	for host, want := range map[string]bool{
		"mirror.local:5000": true, "registry.lab:5000": true, "10.0.0.5:5000": true, "ghcr.io": false, "registry.lab": false,
	} {
		if got := r.Has(host); got != want {
			t.Errorf("Has(%s) = %v", host, got)
		}
	}
	if !r.HasRef("oci://registry.lab:5000/charts/app") || r.HasRef("oci://ghcr.io/kuberoot-dev/charts") {
		t.Error("HasRef must look at the registry of the reference")
	}
	// One repository letting go of a host leaves it to the one that
	// still lists it.
	r.Set("repository/lab", nil)
	if !r.Has("registry.lab:5000") {
		t.Error("registry.lab:5000 is still listed by edge")
	}
	r.Set("repository/edge", nil)
	if r.Has("registry.lab:5000") || r.Has("10.0.0.5:5000") {
		t.Error("hosts nobody lists any more must use TLS again")
	}
}

func TestCheckRegistryHost(t *testing.T) {
	for _, ok := range []string{"localhost:5000", "registry.local", "10.0.0.5:5000", "[::1]:5000"} {
		if err := CheckRegistryHost(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "http://localhost:5000", "localhost:5000/path", "user@host", "oci://ghcr.io"} {
		if CheckRegistryHost(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPlainHTTPRegistriesLeaveOthersOnTLS(t *testing.T) {
	f := &Fetcher{PlainHTTPRegistries: NewRegistries("localhost:5001")}
	if !f.plainHTTP("localhost:5001") || f.plainHTTP("ghcr.io") {
		t.Fatal("only the listed registry goes without TLS")
	}
}

func TestFetchChartFromAPlainHTTPRegistry(t *testing.T) {
	host := newMemRegistry(t)
	dir := t.TempDir()
	for name, body := range map[string]string{
		"Chart.yaml":  "apiVersion: v2\nname: app\nversion: 1.0.0\n",
		"values.yaml": "replicas: 1\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	repository := "oci://" + host + "/charts"
	res, err := PushChart(ctx, dir, repository, "app", "1.0.0", PushOptions{PlainHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	chart := Chart{Repository: repository, Name: "app", Version: "1.0.0", Digest: res.LayerDigest}
	if _, _, err := (&Fetcher{CacheDir: t.TempDir()}).FetchChart(ctx, chart); err == nil {
		t.Fatal("a registry without TLS was reached over TLS")
	}
	f := &Fetcher{CacheDir: t.TempDir(), PlainHTTPRegistries: NewRegistries(host)}
	if _, d, err := f.FetchChart(ctx, chart); err != nil || d != res.LayerDigest {
		t.Fatalf("fetch from a listed plain HTTP registry: %s %v", d, err)
	}
}

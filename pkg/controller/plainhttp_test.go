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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kuberoot-dev/kubepkg/api/v1"
	"github.com/kuberoot-dev/kubepkg/pkg/source"
)

func TestRepositoryPlainHTTPRegistries(t *testing.T) {
	e := newRepoEnv(t)
	plain := &source.Registries{}
	e.rr.Repositories.PlainHTTP = plain
	e.indexes["test://lab/index.yaml"] = indexOf(t, "1.0.0")
	e.create(&v1.Repository{ObjectMeta: metav1.ObjectMeta{Name: "lab"}, Spec: v1.RepositorySpec{
		URL: "test://lab/index.yaml", AllowUnsigned: true, PlainHTTPRegistries: []string{"registry.lab:5000"},
	}})
	e.fetch("lab")
	if !plain.Has("registry.lab:5000") || plain.Has("ghcr.io") {
		t.Fatal("the repository's registry must be reached without TLS, and only it")
	}
	// Also when the index cannot be fetched: installed packages still
	// update from the registry.
	delete(e.indexes, "test://lab/index.yaml")
	e.fetch("lab")
	if !plain.Has("registry.lab:5000") {
		t.Fatal("a failed fetch dropped the repository's plain HTTP registries")
	}
	rp := &v1.Repository{}
	if err := e.c.Get(context.Background(), types.NamespacedName{Name: "lab"}, rp); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Delete(context.Background(), rp); err != nil {
		t.Fatal(err)
	}
	e.fetch("lab")
	if plain.Has("registry.lab:5000") {
		t.Fatal("a deleted repository's registry must use TLS again")
	}
}

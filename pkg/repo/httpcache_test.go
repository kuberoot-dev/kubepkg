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

package repo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/kuberoot-dev/kubepkg/pkg/source"
)

func TestHTTPCacheAsksWhetherTheIndexChanged(t *testing.T) {
	var mu sync.Mutex
	body, etag := "index v1", `"v1"`
	downloads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		downloads++
		w.Header().Set("ETag", etag)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	f := DefaultFetchers()
	get := func() string {
		t.Helper()
		raw, err := f.Fetch(context.Background(), srv.URL+"/index.yaml")
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if got := get(); got != "index v1" {
		t.Fatalf("got %q", got)
	}
	if got := get(); got != "index v1" || downloads != 1 {
		t.Fatalf("unchanged index: got %q after %d downloads", got, downloads)
	}
	mu.Lock()
	body, etag = "index v2", `"v2"`
	mu.Unlock()
	if got := get(); got != "index v2" || downloads != 2 {
		t.Fatalf("changed index: got %q after %d downloads", got, downloads)
	}
	if _, err := f.Fetch(context.Background(), srv.URL+"/missing"); !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("missing file: %v", err)
	}
}

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
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// Registries is a set of registry hosts, host or host:port, that may change
// while it is in use: the operator keeps the plain HTTP registries of every
// Repository in one. The zero value is empty and ready to use; a nil
// *Registries is empty too.
type Registries struct {
	mu     sync.RWMutex
	owners map[string][]string
}

// NewRegistries returns a set holding hosts under the owner "".
func NewRegistries(hosts ...string) *Registries {
	r := &Registries{}
	r.Set("", hosts)
	return r
}

// Set replaces the hosts an owner contributes; no hosts removes the owner.
func (r *Registries) Set(owner string, hosts []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(hosts) == 0 {
		delete(r.owners, owner)
		return
	}
	if r.owners == nil {
		r.owners = map[string][]string{}
	}
	r.owners[owner] = slices.Clone(hosts)
}

// Has reports whether any owner lists host.
func (r *Registries) Has(host string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, hosts := range r.owners {
		if slices.Contains(hosts, host) {
			return true
		}
	}
	return false
}

// HasRef reports whether the registry of an oci:// reference or a
// registry path is in the set.
func (r *Registries) HasRef(ref string) bool {
	host, _, _ := strings.Cut(strings.TrimPrefix(ref, "oci://"), "/")
	return r.Has(host)
}

// CheckRegistryHost refuses what is not a registry host, host or
// host:port, such as a URL or a path.
func CheckRegistryHost(host string) error {
	u, err := url.Parse("oci://" + host)
	if err != nil || host == "" || u.Host != host || u.User != nil {
		return fmt.Errorf("%q is not a registry host (host or host:port)", host)
	}
	return nil
}

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

package resolve

import (
	"encoding/json"
	"fmt"

	"github.com/kuberoot-dev/kubepkg/api/v1"
)

// RequirementsOf turns a variant's dependsOn and requires into
// requirements, minus the packages and capabilities listed in ignore.
func RequirementsOf(v *v1.Variant, ignore []string) []Requirement {
	ignored := map[string]bool{}
	for _, d := range ignore {
		ignored[d] = true
	}
	var out []Requirement
	seen := map[string]bool{}
	for _, d := range v.DependsOn {
		if !ignored[d] && !seen[d] {
			seen[d] = true
			out = append(out, Requirement{Package: d})
		}
	}
	for _, q := range v.Requires {
		if q.Package != "" && ignored[q.Package] {
			continue
		}
		if q.Capability != "" && ignored[q.Capability] {
			continue
		}
		r := Requirement{Package: q.Package, Capability: q.Capability, Version: q.Version, Optional: q.Optional, Variant: q.Variant}
		r.Values, r.Invalid = requiredValues(q)
		out = append(out, r)
	}
	return out
}

// requiredValues reads the values a requirement passes, by component.
func requiredValues(q v1.Requirement) (map[string]map[string]any, string) {
	var out map[string]map[string]any
	for _, name := range sortedKeys(q.Components) {
		c := q.Components[name]
		if c.Values == nil || len(c.Values.Raw) == 0 {
			continue
		}
		var vals map[string]any
		if err := json.Unmarshal(c.Values.Raw, &vals); err != nil {
			return nil, fmt.Sprintf("values for component %s are not an object: %v", name, err)
		}
		if len(vals) == 0 {
			continue
		}
		if out == nil {
			out = map[string]map[string]any{}
		}
		out[name] = vals
	}
	return out, ""
}

// CheckRequirement refuses what a requirement cannot carry: a variant or
// values without a package, or values that are not an object.
func CheckRequirement(q v1.Requirement) error {
	if q.Package == "" && (q.Variant != "" || len(q.Components) > 0) {
		return fmt.Errorf("requirement %s: variant and components apply to a required package only", q.Capability)
	}
	if _, invalid := requiredValues(q); invalid != "" {
		return fmt.Errorf("requirement %s: %s", q.Package, invalid)
	}
	return nil
}

// ReleaseOf describes one variant of a package version for resolving,
// with the requirements of its other variants for packages that require
// it in one of them. A missing variant leaves the release without
// requirements.
func ReleaseOf(name string, spec *v1.PackageSourceSpec, variant string) Release {
	r := Release{Name: name, Version: spec.Version, Provides: spec.Provides, Conflicts: spec.Conflicts}
	if r.Version == "" {
		r.Version = Unversioned
	}
	if variant == "" {
		variant = "default"
	}
	for i := range spec.Variants {
		reqs := RequirementsOf(&spec.Variants[i], nil)
		if r.Variants == nil {
			r.Variants = map[string][]Requirement{}
		}
		r.Variants[spec.Variants[i].Name] = reqs
		if spec.Variants[i].Name == variant {
			r.Requires = reqs
			if variant != "default" {
				r.Variant = variant
			}
		}
	}
	return r
}

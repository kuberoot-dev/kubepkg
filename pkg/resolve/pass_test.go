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
	"errors"
	"reflect"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/kuberoot-dev/kubepkg/api/v1"
)

// passes makes a release require pkg with values for one component and,
// when variant is set, in that variant.
func passes(pkg, variant, comp string, values map[string]any) func(*Release) {
	return func(r *Release) {
		q := Requirement{Package: pkg, Variant: variant}
		if values != nil {
			q.Values = map[string]map[string]any{comp: values}
		}
		r.Requires = append(r.Requires, q)
	}
}

func inVariants(variants map[string][]Requirement) func(*Release) {
	return func(r *Release) { r.Variants = variants }
}

func TestMergePassesMergesAndReportsConflicts(t *testing.T) {
	obs := rel("observability", "1.0.0", passes("vm", "", "vm", map[string]any{"grafana": map[string]any{"enabled": false}}))
	edge := rel("edge", "1.0.0", passes("vm", "", "vm", map[string]any{"grafana": map[string]any{"enabled": false}, "retention": "7d"}))
	got, err := MergePasses("vm", PassesTo("vm", []Release{obs, edge}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]any{"vm": {"grafana": map[string]any{"enabled": false}, "retention": "7d"}}
	if !reflect.DeepEqual(got.Values, want) || strings.Join(got.From, ",") != "edge,observability" {
		t.Fatalf("merged %v from %v", got.Values, got.From)
	}

	// The same key set differently is not resolved silently.
	full := rel("full", "1.0.0", passes("vm", "", "vm", map[string]any{"grafana": map[string]any{"enabled": true}}))
	_, err = MergePasses("vm", PassesTo("vm", []Release{obs, full}))
	if !errors.Is(err, ErrConflictingRequirements) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"full", "observability", "grafana.enabled", "component vm", "true", "false"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("conflict message lacks %q: %s", want, err)
		}
	}

	// A map against a scalar is a conflict too.
	flat := rel("flat", "1.0.0", passes("vm", "", "vm", map[string]any{"grafana": false}))
	if _, err := MergePasses("vm", PassesTo("vm", []Release{obs, flat})); !errors.Is(err, ErrConflictingRequirements) {
		t.Fatalf("map against scalar: %v", err)
	}

	// Variants: the same one twice is fine, two different ones are not.
	a := rel("a", "1.0.0", passes("vm", "lite", "", nil))
	b := rel("b", "1.0.0", passes("vm", "lite", "", nil))
	c := rel("c", "1.0.0", passes("vm", "full", "", nil))
	if got, err := MergePasses("vm", PassesTo("vm", []Release{a, b})); err != nil || got.Variant != "lite" {
		t.Fatalf("same variant: %v %v", got, err)
	}
	if _, err := MergePasses("vm", PassesTo("vm", []Release{a, c})); err == nil || !strings.Contains(err.Error(), "variant full") {
		t.Fatalf("different variants: %v", err)
	}
}

func TestRequirementValuesFromTheAPI(t *testing.T) {
	v := &v1.Variant{Name: "default", Requires: []v1.Requirement{{
		Package: "vm", Variant: "lite",
		Components: map[string]v1.RequiredComponent{"vm": {Values: &apiextensionsv1.JSON{Raw: []byte(`{"grafana":{"enabled":false}}`)}}},
	}}}
	reqs := RequirementsOf(v, nil)
	if len(reqs) != 1 || reqs[0].Variant != "lite" || reqs[0].Values["vm"]["grafana"].(map[string]any)["enabled"] != false {
		t.Fatalf("requirements = %+v", reqs)
	}
	bad := v1.Requirement{Package: "vm", Components: map[string]v1.RequiredComponent{"vm": {Values: &apiextensionsv1.JSON{Raw: []byte(`[1]`)}}}}
	if err := CheckRequirement(bad); err == nil {
		t.Fatal("values that are not an object were accepted")
	}
	if err := CheckRequirement(v1.Requirement{Capability: "ingress", Variant: "lite"}); err == nil {
		t.Fatal("a variant on a capability was accepted")
	}
	if got := RequirementsOf(&v1.Variant{Requires: []v1.Requirement{bad}}, nil); got[0].Invalid == "" {
		t.Fatal("unreadable values must be reported, not dropped")
	}
}

func TestResolveInTheVariantARequirerNeeds(t *testing.T) {
	vmVariants := inVariants(map[string][]Requirement{
		"default": {{Package: "grafana"}},
		"lite":    nil,
	})
	cat := Catalog{
		"vm":            {rel("vm", "1.0.0", needsPkg("grafana", ""), vmVariants)},
		"grafana":       {rel("grafana", "11.0.0")},
		"observability": {rel("observability", "1.0.0", passes("vm", "lite", "vm", map[string]any{"grafana": map[string]any{"enabled": false}}))},
	}
	p, err := Resolve(cat, installed(), []Request{{Name: "observability"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(p); got != "observability:install:1.0.0 vm:install:1.0.0" {
		t.Fatalf("plan = %s (lite needs no grafana)", got)
	}
	for _, c := range p.Changes {
		if c.Name == "vm" && (c.Variant != "lite" || c.Passed.Values["vm"] == nil || c.Passed.From[0] != "observability") {
			t.Fatalf("vm change = %+v", c)
		}
	}

	// Asked for vm first, without a variant, and for the package that
	// needs lite after it: the plan still installs vm as lite.
	p, err = Resolve(cat, installed(), []Request{{Name: "vm"}, {Name: "observability"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(p); got != "observability:install:1.0.0 vm:install:1.0.0" {
		t.Fatalf("plan = %s", got)
	}

	// Two packages needing different variants cannot both be installed.
	cat["full"] = []Release{rel("full", "1.0.0", passes("vm", "default", "", nil))}
	if _, err := Resolve(cat, installed(), []Request{{Name: "observability"}, {Name: "full"}}); !errors.Is(err, ErrUnresolvable) || !strings.Contains(err.Error(), "variant") {
		t.Fatalf("conflicting variants: %v", err)
	}

	// A variant the cluster's Package names is the user's choice.
	st := installed(rel("vm", "1.0.0"))
	inst := st.Packages["vm"]
	inst.VariantSet = true
	st.Packages["vm"] = inst
	if _, err := Resolve(cat, st, []Request{{Name: "observability"}}); err == nil || !strings.Contains(err.Error(), "installed as variant default") {
		t.Fatalf("installed variant: %v", err)
	}

	// Values two packages pass differently fail the plan.
	cat["other"] = []Release{rel("other", "1.0.0", passes("vm", "lite", "vm", map[string]any{"grafana": map[string]any{"enabled": true}}))}
	if _, err := Resolve(cat, installed(), []Request{{Name: "observability"}, {Name: "other"}}); !errors.Is(err, ErrConflictingRequirements) {
		t.Fatalf("conflicting values: %v", err)
	}
}

func TestCheckWantsTheVariant(t *testing.T) {
	st := installed(rel("vm", "1.0.0"))
	unmet, err := Check([]Requirement{{Package: "vm", Variant: "lite"}}, st)
	if err != nil || len(unmet) != 1 || !strings.Contains(unmet[0].String(), "runs variant default, variant lite is needed") {
		t.Fatalf("unmet = %v %v", unmet, err)
	}
	if unmet, _ := Check([]Requirement{{Package: "vm", Variant: "default"}}, st); len(unmet) != 0 {
		t.Fatalf("default variant: %v", unmet)
	}
}

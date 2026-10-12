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
	"reflect"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kuberoot-dev/kubepkg/api/v1"
)

// metaPassing is a meta package that requires vm and passes values to its
// component vm.
func metaPassing(name, values string) *v1.PackageSource {
	s := mkSource(name, "1.0.0", true)
	s.Spec.Variants[0].Requires = []v1.Requirement{{
		Package:    "vm",
		Components: map[string]v1.RequiredComponent{"vm": {Values: &apiextensionsv1.JSON{Raw: []byte(values)}}},
	}}
	return s
}

func TestAMetaPackagePassesValuesToWhatItRequires(t *testing.T) {
	e := newEnv(t)
	e.create(mkSource("vm", "1.0.0", true, "vm"), &v1.Package{ObjectMeta: metav1.ObjectMeta{Name: "vm"}, Spec: v1.PackageSpec{
		Components: map[string]v1.PackageComponent{"vm": {Values: &apiextensionsv1.JSON{Raw: []byte(`{"replicas":3,"grafana":{"enabled":true,"admin":"me"}}`)}}},
	}})
	e.reconcile("vm")
	before := e.revs("vm")

	e.create(metaPassing("observability", `{"grafana":{"enabled":false,"persistence":false},"retention":"7d"}`),
		&v1.Package{ObjectMeta: metav1.ObjectMeta{Name: "observability"}})
	e.reconcile("vm")
	// The Package's own values win over what the meta package passes;
	// the rest of what it passes goes in.
	want := map[string]any{"replicas": float64(3), "retention": "7d", "grafana": map[string]any{"enabled": true, "admin": "me", "persistence": false}}
	if got := e.be.values["ns-vm/vm"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("values = %v, want %v", got, want)
	}
	if e.revs("vm") == before {
		t.Fatal("passed values did not make a new revision")
	}
	e.reconcile("observability")
	if ok, reason, msg := ready(e.pkg("observability")); !ok {
		t.Fatalf("meta package not ready: %s %s", reason, msg)
	}

	// A second meta package that sets the same key differently is a
	// conflict on vm, and nothing is applied.
	e.create(metaPassing("full", `{"retention":"30d"}`), &v1.Package{ObjectMeta: metav1.ObjectMeta{Name: "full"}})
	calls := len(e.be.calls)
	e.reconcile("vm")
	_, reason, msg := ready(e.pkg("vm"))
	if reason != v1.ReasonConflictingRequirements || !strings.Contains(msg, "retention") || !strings.Contains(msg, "full") || !strings.Contains(msg, "observability") {
		t.Fatalf("condition = %s %s", reason, msg)
	}
	if len(e.be.calls) != calls {
		t.Fatalf("applied despite the conflict: %v", e.be.calls[calls:])
	}
	if e.be.values["ns-vm/vm"]["retention"] != "7d" {
		t.Fatal("a conflict changed what runs")
	}

	// Once one of them goes, the other's values apply again.
	if err := e.c.Delete(context.Background(), &v1.PackageSource{ObjectMeta: metav1.ObjectMeta{Name: "observability"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Delete(context.Background(), &v1.Package{ObjectMeta: metav1.ObjectMeta{Name: "observability"}}); err != nil {
		t.Fatal(err)
	}
	e.reconcile("observability") // finalizer
	e.reconcile("vm")
	if ok, reason, msg := ready(e.pkg("vm")); !ok {
		t.Fatalf("vm not ready after the conflict went: %s %s", reason, msg)
	}
	if got := e.be.values["ns-vm/vm"]["retention"]; got != "30d" {
		t.Fatalf("retention = %v", got)
	}
	if _, ok := e.be.values["ns-vm/vm"]["grafana"].(map[string]any)["persistence"]; ok {
		t.Fatal("values of a removed meta package stayed")
	}
}

func TestARequiredVariantMustRun(t *testing.T) {
	e := newEnv(t)
	vm := mkSource("vm", "1.0.0", true, "vm")
	vm.Spec.Variants = append(vm.Spec.Variants, v1.Variant{Name: "lite", Components: vm.Spec.Variants[0].Components})
	e.create(vm, &v1.Package{ObjectMeta: metav1.ObjectMeta{Name: "vm"}})
	e.reconcile("vm")
	meta := mkSource("observability", "1.0.0", true)
	meta.Spec.Variants[0].Requires = []v1.Requirement{{Package: "vm", Variant: "lite"}}
	e.create(meta, &v1.Package{ObjectMeta: metav1.ObjectMeta{Name: "observability"}})
	e.reconcile("observability")
	if _, reason, msg := ready(e.pkg("observability")); reason != v1.ReasonRequirementsNotMet || !strings.Contains(msg, "variant lite") {
		t.Fatalf("condition = %s %s", reason, msg)
	}
	p := e.pkg("vm")
	p.Spec.Variant = "lite"
	if err := e.c.Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	e.reconcile("vm")
	e.reconcile("observability")
	if ok, reason, msg := ready(e.pkg("observability")); !ok {
		t.Fatalf("not ready with vm as lite: %s %s", reason, msg)
	}
}

func TestNothingPassedKeepsTheDigest(t *testing.T) {
	e := newEnv(t)
	src := mkSource("app", "1.0.0", true, "app")
	pkg := &v1.Package{ObjectMeta: metav1.ObjectMeta{Name: "app"}}
	e.create(src, pkg)
	d, err := e.r.buildDesired(context.Background(), pkg, src, &src.Spec.Variants[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The digest as 1.2 computed it: revisions made before values could be
	// passed must not look out of date after an upgrade.
	old, err := digestOf(struct {
		Package v1.PackageSpec
		Retry   string
		Source  v1.PackageSourceSpec
	}{pkg.Spec, "", src.Spec})
	if err != nil {
		t.Fatal(err)
	}
	if d.digest != old {
		t.Fatal("the desired digest changed for a package nothing passes values to")
	}
}

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
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// ErrConflictingRequirements is returned when packages that require the
// same package ask for different variants of it or pass different values
// for the same key.
var ErrConflictingRequirements = errors.New("packages that require it disagree")

// Pass is what one package passes to a package it requires: a variant,
// values by component, or both.
type Pass struct {
	From    string
	Variant string
	Values  map[string]map[string]any
	// Invalid says why the requirement's values cannot be read.
	Invalid string
}

// PassesTo collects what the given releases pass to the package name,
// ordered by the name of the release passing it.
func PassesTo(name string, requirers []Release) []Pass {
	var out []Pass
	for _, r := range requirers {
		if r.Name == name {
			continue
		}
		for _, q := range r.Requires {
			if q.Package != name || (q.Variant == "" && len(q.Values) == 0 && q.Invalid == "") {
				continue
			}
			out = append(out, Pass{From: r.Name, Variant: q.Variant, Values: q.Values, Invalid: q.Invalid})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}

// Passed is the merged outcome of what several packages pass to one.
type Passed struct {
	// Variant is the variant they ask for; empty when none asks.
	Variant string
	// Values are the values for each component, merged.
	Values map[string]map[string]any
	// From names the packages that pass something, sorted.
	From []string
}

// MergePasses merges what several packages pass to the package name.
// Values merge key by key; two packages setting the same key to
// different values, or asking for different variants, is an error
// wrapping ErrConflictingRequirements that names them. Nothing is
// resolved silently, since either choice would break the other package.
func MergePasses(name string, passes []Pass) (Passed, error) {
	var out Passed
	var problems []string
	variantFrom := ""
	owners := map[string]string{}
	for _, p := range passes {
		if len(out.From) == 0 || out.From[len(out.From)-1] != p.From {
			out.From = append(out.From, p.From)
		}
		if p.Invalid != "" {
			problems = append(problems, fmt.Sprintf("%s passes values that cannot be read: %s", p.From, p.Invalid))
			continue
		}
		if p.Variant != "" {
			switch {
			case out.Variant == "":
				out.Variant, variantFrom = p.Variant, p.From
			case out.Variant != p.Variant:
				problems = append(problems, fmt.Sprintf("%s needs variant %s, %s needs variant %s", variantFrom, out.Variant, p.From, p.Variant))
			}
		}
		for _, comp := range sortedKeys(p.Values) {
			if out.Values == nil {
				out.Values = map[string]map[string]any{}
			}
			if out.Values[comp] == nil {
				out.Values[comp] = map[string]any{}
			}
			mergeOwned(out.Values[comp], p.Values[comp], comp, p.From, owners, &problems)
		}
	}
	if len(problems) > 0 {
		return out, fmt.Errorf("%w on %s: %s", ErrConflictingRequirements, name, strings.Join(problems, "; "))
	}
	return out, nil
}

// mergeOwned merges src into dst, remembering who set each key, and
// records a problem for every key two packages set differently.
func mergeOwned(dst, src map[string]any, path, from string, owners map[string]string, problems *[]string) {
	for _, k := range sortedKeys(src) {
		v := src[k]
		p := path + "." + k
		have, ok := dst[k]
		if !ok {
			dst[k] = copyValue(v)
			owners[p] = from
			continue
		}
		hm, hok := have.(map[string]any)
		vm, vok := v.(map[string]any)
		if hok && vok {
			mergeOwned(hm, vm, p, from, owners, problems)
			continue
		}
		if !reflect.DeepEqual(have, v) {
			comp, key, _ := strings.Cut(p, ".")
			*problems = append(*problems, fmt.Sprintf("%s sets %s to %s in component %s, %s sets it to %s",
				ownerOf(owners, p), key, compact(have), comp, from, compact(v)))
		}
	}
}

// ownerOf finds who set a key, or the map it is in.
func ownerOf(owners map[string]string, path string) string {
	for p := path; ; {
		if o, ok := owners[p]; ok {
			return o
		}
		i := strings.LastIndex(p, ".")
		if i < 0 {
			return "another package"
		}
		p = p[:i]
	}
}

func compact(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}

func copyValue(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := make(map[string]any, len(m))
	for k, x := range m {
		out[k] = copyValue(x)
	}
	return out
}

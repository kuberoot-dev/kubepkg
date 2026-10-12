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
	"fmt"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// Catalog holds every published release, by package name.
type Catalog map[string][]Release

// Request asks for a package, optionally constrained to a version range.
type Request struct {
	Name    string
	Version string
}

// Action is what a plan does to one package.
type Action string

const (
	ActionInstall   Action = "install"
	ActionUpgrade   Action = "upgrade"
	ActionDowngrade Action = "downgrade"
	ActionKeep      Action = "keep"
)

// Change is one line of a plan.
type Change struct {
	Name   string
	From   string
	To     string
	Action Action
	// Reason says why the package is in the plan: "requested" or the
	// requirement that pulled it in.
	Reason string
	// Variant is the variant the package is resolved in.
	Variant string
	// Passed is what the packages requiring this one pass to it.
	Passed Passed
}

// Plan is the outcome of resolving a set of requests.
type Plan struct {
	Changes []Change
}

// ErrUnresolvable is returned when no combination satisfies the requests.
// The wrapped message explains the closest attempt.
var ErrUnresolvable = errors.New("requests cannot be satisfied")

// maxSteps bounds the search. Real package sets resolve in a handful of
// steps; hitting the bound means the constraints are pathological, and
// failing with an explanation beats spinning.
const maxSteps = 10000

type goal struct {
	pkg        string // package goal
	constraint string
	variant    string
	capability string // capability goal
	origin     string
}

type search struct {
	cat   Catalog
	st    State
	steps int
	// deepest failure seen, reported when everything fails
	why string
	// pins are variants asked for by a package found late in a search
	// that had already chosen the package in another variant; the next
	// search starts with them.
	pins  map[string]string
	repin *goal
}

// maxPins bounds the restarts for variants asked for late.
const maxPins = 32

// Resolve picks a release for every requested package and everything they
// require. Already installed versions are kept when they still satisfy the
// constraints, so asking for one package does not silently upgrade its
// dependencies; requested packages get the highest acceptable version.
func Resolve(cat Catalog, st State, reqs []Request) (Plan, error) {
	var goals []goal
	requested := map[string]bool{}
	for _, r := range reqs {
		goals = append(goals, goal{pkg: r.Name, constraint: r.Version, origin: "requested"})
		requested[r.Name] = true
	}
	pins := map[string]string{}
	for {
		s := &search{cat: cat, st: st, pins: pins}
		chosen, origins, ok := s.solve(goals, map[string]Release{}, map[string][]goal{}, requested)
		if ok {
			return s.plan(chosen, origins)
		}
		// A package asked for a variant of one already chosen in
		// another: start again with that variant, once per package.
		if g := s.repin; g != nil && pins[g.pkg] == "" && len(pins) < maxPins {
			pins[g.pkg] = g.variant
			continue
		}
		if s.why == "" {
			s.why = "no candidates"
		}
		return Plan{}, fmt.Errorf("%w: %s", ErrUnresolvable, s.why)
	}
}

func (s *search) fail(msg string) bool {
	if len(msg) > len(s.why) || s.why == "" {
		s.why = msg
	}
	return false
}

func (s *search) solve(goals []goal, chosen map[string]Release, cons map[string][]goal, requested map[string]bool) (map[string]Release, map[string][]goal, bool) {
	s.steps++
	if s.steps > maxSteps {
		s.fail("search limit reached; constraints are too tangled to resolve")
		return nil, nil, false
	}
	if len(goals) == 0 {
		return chosen, cons, true
	}
	g, rest := goals[0], goals[1:]

	if g.capability != "" {
		return s.solveCapability(g, rest, chosen, cons, requested)
	}

	cons = withGoal(cons, g)
	if r, ok := chosen[g.pkg]; ok {
		// Already decided; the new constraint must agree with it.
		ok, err := Satisfies(r.Version, g.constraint)
		if err != nil {
			s.fail(err.Error())
			return nil, nil, false
		}
		if !ok {
			s.fail(fmt.Sprintf("%s %s is selected, but %s needs %s", g.pkg, r.Version, g.origin, g.constraint))
			return nil, nil, false
		}
		if g.variant != "" && g.variant != r.VariantName() {
			if s.pins[g.pkg] == "" && !s.variantFixed(g.pkg) {
				s.repin = &g
			}
			s.fail(fmt.Sprintf("%s is selected in variant %s, but %s needs variant %s", g.pkg, r.VariantName(), g.origin, g.variant))
			return nil, nil, false
		}
		return s.solve(rest, chosen, cons, requested)
	}

	variant, err := s.variantFor(g)
	if err != nil {
		s.fail(err.Error())
		return nil, nil, false
	}
	cands, err := s.candidates(g.pkg, cons[g.pkg], requested[g.pkg])
	if err != nil {
		s.fail(err.Error())
		return nil, nil, false
	}
	if len(cands) == 0 {
		s.fail(fmt.Sprintf("no release of %s satisfies %s", g.pkg, describe(cons[g.pkg])))
		return nil, nil, false
	}
	for _, r := range cands {
		r, ok := r.inVariant(variant)
		if !ok {
			s.fail(fmt.Sprintf("%s %s has no variant %s (needed by %s)", r.Name, r.Version, variant, g.origin))
			continue
		}
		if c := s.conflictsWith(r, chosen); c != "" {
			s.fail(fmt.Sprintf("%s %s conflicts with %s", r.Name, r.Version, c))
			continue
		}
		next := copyChosen(chosen)
		next[r.Name] = r
		more := append(requirementGoals(r), rest...)
		if out, oc, ok := s.solve(more, next, cons, requested); ok {
			return out, oc, true
		}
	}
	return nil, nil, false
}

// variantFixed reports whether the cluster's Package names a variant.
func (s *search) variantFixed(name string) bool {
	inst, ok := s.st.Packages[name]
	return ok && inst.VariantSet
}

// variantFor is the variant a package goal is resolved in: the one asked
// for, a pinned one, the one the installed Package names, or the
// catalog's.
func (s *search) variantFor(g goal) (string, error) {
	inst, installed := s.st.Packages[g.pkg]
	fixed := installed && inst.VariantSet
	switch {
	case g.variant != "" && fixed && g.variant != inst.VariantName():
		return "", fmt.Errorf("%s is installed as variant %s, but %s needs variant %s", g.pkg, inst.VariantName(), g.origin, g.variant)
	case g.variant != "":
		if p := s.pins[g.pkg]; p != "" && p != g.variant {
			return "", fmt.Errorf("%s needs variant %s of %s, another package needs variant %s", g.origin, g.variant, g.pkg, p)
		}
		return g.variant, nil
	case s.pins[g.pkg] != "":
		return s.pins[g.pkg], nil
	case fixed:
		return inst.VariantName(), nil
	}
	return "", nil
}

func (s *search) solveCapability(g goal, rest []goal, chosen map[string]Release, cons map[string][]goal, requested map[string]bool) (map[string]Release, map[string][]goal, bool) {
	if strings.HasPrefix(g.capability, APIPrefix) && s.st.APIs[strings.TrimPrefix(g.capability, APIPrefix)] {
		return s.solve(rest, chosen, cons, requested)
	}
	for _, name := range sortedKeys(chosen) {
		if provides(chosen[name], g.capability) {
			return s.solve(rest, chosen, cons, requested)
		}
	}
	for _, name := range sortedKeys(s.st.Packages) {
		if _, replaced := chosen[name]; replaced {
			continue // the chosen release decides, checked above
		}
		if provides(s.st.Packages[name].Release, g.capability) {
			return s.solve(rest, chosen, cons, requested)
		}
	}
	// Pull in a provider. Each provider package is tried in name order; the
	// version is then chosen like any other package goal.
	providers := s.providers(g.capability)
	if len(providers) == 0 {
		s.fail(fmt.Sprintf("nothing provides %s (needed by %s)", g.capability, g.origin))
		return nil, nil, false
	}
	for _, p := range providers {
		pg := goal{pkg: p, origin: g.origin + " via " + g.capability}
		if out, oc, ok := s.solve(append([]goal{pg}, rest...), chosen, cons, requested); ok {
			return out, oc, true
		}
	}
	return nil, nil, false
}

// candidates lists releases of a package that satisfy all constraints, in
// the order they should be tried: the installed version first unless the
// package was requested explicitly, then highest to lowest.
func (s *search) candidates(name string, goals []goal, isRequested bool) ([]Release, error) {
	var out []Release
	for _, r := range s.cat[name] {
		ok := true
		for _, g := range goals {
			sat, err := Satisfies(r.Version, g.constraint)
			if err != nil {
				return nil, err
			}
			if !sat {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return newer(out[i].Version, out[j].Version) })
	if inst, ok := s.st.Packages[name]; ok && !isRequested {
		for i, r := range out {
			if r.Version == inst.Version {
				out = append([]Release{r}, append(out[:i:i], out[i+1:]...)...)
				break
			}
		}
	}
	return out, nil
}

func (s *search) providers(capability string) []string {
	var out []string
	for _, name := range sortedKeys(s.cat) {
		for _, r := range s.cat[name] {
			if provides(r, capability) {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

// conflictsWith checks a release against everything chosen so far and
// everything installed that the plan does not replace.
func (s *search) conflictsWith(r Release, chosen map[string]Release) string {
	for _, name := range sortedKeys(chosen) {
		if name != r.Name && (clashes(r, chosen[name]) || clashes(chosen[name], r)) {
			return name
		}
	}
	for _, name := range sortedKeys(s.st.Packages) {
		if _, replaced := chosen[name]; replaced || name == r.Name {
			continue
		}
		other := s.st.Packages[name].Release
		if clashes(r, other) || clashes(other, r) {
			return name + " (installed)"
		}
	}
	return ""
}

func (s *search) plan(chosen map[string]Release, cons map[string][]goal) (Plan, error) {
	// What every package that will be there passes to the ones it
	// requires: the chosen releases and the installed packages that stay.
	requirers := make([]Release, 0, len(chosen)+len(s.st.Packages))
	for _, name := range sortedKeys(chosen) {
		requirers = append(requirers, chosen[name])
	}
	for _, name := range sortedKeys(s.st.Packages) {
		if _, replaced := chosen[name]; !replaced && !s.st.Packages[name].Leaving {
			requirers = append(requirers, s.st.Packages[name].Release)
		}
	}
	var p Plan
	var conflicts []error
	for _, name := range sortedKeys(chosen) {
		r := chosen[name]
		c := Change{Name: name, To: r.Version, Reason: reason(cons[name]), Variant: r.VariantName()}
		passed, err := MergePasses(name, PassesTo(name, requirers))
		if err != nil {
			conflicts = append(conflicts, err)
		}
		c.Passed = passed
		inst, ok := s.st.Packages[name]
		switch {
		case !ok:
			c.Action = ActionInstall
		case inst.Version == r.Version:
			c.From, c.Action = inst.Version, ActionKeep
		case newer(r.Version, inst.Version):
			c.From, c.Action = inst.Version, ActionUpgrade
		default:
			c.From, c.Action = inst.Version, ActionDowngrade
		}
		p.Changes = append(p.Changes, c)
	}
	if len(conflicts) > 0 {
		return Plan{}, errors.Join(conflicts...)
	}
	return p, nil
}

func requirementGoals(r Release) []goal {
	var out []goal
	for _, q := range r.Requires {
		if q.Optional {
			continue // optional requirements order installs, they do not pull packages in
		}
		origin := r.Name + " " + r.Version
		if q.Package != "" {
			out = append(out, goal{pkg: q.Package, constraint: q.Version, variant: q.Variant, origin: origin})
		} else {
			out = append(out, goal{capability: q.Capability, origin: origin})
		}
	}
	return out
}

func withGoal(cons map[string][]goal, g goal) map[string][]goal {
	out := make(map[string][]goal, len(cons)+1)
	for k, v := range cons {
		out[k] = v
	}
	out[g.pkg] = append(append([]goal(nil), cons[g.pkg]...), g)
	return out
}

func copyChosen(m map[string]Release) map[string]Release {
	out := make(map[string]Release, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func describe(goals []goal) string {
	var parts []string
	for _, g := range goals {
		c := g.constraint
		if c == "" {
			c = "any version"
		}
		parts = append(parts, fmt.Sprintf("%s (from %s)", c, g.origin))
	}
	return strings.Join(parts, " and ")
}

func reason(goals []goal) string {
	for _, g := range goals {
		if g.origin == "requested" {
			return "requested"
		}
	}
	if len(goals) > 0 {
		return "required by " + goals[0].origin
	}
	return ""
}

// newer orders versions highest first; unparsable versions sort last.
func newer(a, b string) bool {
	va, ea := semver.NewVersion(a)
	vb, eb := semver.NewVersion(b)
	switch {
	case ea != nil && eb != nil:
		return a > b
	case ea != nil:
		return false
	case eb != nil:
		return true
	}
	return va.GreaterThan(vb)
}

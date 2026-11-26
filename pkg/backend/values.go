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

package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"github.com/kuberoot-dev/kubepkg/pkg/source"
)

// ResolveValues layers the component's values over the values.yaml of each
// ValuesFromSecrets Secret, named namespace/name or, like Flux valuesFrom,
// by name alone in the release namespace. A missing Secret is an error:
// installing without platform settings would produce a release that looks
// healthy and is configured wrong. Backends whose installer cannot read
// Secrets itself pass the result on as plain values.
func ResolveValues(ctx context.Context, secrets kubernetes.Interface, c Component) (map[string]any, error) {
	out := map[string]any{}
	for _, ref := range c.ValuesFromSecrets {
		ns, name, ok := strings.Cut(ref, "/")
		if !ok {
			ns, name = c.Namespace, ref
		}
		sec, err := secrets.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("values secret %s/%s: %w", ns, name, err)
		}
		var v map[string]any
		if err := yaml.Unmarshal(sec.Data["values.yaml"], &v); err != nil {
			return nil, fmt.Errorf("values secret %s/%s: %w", ns, name, err)
		}
		out = source.MergeValues(out, v)
	}
	out, err := AcceptedBySchema(out, c.ChartDir)
	if err != nil {
		return nil, err
	}
	return source.MergeValues(out, c.Values), nil
}

// AcceptedBySchema leaves out of platform values the top-level keys a
// chart's values.schema.json does not accept. Platform values go to every
// component, and a chart whose schema forbids additional properties would
// refuse them all for one key meant for other charts. The package's own
// values are not filtered: a wrong key there is a mistake worth seeing.
func AcceptedBySchema(platform map[string]any, chartDir string) (map[string]any, error) {
	if chartDir == "" || len(platform) == 0 {
		return platform, nil
	}
	raw, err := os.ReadFile(filepath.Join(chartDir, "values.schema.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return platform, nil
	}
	if err != nil {
		return nil, err
	}
	var schema struct {
		Ref                  string                     `json:"$ref"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
		PatternProperties    map[string]json.RawMessage `json:"patternProperties"`
	}
	// Some charts (including cert-manager) put the root schema in $defs
	// and expose it through a document-local reference. Follow references
	// against the original document, not the previously resolved fragment.
	document := raw
	seen := map[string]bool{}
	for {
		if err := json.Unmarshal(raw, &schema); err != nil {
			// Helm will report a broken schema; nothing to filter by.
			return platform, nil
		}
		if schema.Ref == "" {
			break
		}
		ref := schema.Ref
		if seen[ref] {
			return platform, nil
		}
		seen[ref] = true
		var ok bool
		raw, ok = localSchemaReference(document, ref)
		if !ok {
			// Remote or invalid references remain Helm's responsibility.
			return platform, nil
		}
		// Unmarshal does not clear fields absent from the next fragment.
		schema.Ref = ""
		schema.AdditionalProperties = nil
		schema.Properties = nil
		schema.PatternProperties = nil
	}
	if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		return platform, nil
	}
	var patterns []*regexp.Regexp
	for p := range schema.PatternProperties {
		if re, err := regexp.Compile(p); err == nil {
			patterns = append(patterns, re)
		}
	}
	out := map[string]any{}
	for k, v := range platform {
		_, ok := schema.Properties[k]
		for _, re := range patterns {
			ok = ok || re.MatchString(k)
		}
		if ok {
			out[k] = v
		}
	}
	return out, nil
}

// localSchemaReference resolves an object JSON Pointer in this document.
// It never fetches remote schemas or reads another file.
func localSchemaReference(document json.RawMessage, ref string) (json.RawMessage, bool) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, false
	}
	pointer, err := url.PathUnescape(strings.TrimPrefix(ref, "#"))
	if err != nil {
		return nil, false
	}
	raw := document
	for _, part := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, false
		}
		key := strings.NewReplacer("~1", "/", "~0", "~").Replace(part)
		var ok bool
		raw, ok = object[key]
		if !ok {
			return nil, false
		}
	}
	return raw, true
}

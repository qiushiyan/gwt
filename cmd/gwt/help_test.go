package main

import (
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/qiushiyan/clikit/clikittest"
	"github.com/qiushiyan/gwt/internal/config"
	"github.com/qiushiyan/gwt/internal/worktree"
)

// Every help page, pinned. make golden rewrites them after an intended change;
// the diff is reviewed like code.
func TestHelpPages(t *testing.T) {
	clikittest.Golden(t, app, "testdata/help")
}

// Each page's JSON section names every key its command's --json can print,
// optional ones included, so the page an agent reads the shape from cannot
// fall behind it. A nested trunk is described once, on trunk's page, and the
// others point there.
func TestTheJSONIsDescribed(t *testing.T) {
	trunk := reflect.TypeFor[worktree.Trunk]()
	for command, shape := range map[string]any{
		"create":      worktree.Result{},
		"resolve":     worktree.Resolution{},
		"path":        pathJSON{},
		"list":        worktree.Listing{},
		"merged":      worktree.Verdicts{},
		"trunk":       worktree.Trunk{},
		"remove":      worktree.Removal{},
		"config show": config.Config{},
	} {
		r := clikittest.Run(app, append(strings.Fields(command), "--help")...)
		_, section, ok := strings.Cut(r.Stdout, "\nJSON:\n")
		if r.Code != 0 || !ok {
			t.Errorf("%s --help has no JSON section", command)
			continue
		}
		section, _, _ = strings.Cut(section, "\n\n")
		elsewhere := trunk
		if reflect.TypeOf(shape) == trunk {
			elsewhere = nil
		} else if nests(reflect.TypeOf(shape), trunk) && !strings.Contains(section, "as gwt trunk --json prints") {
			t.Errorf("%s --help's JSON section does not point at gwt trunk --json for its trunk", command)
		}
		for _, key := range jsonKeys(reflect.TypeOf(shape), elsewhere) {
			if !regexp.MustCompile(`\b` + regexp.QuoteMeta(key) + `\b`).MatchString(section) {
				t.Errorf("%s --help's JSON section does not name %q", command, key)
			}
		}
	}
}

// jsonKeys are the object keys a value of type t can print, nested ones
// included, read from its json tags; a type with its own MarshalJSON is asked.
// The keys of a nested elsewhere are left to the page that describes it.
func jsonKeys(t reflect.Type, elsewhere reflect.Type) []string {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	if t.Implements(reflect.TypeFor[json.Marshaler]()) {
		data, _ := json.Marshal(reflect.Zero(t).Interface())
		var m map[string]any
		json.Unmarshal(data, &m)
		var keys []string
		for k, v := range m {
			keys = append(keys, k)
			if sub, ok := v.(map[string]any); ok {
				for k := range sub {
					keys = append(keys, k)
				}
			}
		}
		return keys
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var keys []string
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !f.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		keys = append(keys, name)
		if f.Type.Kind() != reflect.Map && f.Type != elsewhere {
			keys = append(keys, jsonKeys(f.Type, elsewhere)...)
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// nests reports whether a field of struct t has type inner.
func nests(t, inner reflect.Type) bool {
	for field := range t.Fields() {
		if field.Type == inner {
			return true
		}
	}
	return false
}

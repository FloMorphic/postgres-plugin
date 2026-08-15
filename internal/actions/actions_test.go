package actions

import (
	"encoding/json"
	"testing"
)

// TestFormsBuild fails if any package-level form is malformed: formkit.Build
// panics on an invalid form, and merely loading this test package evaluates
// every form var, so a broken form cannot reach a user as a dialog that will
// not open.
func TestFormsBuild(t *testing.T) {
	r := New()
	if got := len(r.All()); got != 3 {
		t.Fatalf("want 3 actions, got %d", got)
	}
	for _, a := range r.All() {
		if a.Method == "" || a.RequestHandler == nil {
			t.Fatalf("action %q is incomplete", a.Title)
		}
		if a.Form.Jsonschema == "" {
			t.Fatalf("action %q has no schema", a.Method)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(a.Form.Jsonschema), &doc); err != nil {
			t.Fatalf("action %q schema does not parse: %v", a.Method, err)
		}
	}
	if r.SettingsForm().Jsonschema == "" {
		t.Fatal("settings form has no schema")
	}
}

func TestCoerceParam(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"42", int64(42)},
		{"42.5", 42.5},
		{"true", true},
		{"null", nil},
		{"alice", "alice"},
		{"007", "007"}, // invalid JSON (leading zero) → stays a string
		{`"42"`, "42"}, // quoted → forced string
		{"", ""},       // empty stays empty string
		{"1 2", "1 2"}, // trailing token → string
		{"user@x.com", "user@x.com"},
	}
	for _, c := range cases {
		if got := coerceParam(c.in); got != c.want {
			t.Errorf("coerceParam(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestParseParamsJSONObject(t *testing.T) {
	args, err := parseParams([]string{`{"k": 1}`})
	if err != nil {
		t.Fatal(err)
	}
	obj, ok := args[0].(map[string]any)
	if !ok {
		t.Fatalf("want map, got %T", args[0])
	}
	if obj["k"] != int64(1) {
		t.Errorf("want k=1 (int64), got %#v", obj["k"])
	}
}

package actions

import (
	"encoding/json"
	"strings"
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

func TestBuildCreateDDL(t *testing.T) {
	// Bare column definitions are wrapped with the quoted, schema-qualified name.
	got, err := buildCreateDDL("public", "findings", "id int primary key, name text")
	if err != nil {
		t.Fatal(err)
	}
	if want := `CREATE TABLE IF NOT EXISTS "public"."findings" (id int primary key, name text)`; got != want {
		t.Errorf("columns mode:\n got %q\nwant %q", got, want)
	}

	// A full statement runs as given, gains IF NOT EXISTS, and loses its ';'.
	full := "CREATE TABLE findings (\n  id integer primary key,\n  name varchar(255) not null\n);"
	got, err = buildCreateDDL("", "findings", full)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "CREATE TABLE IF NOT EXISTS findings (") {
		t.Errorf("full statement should gain IF NOT EXISTS, got %q", got)
	}
	if strings.HasSuffix(got, ";") {
		t.Errorf("trailing semicolon should be trimmed, got %q", got)
	}
	if !strings.Contains(got, "varchar(255) not null") {
		t.Errorf("body should be preserved, got %q", got)
	}

	// An already-idempotent statement is left untouched (no double clause).
	idem := "create table if not exists findings (id int)"
	if got, _ := buildCreateDDL("", "", idem); got != idem {
		t.Errorf("idempotent statement changed:\n got %q\nwant %q", got, idem)
	}

	// Column-only definitions without a table name are an error.
	if _, err := buildCreateDDL("", "", "id int"); err == nil {
		t.Error("columns without a table name should error")
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

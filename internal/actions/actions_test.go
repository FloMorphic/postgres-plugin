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
	if got := len(r.All()); got != 4 {
		t.Fatalf("want 4 actions, got %d", got)
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

func TestResolveLimit(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"", 100, false},     // empty → default/ceiling
		{"25", 25, false},    // within range
		{"100", 100, false},  // the ceiling itself
		{"5000", 100, false}, // clamped down to the ceiling
		{"0", 100, false},    // non-positive → ceiling, never unlimited
		{"-3", 100, false},   // negative → ceiling
		{"  10 ", 10, false}, // surrounding space tolerated
		{"{{$.n}}", 0, true}, // unresolved token is an error, not silently 0
		{"lots", 0, true},    // non-numeric is an error
	}
	for _, c := range cases {
		got, err := resolveLimit(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("resolveLimit(%q) = %d, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveLimit(%q) errored: %v", c.in, err)
		} else if got != c.want {
			t.Errorf("resolveLimit(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestBuildUpsertDDL(t *testing.T) {
	// Every PK column present → clash updates the non-key columns.
	got := buildUpsertDDL("public", "findings", []string{"finding_id", "name", "severity"}, []string{"finding_id"})
	want := `INSERT INTO "public"."findings" ("finding_id", "name", "severity") VALUES ($1, $2, $3) ` +
		`ON CONFLICT ("finding_id") DO UPDATE SET "name" = EXCLUDED."name", "severity" = EXCLUDED."severity" RETURNING *`
	if got != want {
		t.Errorf("upsert:\n got %q\nwant %q", got, want)
	}

	// A composite key, and only the key columns supplied → clash is a no-op.
	got = buildUpsertDDL("", "membership", []string{"org_id", "user_id"}, []string{"org_id", "user_id"})
	want = `INSERT INTO "membership" ("org_id", "user_id") VALUES ($1, $2) ` +
		`ON CONFLICT ("org_id", "user_id") DO NOTHING RETURNING *`
	if got != want {
		t.Errorf("key-only upsert:\n got %q\nwant %q", got, want)
	}

	// No primary key → plain insert.
	got = buildUpsertDDL("", "logs", []string{"message"}, nil)
	if want := `INSERT INTO "logs" ("message") VALUES ($1) RETURNING *`; got != want {
		t.Errorf("no-pk insert:\n got %q\nwant %q", got, want)
	}

	// Serial key left out (not among the values) → nothing to conflict on, so a
	// plain insert rather than an upsert.
	got = buildUpsertDDL("", "events", []string{"name"}, []string{"id"})
	if want := `INSERT INTO "events" ("name") VALUES ($1) RETURNING *`; got != want {
		t.Errorf("serial-key insert:\n got %q\nwant %q", got, want)
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

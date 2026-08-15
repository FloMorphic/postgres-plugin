package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/FloMorphic/postgres-plugin/internal/postgres"
	"github.com/Inflowenger/go-plugin-sdk/sdkv1"
	"github.com/jackc/pgx/v5"
)

// ------------------------------------------------------------------- read --

type queryInput struct {
	SQL     string   `json:"sql"`
	Params  []string `json:"params"`
	MaxRows int      `json:"maxRows"`
}

func (r *Registry) query() sdkv1.Action {
	return sdkv1.Action{
		Method:      "postgres.query",
		Title:       "Run Query (read)",
		Description: "Run a SELECT (or any row-returning statement) and get the rows back. Use $1, $2 … for values, and {{$.path}} to pull them from the flow.",
		Icon:        sdkv1.Icon{Icon: "mdi-database-search"},
		Form:        queryForm,
		RequestHandler: run(r, "query", func(ctx context.Context, job *sdkv1.Job, client *postgres.Client, in queryInput) (map[string]any, error) {
			if strings.TrimSpace(in.SQL) == "" {
				return nil, fmt.Errorf("missing required input: sql")
			}
			args, err := parseParams(in.Params)
			if err != nil {
				return nil, err
			}

			job.Progress(50, sdkv1.Frame{Title: "query", Content: preview(in.SQL)})

			res, err := client.Query(ctx, in.SQL, args, in.MaxRows)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"columns":  res.Columns,
				"rows":     res.Rows,
				"rowCount": res.RowCount,
			}, nil
		}),
	}
}

// ------------------------------------------------------------------ write --

type executeInput struct {
	SQL    string   `json:"sql"`
	Params []string `json:"params"`
}

func (r *Registry) execute() sdkv1.Action {
	return sdkv1.Action{
		Method:      "postgres.execute",
		Title:       "Execute (write)",
		Description: "Run an INSERT / UPDATE / DELETE and get the affected count. Add RETURNING to also read back rows. Use $1, $2 … and {{$.path}} for values.",
		Icon:        sdkv1.Icon{Icon: "mdi-database-edit"},
		Form:        executeForm,
		RequestHandler: run(r, "execute", func(ctx context.Context, job *sdkv1.Job, client *postgres.Client, in executeInput) (map[string]any, error) {
			if strings.TrimSpace(in.SQL) == "" {
				return nil, fmt.Errorf("missing required input: sql")
			}
			args, err := parseParams(in.Params)
			if err != nil {
				return nil, err
			}

			job.Progress(50, sdkv1.Frame{Title: "execute", Content: preview(in.SQL)})

			res, err := client.Exec(ctx, in.SQL, args)
			if err != nil {
				return nil, err
			}
			out := map[string]any{
				"command":      res.Command,
				"rowsAffected": res.RowsAffected,
				"rowCount":     res.RowCount,
			}
			if len(res.Rows) > 0 {
				out["columns"] = res.Columns
				out["rows"] = res.Rows
			}
			return out, nil
		}),
	}
}

// ----------------------------------------------------------- create table --

type createTableInput struct {
	Schema  string `json:"schema"`
	Table   string `json:"table"`
	Columns string `json:"columns"`
}

func (r *Registry) createTable() sdkv1.Action {
	return sdkv1.Action{
		Method:      "postgres.table.create",
		Title:       "Create Table (if not exists)",
		Description: "Create a table only if it does not already exist. Give it either PostgreSQL column definitions (with a table name) or a whole CREATE TABLE statement; existing tables are left untouched.",
		Icon:        sdkv1.Icon{Icon: "mdi-table-plus"},
		Form:        createTableForm,
		RequestHandler: run(r, "create table", func(ctx context.Context, job *sdkv1.Job, client *postgres.Client, in createTableInput) (map[string]any, error) {
			table := strings.TrimSpace(in.Table)
			columns := strings.TrimSpace(in.Columns)
			if columns == "" {
				return nil, fmt.Errorf("missing required input: columns")
			}

			ddl, err := buildCreateDDL(strings.TrimSpace(in.Schema), table, columns)
			if err != nil {
				return nil, err
			}

			job.Progress(50, sdkv1.Frame{Title: "create table", Content: preview(ddl)})

			res, err := client.Exec(ctx, ddl, nil)
			if err != nil {
				return nil, err
			}
			out := map[string]any{"command": res.Command, "ddl": ddl}
			if table != "" {
				out["table"] = table
			}
			return out, nil
		}),
	}
}

var (
	// createTableRe matches the head of a CREATE TABLE statement up to (and
	// including) the whitespace before the table name — so a whole statement can
	// be told apart from a bare column list, and IF NOT EXISTS can be spliced in.
	createTableRe = regexp.MustCompile(`(?i)^\s*CREATE\s+TABLE\s+`)
	// ifNotExistsRe recognises a statement that is already idempotent, so the
	// clause is never inserted twice.
	ifNotExistsRe = regexp.MustCompile(`(?i)^\s*CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\b`)
)

// buildCreateDDL turns the form input into the statement to run. Two shapes are
// accepted:
//
//   - A whole CREATE TABLE statement (what most people paste). It is run as
//     given — full control over names, types, constraints — with IF NOT EXISTS
//     spliced in when absent so re-running a flow stays a no-op, and a trailing
//     semicolon trimmed (the extended protocol runs one statement).
//   - Bare column definitions, the text inside the parentheses. These are wrapped
//     as CREATE TABLE IF NOT EXISTS <table> (...), so a table name is required.
//
// The table name is an identifier, not a value, so it cannot be a bound
// parameter — it is quoted with pgx.Identifier, which rejects a name it cannot
// represent safely.
func buildCreateDDL(schema, table, columns string) (string, error) {
	if createTableRe.MatchString(columns) {
		ddl := strings.TrimRight(columns, " \t\r\n;")
		if !ifNotExistsRe.MatchString(ddl) {
			ddl = createTableRe.ReplaceAllString(ddl, "CREATE TABLE IF NOT EXISTS ")
		}
		return ddl, nil
	}

	if table == "" {
		return "", fmt.Errorf("missing required input: table (needed when the definition is only columns, not a full CREATE TABLE statement)")
	}
	ident := pgx.Identifier{table}
	if schema != "" {
		ident = pgx.Identifier{schema, table}
	}
	return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", ident.Sanitize(), columns), nil
}

// ---------------------------------------------------------------- helpers --

// parseParams turns the positional param strings the form collects into the
// typed `$1…$n` arguments pgx binds.
//
// Each entry is read as a JSON scalar so a value carries its type: 42 is an
// integer, true a boolean, null the SQL NULL, {"k":1} a jsonb object. Anything
// that is not valid JSON — a bare word, an e-mail, a zero-padded code like 007 —
// is a plain string, which is almost always what a hand-typed value is. To force
// a value to stay a string, quote it: "42".
func parseParams(params []string) ([]any, error) {
	if len(params) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(params))
	for _, p := range params {
		args = append(args, coerceParam(p))
	}
	return args, nil
}

// coerceParam reads one param string. A clean JSON value wins; otherwise the raw
// string is the value.
func coerceParam(s string) any {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return s
	}

	dec := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return s
	}
	// A trailing token means it was not a single JSON value (e.g. `1 2`), so it
	// is really a string.
	if dec.More() {
		return s
	}
	return normalizeNumbers(v)
}

// normalizeNumbers walks a decoded JSON value and turns json.Number into int64
// (when integral) or float64, so a whole number does not reach pgx as a float
// and get bound to an integer column as 42.0.
func normalizeNumbers(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	case []any:
		for i := range t {
			t[i] = normalizeNumbers(t[i])
		}
		return t
	case map[string]any:
		for k := range t {
			t[k] = normalizeNumbers(t[k])
		}
		return t
	default:
		return v
	}
}

// preview trims SQL to one short line for a progress frame.
func preview(sql string) string {
	sql = strings.Join(strings.Fields(sql), " ")
	if len(sql) > 120 {
		return sql[:117] + "…"
	}
	return sql
}

package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/FloMorphic/postgres-plugin/internal/postgres"
	"github.com/Inflowenger/go-plugin-sdk/sdkv1"
	"github.com/jackc/pgx/v5"
)

// ------------------------------------------------------------------- read --

type queryInput struct {
	SQL   string `json:"sql"`
	Limit string `json:"limit"`
}

// maxRowLimit is the ceiling on how many rows a read returns. A larger request
// is clamped to it rather than refused, and it is also the default.
const maxRowLimit = 100

func (r *Registry) query() sdkv1.Action {
	return sdkv1.Action{
		Method:      "postgres.query",
		Title:       "Run Query (read)",
		Description: "Run a SELECT (or any row-returning statement) and get the rows back. Pull values from the flow inline with {{$.path}}. At most 100 rows are returned.",
		Icon:        sdkv1.Icon{Icon: "mdi-database-search"},
		Form:        queryForm,
		RequestHandler: run(r, "query", func(ctx context.Context, job *sdkv1.Job, client *postgres.Client, in queryInput) (map[string]any, error) {
			if strings.TrimSpace(in.SQL) == "" {
				return nil, fmt.Errorf("missing required input: sql")
			}
			limit, err := resolveLimit(in.Limit)
			if err != nil {
				return nil, err
			}

			job.Progress(50, sdkv1.Frame{Title: "query", Content: preview(in.SQL)})

			res, err := client.Query(ctx, in.SQL, nil, limit)
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

// resolveLimit turns the (already {{$.path}}-resolved) limit field into the row
// cap to apply. Empty means the default; anything over the ceiling is clamped to
// it, so a read never returns more than maxRowLimit rows. A non-number is an
// error, since it is almost always an unresolved token the user meant to fill.
func resolveLimit(raw string) (int, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return maxRowLimit, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("max rows must be a whole number up to %d: %q", maxRowLimit, s)
	}
	if n <= 0 || n > maxRowLimit {
		return maxRowLimit, nil
	}
	return n, nil
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

// --------------------------------------------------------- insert / upsert --

// insertInput is a row to write, given one of two ways. The column fields loaded
// into the form arrive under `values`; a pasted JSON object arrives as `record`
// and, when present, wins.
type insertInput struct {
	Schema string         `json:"schema"`
	Table  string         `json:"table"`
	Values map[string]any `json:"values"`
	Record string         `json:"record"`
}

func (r *Registry) insert() sdkv1.Action {
	return sdkv1.Action{
		Method:      "postgres.record.insert",
		Title:       "Insert / Upsert record",
		Description: "Write a row to a table from column fields or a JSON record. On a primary-key clash the existing row is updated instead of inserted (upsert). Values accept {{$.path}} tokens.",
		Icon:        sdkv1.Icon{Icon: "mdi-table-row-plus-after"},
		Form:        insertForm,
		RequestHandler: run(r, "insert", func(ctx context.Context, job *sdkv1.Job, client *postgres.Client, in insertInput) (map[string]any, error) {
			table := strings.TrimSpace(in.Table)
			if table == "" {
				return nil, fmt.Errorf("missing required input: table")
			}
			data, err := insertData(job, in)
			if err != nil {
				return nil, err
			}
			if len(data) == 0 {
				return nil, fmt.Errorf("no column values: fill in at least one column field, or a JSON record")
			}

			schema := strings.TrimSpace(in.Schema)
			qualified := table
			if schema != "" {
				qualified = schema + "." + table
			}

			// Discover the primary key so a clash can be turned into an update.
			pk, err := client.PrimaryKeyColumns(ctx, qualified)
			if err != nil {
				return nil, err
			}

			cols := sortedMapKeys(data)
			args := make([]any, len(cols))
			for i, c := range cols {
				args[i] = data[c]
			}
			upsert := len(pk) > 0 && subset(pk, cols)
			sql := buildUpsertDDL(schema, table, cols, pk)

			job.Progress(50, sdkv1.Frame{Title: "insert", Content: preview(sql)})

			res, err := client.Exec(ctx, sql, args)
			if err != nil {
				return nil, err
			}
			out := map[string]any{
				"command":      res.Command,
				"rowsAffected": res.RowsAffected,
				"table":        table,
				"upsert":       upsert,
			}
			if len(res.Rows) > 0 {
				out["columns"] = res.Columns
				out["rows"] = res.Rows
				out["row"] = res.Rows[0]
			}
			return out, nil
		}),
	}
}

// insertData turns the action's two input shapes into the column → value map to
// write. A JSON record wins when present; otherwise the loaded column fields are
// used, with each value resolved and typed the way a positional param is.
func insertData(job *sdkv1.Job, in insertInput) (map[string]any, error) {
	// A pasted record. Its {{$.path}} tokens were resolved by run() before it
	// reached here, since Record is a plain string field, so it now parses as
	// ordinary JSON. UseNumber keeps whole numbers off float64.
	if rec := strings.TrimSpace(in.Record); rec != "" {
		dec := json.NewDecoder(strings.NewReader(rec))
		dec.UseNumber()
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			return nil, fmt.Errorf("JSON record does not parse as an object: %w", err)
		}
		normalized, _ := normalizeNumbers(obj).(map[string]any)
		return normalized, nil
	}

	// Column fields. run()'s reflect walker rewrites strings and []string on the
	// input, but not the values inside this map, so resolve them here. A blank
	// column is omitted so the column's default (or serial) applies.
	vr := newVarResolver(job)
	data := make(map[string]any, len(in.Values))
	for col, raw := range in.Values {
		s, ok := raw.(string)
		if !ok {
			if raw != nil {
				data[col] = raw
			}
			continue
		}
		if s = strings.TrimSpace(vr.resolve(s)); s == "" {
			continue
		}
		data[col] = coerceParam(s)
	}
	return data, nil
}

// buildUpsertDDL renders the write. Values are bound as $1…$n, never spliced in;
// only the identifiers — the table and the column names — are put into the text,
// each quoted with pgx.Identifier so a name it cannot represent safely is
// rejected rather than injected.
//
// When every primary-key column is among the values supplied, a clash becomes an
// UPDATE of the other columns (an upsert); when only the key is supplied, the
// clash is a no-op. With no usable key — none defined, or a serial one left out
// to be generated — there is nothing to conflict on, so it is a plain insert.
func buildUpsertDDL(schema, table string, cols, pk []string) string {
	ident := pgx.Identifier{table}
	if schema != "" {
		ident = pgx.Identifier{schema, table}
	}

	colIdents := make([]string, len(cols))
	placeholders := make([]string, len(cols))
	for i, c := range cols {
		colIdents[i] = pgx.Identifier{c}.Sanitize()
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	base := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		ident.Sanitize(), strings.Join(colIdents, ", "), strings.Join(placeholders, ", "))

	if len(pk) == 0 || !subset(pk, cols) {
		return base + " RETURNING *"
	}

	pkSet := make(map[string]bool, len(pk))
	conflict := make([]string, len(pk))
	for i, c := range pk {
		conflict[i] = pgx.Identifier{c}.Sanitize()
		pkSet[c] = true
	}

	assigns := make([]string, 0, len(cols))
	for _, c := range cols {
		if pkSet[c] {
			continue // never overwrite the key with itself
		}
		q := pgx.Identifier{c}.Sanitize()
		assigns = append(assigns, fmt.Sprintf("%s = EXCLUDED.%s", q, q))
	}

	if len(assigns) == 0 {
		return fmt.Sprintf("%s ON CONFLICT (%s) DO NOTHING RETURNING *", base, strings.Join(conflict, ", "))
	}
	return fmt.Sprintf("%s ON CONFLICT (%s) DO UPDATE SET %s RETURNING *",
		base, strings.Join(conflict, ", "), strings.Join(assigns, ", "))
}

// subset reports whether every element of sub is in super.
func subset(sub, super []string) bool {
	set := make(map[string]bool, len(super))
	for _, s := range super {
		set[s] = true
	}
	for _, s := range sub {
		if !set[s] {
			return false
		}
	}
	return true
}

// sortedMapKeys orders a value map's keys, so the generated column list and the
// bound arguments line up deterministically.
func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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

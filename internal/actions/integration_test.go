package actions

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/FloMorphic/postgres-plugin/internal/postgres"
)

// TestIntegration exercises the real database path — create-from-JSON, the
// existence no-op, an insert with typed params, and a read back — against a live
// Postgres. It is skipped unless PG_TEST_DSN is set, so `go test ./...` stays
// hermetic.
//
//	PG_TEST_DSN="host=localhost port=5432 user=postgres password=... dbname=postgres sslmode=disable" \
//	  go test ./internal/actions -run TestIntegration -v
func TestIntegration(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set PG_TEST_DSN to run the live-database integration test")
	}

	client, err := postgres.New(postgres.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	const table = "findings_it"
	// Start clean, however a previous run left off.
	if _, err := client.Exec(ctx, `DROP TABLE IF EXISTS public.findings_it`, nil); err != nil {
		t.Fatalf("pre-drop: %v", err)
	}
	defer client.Exec(context.Background(), `DROP TABLE IF EXISTS public.findings_it`, nil)

	// 1. Create it from a full CREATE TABLE statement, routed through the real
	//    buildCreateDDL so the whole path is exercised. IF NOT EXISTS is spliced
	//    in and the trailing semicolon trimmed.
	full := `CREATE TABLE findings_it (
		finding_id   INTEGER PRIMARY KEY,
		finding_name VARCHAR(255) NOT NULL,
		severity     VARCHAR(20) NOT NULL,
		description  TEXT,
		status       VARCHAR(50) NOT NULL,
		created_at   TIMESTAMPTZ NOT NULL
	);`
	ddl, err := buildCreateDDL("", table, full)
	if err != nil {
		t.Fatalf("buildCreateDDL: %v", err)
	}
	t.Logf("DDL: %s", ddl)
	if _, err := client.Exec(ctx, ddl, nil); err != nil {
		t.Fatalf("create: %v", err)
	}

	// 2. Re-running is a no-op — IF NOT EXISTS leaves the table untouched.
	if _, err := client.Exec(ctx, ddl, nil); err != nil {
		t.Fatalf("create again: %v", err)
	}

	// 3. Insert the record with typed params, as the Execute action would.
	insert := `INSERT INTO public.findings_it (finding_id, finding_name, severity, description, status, created_at)
	           VALUES ($1, $2, $3, $4, $5, $6)`
	args, err := parseParams([]string{"2", "Duplicate Records", "Medium", "Duplicate customer entries detected.", "In Progress", "2026-08-16T11:00:00Z"})
	if err != nil {
		t.Fatalf("parseParams: %v", err)
	}
	exec, err := client.Exec(ctx, insert, args)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if exec.Command != "INSERT" || exec.RowsAffected != 1 {
		t.Fatalf("insert result = %q, rows=%d; want INSERT, 1", exec.Command, exec.RowsAffected)
	}

	// 5. Read it back.
	res, err := client.Query(ctx, `SELECT finding_id, finding_name, severity, created_at FROM public.findings_it WHERE finding_id = $1`, []any{int64(2)}, 100)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if res.RowCount != 1 {
		t.Fatalf("query returned %d rows, want 1", res.RowCount)
	}
	row := res.Rows[0]
	t.Logf("row read back: %+v", row)
	// An INTEGER column comes back as int32; the point is the value, not the
	// width, so compare stringwise.
	if fmt.Sprint(row["finding_id"]) != "2" {
		t.Errorf("finding_id = %#v, want 2", row["finding_id"])
	}
	if row["finding_name"] != "Duplicate Records" {
		t.Errorf("finding_name = %#v", row["finding_name"])
	}
	if _, ok := row["created_at"].(string); !ok {
		t.Errorf("created_at = %#v, want an RFC3339 string", row["created_at"])
	}

	// 6. The Insert action's upsert path: discover the primary key, build the
	//    ON CONFLICT statement, and run it against the row just inserted. The key
	//    (finding_id = 2) already exists, so this must UPDATE rather than insert a
	//    second row — the whole point of the upsert.
	pk, err := client.PrimaryKeyColumns(ctx, "public.findings_it")
	if err != nil {
		t.Fatalf("primary key: %v", err)
	}
	if len(pk) != 1 || pk[0] != "finding_id" {
		t.Fatalf("primary key = %v, want [finding_id]", pk)
	}
	cols := []string{"created_at", "description", "finding_id", "finding_name", "severity", "status"}
	upsert := buildUpsertDDL("public", "findings_it", cols, pk)
	t.Logf("upsert: %s", upsert)
	up, err := client.Exec(ctx, upsert, []any{
		"2026-08-16T11:00:00Z", "Now resolved.", int64(2), "Duplicate Records", "High", "Resolved",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if up.RowsAffected != 1 {
		t.Fatalf("upsert affected %d rows, want 1", up.RowsAffected)
	}

	// The update must have landed, and there must still be exactly one row for
	// the key — not a duplicate.
	res, err = client.Query(ctx, `SELECT severity, status FROM public.findings_it WHERE finding_id = $1`, []any{int64(2)}, 100)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if res.RowCount != 1 {
		t.Fatalf("after upsert, key has %d rows, want 1", res.RowCount)
	}
	if got := res.Rows[0]["status"]; got != "Resolved" {
		t.Errorf("status = %#v, want Resolved (upsert should have updated it)", got)
	}
	if got := res.Rows[0]["severity"]; got != "High" {
		t.Errorf("severity = %#v, want High", got)
	}
}

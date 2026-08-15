package postgres

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Client is a ready-to-use pool for one connection. Query and Exec are the two
// halves of the plugin — reading rows, and writing with a count — and both
// accept positional `$1…$n` arguments so user input never lands in the SQL text.
type Client struct {
	pool     *pgxpool.Pool
	connInfo string
}

// New opens a pgx pool for the config. It parses the DSN eagerly so a malformed
// connection string fails here, at set-up, rather than on the first query.
func New(cfg Config) (*Client, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.ConnString())
	if err != nil {
		return nil, fmt.Errorf("invalid connection settings: %w", err)
	}

	// pgxpool.New does not dial until a connection is first needed, which keeps
	// start-up cheap; the Ping in the connection test is what proves the pool.
	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		return nil, fmt.Errorf("cannot open connection pool: %w", err)
	}

	conn := poolCfg.ConnConfig
	return &Client{pool: pool, connInfo: fmt.Sprintf("%s:%d/%s", conn.Host, conn.Port, conn.Database)}, nil
}

// Close releases the pool. It is called when a client is evicted from the cache.
func (c *Client) Close() { c.pool.Close() }

// ConnInfo is host:port/database — enough to say which database a node is
// talking to, with no credentials in it.
func (c *Client) ConnInfo() string { return c.connInfo }

// Ping verifies the connection is live, for the "Test connection" button.
func (c *Client) Ping(ctx context.Context) error { return c.pool.Ping(ctx) }

// Result is what a read returns: the column names in order, and the rows as
// JSON-able maps.
type Result struct {
	Columns  []string         `json:"columns"`
	Rows     []map[string]any `json:"rows"`
	RowCount int              `json:"rowCount"`
}

// Query runs a statement and reads its rows. It is used for reads (SELECT), and
// also for a write whose statement carries RETURNING — pgx surfaces those rows
// the same way. `maxRows` caps how many are read; 0 means no cap.
func (c *Client) Query(ctx context.Context, sql string, args []any, maxRows int) (Result, error) {
	rows, err := c.pool.Query(ctx, sql, args...)
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.Name
	}

	out := make([]map[string]any, 0, 16)
	for rows.Next() {
		if maxRows > 0 && len(out) >= maxRows {
			break
		}
		values, err := rows.Values()
		if err != nil {
			return Result{}, err
		}
		row := make(map[string]any, len(columns))
		for i, col := range columns {
			row[col] = normalizeValue(values[i])
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return Result{}, err
	}
	return Result{Columns: columns, Rows: out, RowCount: len(out)}, nil
}

// ExecResult is what a write returns: the command tag (INSERT, UPDATE, …), the
// number of rows it affected, and any rows a RETURNING clause produced.
type ExecResult struct {
	Command      string           `json:"command"`
	RowsAffected int64            `json:"rowsAffected"`
	Columns      []string         `json:"columns,omitempty"`
	Rows         []map[string]any `json:"rows,omitempty"`
	RowCount     int              `json:"rowCount"`
}

// Exec runs a write (INSERT / UPDATE / DELETE / DDL). It goes through Query
// rather than pool.Exec so a RETURNING clause still yields its rows; the command
// tag, read after the rows are drained, carries the affected count either way.
func (c *Client) Exec(ctx context.Context, sql string, args []any) (ExecResult, error) {
	rows, err := c.pool.Query(ctx, sql, args...)
	if err != nil {
		return ExecResult{}, err
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.Name
	}

	returned := make([]map[string]any, 0)
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return ExecResult{}, err
		}
		row := make(map[string]any, len(columns))
		for i, col := range columns {
			row[col] = normalizeValue(values[i])
		}
		returned = append(returned, row)
	}
	if err := rows.Err(); err != nil {
		return ExecResult{}, err
	}

	tag := rows.CommandTag()
	res := ExecResult{
		Command:      commandWord(tag),
		RowsAffected: tag.RowsAffected(),
		RowCount:     len(returned),
	}
	if len(returned) > 0 {
		res.Columns = columns
		res.Rows = returned
	}
	return res, nil
}

// commandWord is the leading verb of a command tag ("INSERT 0 3" → "INSERT").
func commandWord(tag pgconn.CommandTag) string {
	s := tag.String()
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			return s[:i]
		}
	}
	return s
}

// normalizeValue turns a pgx-decoded value into something that marshals to clean
// JSON. pgx hands back Go values faithful to the column type — a []byte for
// bytea, a [16]byte for uuid, a time.Time for timestamptz — and left as-is those
// would serialise as byte arrays of numbers. The scalars pass straight through.
func normalizeValue(value any) any {
	switch v := value.(type) {
	case nil:
		return nil
	case time.Time:
		return v.Format(time.RFC3339Nano)
	case [16]byte: // uuid
		return fmt.Sprintf("%x-%x-%x-%x-%x", v[0:4], v[4:6], v[6:8], v[8:10], v[10:16])
	case []byte:
		if utf8.Valid(v) {
			return string(v)
		}
		return base64.StdEncoding.EncodeToString(v)
	default:
		return v
	}
}

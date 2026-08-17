package actions

import (
	"github.com/Inflowenger/go-plugin-sdk/formkit"
)

// Every form this plugin serves is declared here with the SDK's formkit builder,
// which generates the JSON Schema and the JSON Forms UI schema from one
// statement per field. A malformed form panics at start-up (Build calls
// Validate), where it is a compile-time-shaped mistake rather than a dialog that
// will not open.

const (
	descSQL    = `SQL to run. Bind values with $1, $2 … (never string-concatenate them). Pull values from the flow with {{$.path}}, e.g. WHERE id = $1 with the first parameter set to {{$.trigger.userId}}.`
	descParams = `Values for the $1, $2 … placeholders in the SQL above, in order: the first row is $1, the second $2, and so on. They are bound to the prepared statement, never pasted into the SQL text, so a value can never alter the query. Each is read as JSON so it keeps its type — 42 an integer, true a boolean, null the SQL NULL, {"k":1} a jsonb object — and anything that is not JSON is a plain string. A value may be a {{$.path}} token; to force text, quote it: "42".`

	descQuerySQL = `SELECT (or any row-returning statement) to run. Pull values straight from the flow with {{$.path}}, e.g. WHERE tenant = '{{$.trigger.tenantId}}'.`
	descLimit    = `Most rows to return. Capped at 100 — a larger number is treated as 100. Accepts a {{$.path}} token that resolves to a number.`

	descInsertRecord = `Optional JSON object of column → value, e.g. {"finding_id": 7, "severity": "high"}. When given, it wins over the column fields above. Values may be {{$.path}} tokens: {"name": "{{$.user.name}}"}. On a primary-key clash the row is updated instead (upsert).`
)

// ---------------------------------------------------------------- settings --

// settingsForm is stored by the platform as a reusable settings profile and
// shipped with every call as body.settings. The plugin keeps nothing.
//
// A whole DSN or the discrete fields — either works; a DSN, when given, wins.
var settingsForm = formkit.New("Postgres connection").
	Describe("Stored by the platform as a reusable settings profile and shipped with every call as body.settings. The plugin keeps nothing. Give a full connection string, or fill in the fields below.").
	SubmitTo("postgres.meta.ping.check").
	Add(
		formkit.Text("dsn", "Connection string").
			Describe("postgres://user:pass@host:5432/dbname?sslmode=require — or libpq keyword/value. Given, it wins; leave empty to use the fields below."),
		formkit.Text("host", "Host").Describe("e.g. localhost or db.internal"),
		formkit.Integer("port", "Port").Default(5432),
		formkit.Text("database", "Database"),
		formkit.Text("user", "User"),
		formkit.Secret("password", "Password"),
		formkit.Enum("sslmode", "SSL mode", "disable", "allow", "prefer", "require", "verify-ca", "verify-full").
			Default("prefer"),
		// The button hangs off the last field and its answer appears under it, so
		// a bad connection is reported while it is being entered rather than by
		// every node that later fails. It writes no value — saying what it found
		// is the whole point.
		formkit.Text("test", "Test connection").
			Describe("Press ↻ to check these values against the live server before saving.").
			Lookup("postgres.meta.ping.check", "Test connection"),
	).
	Build()

// ------------------------------------------------------------------ actions --

var queryForm = formkit.New("Run query (read)").Add(
	formkit.TextArea("sql", "SQL").Required().
		Describe(descQuerySQL).
		Help("Read-only intent: use this for SELECT. Pull values from the flow inline with {{$.path}}. Writes belong on the Execute or Insert action."),
	formkit.Text("limit", "Max rows").Default("100").
		Describe(descLimit),
).Build()

var executeForm = formkit.New("Execute (write)").Add(
	formkit.TextArea("sql", "SQL").Required().
		Describe(descSQL).
		Help("INSERT / UPDATE / DELETE. Add RETURNING to read rows back alongside the affected count. Example: UPDATE orders SET status = $1 WHERE id = $2 — with Parameters row 1 = shipped (→ $1) and row 2 = 42 (→ $2)."),
	formkit.List("params", "Parameters ($1, $2 …)").Describe(descParams).
		Help("One row per placeholder in the SQL above, in order: row 1 fills $1, row 2 fills $2, … For the example query: row 1 = shipped, row 2 = 42."),
).Build()

// insertFormDef is kept as the builder (not just its Built form) because the
// columns-load meta rebuilds the dialog from it — SchemaMap / UIMap give it a
// fresh copy to splice the discovered column fields into. See columnsEnvelope.
var insertFormDef = formkit.New("Insert / upsert record").
	Describe("Add a row to a table. Pick the table, press ↻ to load its columns as value fields, and fill the ones you want — or paste a JSON record below. If a row with the same primary key already exists, it is updated instead of inserted.").
	Add(
		formkit.Text("schema", "Schema").
			Describe("Optional, e.g. public. Empty uses the connection's search_path."),
		// One button does both steps: with the box empty it lists the tables to
		// pick from; once a table is set, pressing it again loads that table's
		// columns as the value fields below. See metaTablePick.
		formkit.Text("table", "Table").Required().
			Describe("The table to write to. Press ↻ with the box empty to list tables; pick one, then press ↻ again to load its columns as value fields below.").
			Lookup("postgres.meta.table.pick", "Tables / columns"),
		formkit.TextArea("record", "JSON record (optional)").
			Describe(descInsertRecord),
	)

var insertForm = insertFormDef.Build()

var createTableForm = formkit.New("Create table (if not exists)").Add(
	formkit.Text("schema", "Schema").Describe("Optional, e.g. public. Empty uses the connection's search_path. Used only with column definitions; ignored when you paste a full statement."),
	formkit.Text("table", "Table name").
		Describe("Identifier only, e.g. findings. Required when the definition below is only columns; not needed when you paste a full CREATE TABLE statement."),
	formkit.TextArea("columns", "Column definitions or full statement").Required().
		Describe(`Either a whole CREATE TABLE statement — CREATE TABLE findings (finding_id integer primary key, severity varchar(20) not null, ...) — or just the column definitions that go inside the parentheses — finding_id integer primary key, severity varchar(20) not null. This is SQL, not a JSON record. IF NOT EXISTS is applied for you.`),
).Build()

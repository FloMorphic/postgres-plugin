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
	descParams = `Positional values for $1, $2 … in order. Each is read as JSON so it keeps its type — 42 is an integer, true a boolean, null the SQL NULL — and anything that is not JSON is a plain string. A value may be a {{$.path}} token. To force text, quote it: "42".`
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
		Describe(descSQL).
		Help("Read-only intent: use this for SELECT. Writes belong on the Execute action."),
	formkit.List("params", "Parameters").Describe(descParams),
	formkit.Integer("maxRows", "Max rows").Default(1000).Min(0).
		Describe("Stop reading after this many rows. 0 means no limit."),
).Build()

var executeForm = formkit.New("Execute (write)").Add(
	formkit.TextArea("sql", "SQL").Required().
		Describe(descSQL).
		Help("INSERT / UPDATE / DELETE. Add RETURNING to read rows back alongside the affected count."),
	formkit.List("params", "Parameters").Describe(descParams),
).Build()

var createTableForm = formkit.New("Create table (if not exists)").Add(
	formkit.Text("schema", "Schema").Describe("Optional, e.g. public. Empty uses the connection's search_path."),
	formkit.Text("table", "Table name").Required().
		Describe("Identifier only, e.g. events. It is quoted safely; do not include a schema here."),
	formkit.TextArea("columns", "Column definitions").Required().
		Describe(`The body of the parentheses, e.g. id bigserial primary key, name text not null, created_at timestamptz default now()`),
).Build()

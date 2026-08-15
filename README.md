# Inflowenger Postgres plugin

A PostgreSQL node for the Inflowenger workflow canvas, built on the
`go-plugin-sdk` (`sdkv1`) under the **inflowv1** protocol. It runs as a
long-running Go process and is called by the Fractal runtime over NATS.

The plugin holds **no** database configuration. A connection travels in every
call as a settings profile (`body.settings`), so one running plugin serves many
databases and rotating a password needs no redeploy.

## Actions

| Action | Method | What it does |
| --- | --- | --- |
| **Run Query (read)** | `postgres.query` | Runs a SELECT (or any row-returning statement) and returns the rows, their column order, and a count. |
| **Execute (write)** | `postgres.execute` | Runs an INSERT / UPDATE / DELETE and returns the command tag and affected count. Add `RETURNING` to also read rows back. |
| **Create Table (if not exists)** | `postgres.table.create` | Creates a table only when it does not already exist; existing tables are left untouched. |

## Parameters, safely

Never string-concatenate user input into SQL. Bind values with `$1, $2, …` and
supply them positionally in the **Parameters** list:

```sql
SELECT id, email FROM users WHERE org_id = $1 AND active = $2
```

```
params: ["42", "true"]
```

Each parameter is read as a JSON scalar so it keeps its type:

| You type | Bound as |
| --- | --- |
| `42` | integer |
| `42.5` | float |
| `true` | boolean |
| `null` | SQL NULL |
| `{"k": 1}` | jsonb object |
| `alice`, `user@x.com`, `007` | string (not valid JSON → left as text) |
| `"42"` | string (quoted → forced text) |

## `{{JSONPATH}}` — pull values from the flow

Any string input — the SQL text and every parameter — is resolved against the
flow scope before it runs. Write `{{$.path}}` and the runtime substitutes the
value from upstream nodes:

```sql
UPDATE orders SET status = $1 WHERE id = $2
```

```
params: ["shipped", "{{$.trigger.orderId}}"]
```

A token the scope can't supply is left verbatim rather than silently dropped.
Each distinct path is fetched from the runtime once per call, however many
fields reference it.

## Connection (settings profile)

The plugin's set-up form asks for either a full connection string **or** the
discrete fields:

- **Connection string** — `postgres://user:pass@host:5432/dbname?sslmode=require`
  (or libpq keyword/value). Given, it wins.
- **Host / Port / Database / User / Password / SSL mode** — used when no
  connection string is set.

Press **Test connection** (↻) to check the values against the live server before
saving. Keys are matched leniently (case, spaces, dashes and underscores are
ignored, plus the usual synonyms), so a hand-typed profile still resolves.

## Running

Provisioning is a prerequisite: the plugin must be defined in a space to get
`PLUGIN_ID`, `INFRA_CRED` (base64) and `INFRA_URL`. Put them in `.env.inflow`
(see `.env.inflow.example`) — **Postgres credentials never go here**, they are a
settings profile.

```sh
go build ./...
go run .
```

The SDK logs each subscribed subject on startup. Add the node to a flow, bind a
settings profile, and run it.

## Layout

```
main.go                     wiring: intro, settings, actions, block after Start()
internal/postgres/          connection profile → pgx pool → Query / Exec / Ping
internal/actions/           the three actions, the {{JSONPATH}} resolver, forms, the connection-test meta
```

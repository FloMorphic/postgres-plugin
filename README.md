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
| **Run Query (read)** | `postgres.query` | Runs a SELECT (or any row-returning statement) and returns the rows, their column order, and a count. Pull values from the flow inline with `{{$.path}}`. Returns **at most 100 rows**. |
| **Execute (write)** | `postgres.execute` | Runs an INSERT / UPDATE / DELETE and returns the command tag and affected count. Add `RETURNING` to also read rows back. |
| **Insert / Upsert record** | `postgres.record.insert` | Writes a row to a table from per-column fields or a JSON record. On a primary-key clash the existing row is **updated** (upsert). |
| **Create Table (if not exists)** | `postgres.table.create` | Creates a table only when it does not already exist; existing tables are left untouched. |

## Run Query (read)

Just a **SQL** field and a **Max rows** field — no positional parameters. Pull
values from the flow inline with `{{$.path}}`:

```sql
SELECT id, email FROM users WHERE org_id = '{{$.trigger.orgId}}' AND active = true
```

**Max rows** is capped at **100**: leave it blank for the default of 100, and any
larger number (or a `{{$.path}}` token that resolves to one) is treated as 100.

## Insert / Upsert record

Write one row to a table, given one of two ways:

- **Per-column fields.** The **Table** field has a single ↻ *Tables / columns*
  button: press it with the box empty to list the tables, pick one, then press it
  again to load that table's columns as value fields. Fill the ones you want; each
  accepts a literal or a `{{$.path}}` token, and a blank column is omitted so its
  default (or `SERIAL`) applies.
- **A JSON record.** Paste a `{ "column": value }` object into **JSON record**; it
  wins over the column fields when present. Values may be `{{$.path}}` tokens:
  ```json
  { "finding_id": 7, "severity": "high", "name": "{{$.event.title}}" }
  ```

The primary key is discovered automatically. When every key column is supplied,
a clash becomes an `ON CONFLICT … DO UPDATE` of the other columns (a true
upsert); when only the key is supplied, the clash is a no-op; with no usable key
(none defined, or a serial one left out to be generated) it is a plain insert.
Either way the written row comes back via `RETURNING *`.

## Create Table

The definition field accepts **PostgreSQL SQL** in either shape:

- **A whole `CREATE TABLE` statement** (what most people paste):
  ```sql
  CREATE TABLE findings (
      finding_id   INTEGER PRIMARY KEY,
      finding_name VARCHAR(255) NOT NULL,
      severity     VARCHAR(20) NOT NULL,
      created_at   TIMESTAMPTZ NOT NULL
  );
  ```
  Run as given; the table-name field is not needed.
- **Just the column definitions** — the text inside the parentheses — with the
  table name supplied separately:
  ```
  finding_id integer primary key, finding_name varchar(255) not null, severity varchar(20) not null
  ```

Either way it's SQL, not a JSON record, and `IF NOT EXISTS` is applied so an
existing table is left untouched. Creating a table makes it **empty** — inserting
rows is the Execute action's job.

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
internal/actions/           the four actions, the {{JSONPATH}} resolver, forms, the table/column + connection-test metas
```

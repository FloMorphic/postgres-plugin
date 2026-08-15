---
name: inflow-plugin
description: Build an Inflowenger Plugin node with the Go go-plugin-sdk (sdkv1). Use when the user asks to create, scaffold, or extend an inflow/Inflowenger plugin — adding an action, parsing request input, reporting progress, reading/writing flow context, building the action's UI form, or wiring settings. Not for extrinsic nodes (those belong to inflow-fusion).
---

# Building an Inflowenger Plugin node

Instructions for writing a plugin with `github.com/Inflowenger/go-plugin-sdk`
(`sdkv1`), imported as a **library**. A plugin is a long-running Go process that
appears as a node on the Inflowenger workflow canvas and is called by the Fractal
runtime over NATS.

Fuller reference lives in the SDK's GitHub repo (this skill is meant to be copied
into a consuming plugin project, so links point there rather than at local paths):
the human cookbook at
[`cookbook.md`](https://github.com/Inflowenger/go-plugin-sdk/blob/main/cookbook.md) and
concept docs under
[`docs/`](https://github.com/Inflowenger/go-plugin-sdk/tree/main/docs). Read those
for detail — this file is the operational checklist. Verify the current API against
the installed `go-plugin-sdk/sdkv1` package before relying on any signature; do not
invent methods.

## When to use

Use this when the task involves creating or modifying an inflow **plugin** node:
scaffolding a plugin, adding/editing an `Action`, decoding request bodies, progress
reporting, flow-context read/write, stopping a flow, or action/settings forms.

Do **not** use this for **extrinsic** nodes (internal service calls) — those are
registered via `inflow-fusion`, a different repo, and are out of scope here.

## The non-negotiable rules (get these right)

1. **`main` must block after `Start()`.** `p.Start()` only wires NATS subscriptions
   and returns immediately. End `main` with `select {}` (or equivalent) or the
   process exits and the plugin dies.
2. **Every handler ends in exactly one `job.Done(...)` or `job.DoneWithError(...)`
   on every path.** On each error branch call `job.DoneWithError(err.Error())` **and
   `return`**. Never finish twice, never finish zero times.
3. **Decode input with `sdkv1.CastRequestTo[T](job.Req.Data)`**, which unwraps the
   `{ "_registry", "body" }` envelope → `req.Body` (type `T`) + `req.Registry`
   (`map[string]any`). JSON numbers arrive as `float64`; convert before use.
4. **Keep each action's `Jsonschema` in sync with its input struct's JSON tags** —
   the form defines the shape delivered as `body`.
5. **Provisioning is a prerequisite, not code.** The plugin must be defined in a
   space (a NATS account in Infra) to get `PLUGIN_ID`, `INFRA_CRED` (base64), and
   `INFRA_URL`. If these are missing, tell the user to provision first; don't
   fabricate credentials.

## Procedure

1. **Confirm prerequisites**: `PLUGIN_ID`, `INFRA_CRED`, `INFRA_URL` (usually in a
   dotenv like `.env.inflow`), and that Infra + a Fractal are running. Add the dep
   with `go get github.com/Inflowenger/go-plugin-sdk@latest`.
2. **Scaffold `main`** (a `main` package, not a test):
   ```go
   p, err := sdkv1.NewPlugin(sdkv1.WithDotEnv(".env.inflow")) // or WithInfraConnection + WithPluginId
   // handle err
   p.Intro(sdkv1.PluginIntro{Name: "MY.PLUGIN", Author: "…", Version: "v0.0.1"})
   p.AddAction(sdkv1.Action{Method: "do.thing", Title: "…", Form: form, RequestHandler: handler})
   if err := p.Start(); err != nil { /* handle */ }
   select {}
   ```
3. **Write each `RequestHandler(job sdkv1.Job)`** using only these verified `Job`
   operations:
   - `sdkv1.CastRequestTo[T](job.Req.Data)` — typed input (rule 3).
   - `job.Progress(pct, sdkv1.Frame{Title, Content})` — advisory, 0–100; does not finish.
   - `job.Done(map[string]any, key ...string)` — success + output (finishes).
   - `job.DoneWithError(string)` — failure (finishes).
   - `job.CmdGetCurrentScope()` / `job.CmdGetScope("$.path")` — read context; both
     return `any`, type-assert to `[]byte`.
   - `job.CmdSetOnPath("$.path", map[string]any{...})` — write into flow context.
   - `job.CmdSvcCall(action, data, opData)` — ask the extrinsics service to run
     `action` (e.g. `add.db.record`) through the runtime (feeder pattern);
     action is required and is not a registered extrinsics subject. The call is
     origin-tagged `plugin:<node title>`; the service may refuse it if plugin
     calls aren't granted.
   - `job.CmdStopFlow()` — abort the whole flow.
4. **Add forms** when the node needs configuration:
   `sdkv1.FormBuilder{Jsonschema: <JSON Schema>, Jsonui: <UI Schema>}` (JSON Forms).
   For plugin-level onboarding/config use `p.RequiredParams(&sdkv1.Settings{...})`
   with a `SubmitHandler`.
5. **Make dependent fields work.** Any field a user cannot type from memory (an
   `accountId`, a project key, an id valid only inside another selection) must not
   ship as a bare text input. Register a **meta function** and put a button on the
   control that calls it:
   ```go
   p.AddMeta(sdkv1.Meta{                    // before Start()
       Method:         "my.meta.users.resolve",
       RequestHandler: func(r sdkv1.Request) any { /* … */ },
   })
   ```
   ```jsonc
   // in Jsonui, on the control
   "x-inflow-ui": {
     "action": { "name": "pluginFn", "fn": "my.meta.users.resolve" },
     "button": { "position": "append", "label": "Find user" }
   }
   ```
   Four rules, each of which fails **silently** if broken:
   - `action.name` is always the literal `pluginFn`. It is the host's only action.
   - The request arrives **flat** (form fields + `settings` + `value` at the top
     level), *not* in the `{_registry, body}` action envelope — so
     `CastRequestTo` yields a zero struct here. Decode tolerantly, trying `body`
     first and then the raw bytes.
   - Return the **patch object** (`map[string]any{"assignee": "5b10…"}`), not
     `sdkv1.Response` — the latter's `{data,error}` envelope gets patched in as
     fields called `data` and `error`. Patch keys are absolute leaf paths.
   - There is no error channel in the transport. Say what happened under the
     reserved `x-inflow-notif` key, which the host lifts out of the answer and
     shows — or the button appears to do nothing:
     ```go
     map[string]any{
         "assignee":       "5b10…",                       // the value, as before
         "x-inflow-notif": map[string]any{"severity": "success", "message": "Assignee: Mehdi S."},
     }                                                    // severity: help|info|success|warning|error
     ```
     The message is addressed to the button's `targetField`. A field that another
     control fills needs `"x-inflow-notif": {"display": "inline"}` in its UI
     schema so the host has somewhere to put it; a field with its own button does
     not. Do **not** add a readonly `lookupStatus`-style property for this — a
     message is not form data, and one declared as a field is sent to the service
     and stored with the rest.
   Full contract: `docs/form-builder.md` and the catalog's `dependent-fields.md`.
6. **Build & run**: `go build ./...`, then `go run .`; the SDK logs each subscribed
   subject on startup. Verify by adding the node to a flow and running it.

## Known limitations to respect

- There is **no options-loading API**: a field's choices live in its schema, not
  its data, so nothing can add them to a `<select>` in place. To offer a list
  found at runtime, answer with a whole *form envelope* —
  `{schema, uischema, data}`, the same form with the target property rewritten as
  `oneOf: [{const, title}]` — and the host re-renders the dialog as that
  document. Answer with a patch for a single match, an envelope for several.
- Nothing fires automatically: no on-change, no debounce, no type-ahead. The user
  clicks. Label the button with what it does.
- If asked for anything about **extrinsic** nodes, redirect to `inflow-fusion`; it
  is not part of this SDK.

## Verify before finishing

- `go build ./...` passes.
- `main` blocks after `Start()`.
- Each action: unique `Method`, a `RequestHandler`, exactly one finish per path.
- Each `Jsonschema` matches its input struct.
- Every meta function is registered before `Start()`, decodes a flat body, and
  returns a patch (not `sdkv1.Response`) if a form button calls it.
- No fabricated SDK methods — every `Job`/`Plugin` call exists in `sdkv1/`.

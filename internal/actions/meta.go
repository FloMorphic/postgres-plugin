package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/Inflowenger/go-plugin-sdk/formkit"
	"github.com/Inflowenger/go-plugin-sdk/sdkv1"
)

// metaTimeout keeps meta RPCs snappy: they answer a form a user is waiting on,
// so a slow server should fail fast rather than hang the drawer.
const metaTimeout = 15 * time.Second

// Metas are synchronous helper RPCs served outside the job lifecycle. This
// plugin has one: the settings form's "Test connection" button.
func (r *Registry) Metas() []sdkv1.Meta {
	return []sdkv1.Meta{
		{Method: "postgres.meta.ping.check", RequestHandler: r.metaPingCheck},
	}
}

// metaPingCheck backs the settings form's "Test connection" button and its
// submit validation. It opens the connection being edited and pings it, so a
// wrong host or password is reported in the dialog instead of failing every
// node later.
//
// The values arrive one of two ways: on an action's drawer the bound profile is
// under "settings"; the plugin set-up dialog has no bound profile and sends the
// values being edited at the top level, alongside the keys the host adds.
func (r *Registry) metaPingCheck(req sdkv1.Request) any {
	body := decodeMeta[map[string]any](req.Data)

	conn, _ := body["settings"].(map[string]any)
	if len(conn) == 0 {
		conn = body
		for _, hostKey := range []string{"settings", "value", "targetField", "form"} {
			delete(conn, hostKey)
		}
	}

	client, err := r.pool.Client(conn)
	if err != nil {
		return formkit.Failure("%s", err).Patch(nil)
	}

	ctx, cancel := context.WithTimeout(context.Background(), metaTimeout)
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		return formkit.Failure("cannot reach %s: %s", client.ConnInfo(), err).Patch(nil)
	}
	// A patch with nothing but a message: the test writes no value into the
	// profile, and saying what it found is the whole point of the button.
	return formkit.Success("Connected to %s.", client.ConnInfo()).Patch(nil)
}

// Settings declares what a Postgres settings profile must contain. The platform
// renders this form when the plugin is set up, stores the answers as a named,
// reusable profile, and ships the values back with every call as body.settings.
//
// The submit handler is a validator, not a store: it checks the values against
// the live server so a wrong connection is reported in the dialog.
func (r *Registry) Settings() *sdkv1.Settings {
	return &sdkv1.Settings{
		FormBuilder: settingsForm,
		SubmitHandler: func(req sdkv1.Request) sdkv1.Response {
			submitted := decodeMeta[map[string]any](req.Data)
			if nested, ok := submitted["settings"].(map[string]any); ok {
				submitted = nested
			}

			client, err := r.pool.Client(submitted)
			if err != nil {
				return sdkv1.Response{Error: err.Error()}
			}

			ctx, cancel := context.WithTimeout(context.Background(), metaTimeout)
			defer cancel()

			if err := client.Ping(ctx); err != nil {
				return sdkv1.Response{Error: "connection test failed: " + err.Error()}
			}
			return sdkv1.Response{Data: map[string]any{"ok": true, "connectedTo": client.ConnInfo()}}
		},
	}
}

// SettingsForm exposes the same form for PluginIntro.Settings — the document the
// platform's plugin set-up dialog reads to build the profile editor.
func (r *Registry) SettingsForm() *sdkv1.FormBuilder {
	settings := r.Settings().FormBuilder
	return &settings
}

// decodeMeta reads a meta RPC's arguments. Meta calls come from the form
// renderer rather than the job pipeline, so the payload may or may not be
// wrapped in the {_registry, body} envelope — try both, and treat anything
// unreadable as "no arguments" rather than an error.
func decodeMeta[T any](data []byte) T {
	var out T
	if len(bytes.TrimSpace(data)) == 0 {
		return out
	}

	var envelope struct {
		Body json.RawMessage `json:"body"`
	}
	if json.Unmarshal(data, &envelope) == nil && len(envelope.Body) > 0 {
		if json.Unmarshal(envelope.Body, &out) == nil {
			return out
		}
	}

	_ = json.Unmarshal(data, &out)
	return out
}

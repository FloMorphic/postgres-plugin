// Package postgres turns the settings profile the platform ships with every
// call into a live connection pool, and runs read/write SQL over it.
//
// The plugin holds no database configuration of its own. A connection travels
// in each request as `body.settings`, the same named profile the runtime folds
// into action bodies — so one running plugin can serve many databases and
// rotating a password needs no redeploy.
package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// ErrNoSettings is returned when a call arrives without a connection. It is
// phrased for the person looking at the node, since that is where the fix is.
var ErrNoSettings = errors.New("this node has no Postgres connection: pick a settings profile in the node drawer (host, database, user, password) or create one from the plugin's set-up form")

// Config is a resolved Postgres connection. Either a full DSN is given, or the
// discrete fields are, and the two are merged into one keyword/value string
// pgx understands.
type Config struct {
	DSN      string
	Host     string
	Port     string
	Database string
	User     string
	Password string
	SSLMode  string
}

// ParseConfig reads a connection out of the settings profile the platform ships
// with every action call as `body.settings`.
//
// The profile is a free-form key/value record: it is filled from the plugin's
// own set-up form when the platform could read it, and hand-typed otherwise. So
// keys are matched leniently — case, spaces, dashes and underscores are ignored,
// and the usual synonyms for each field are accepted.
func ParseConfig(settings map[string]any) (Config, error) {
	if len(settings) == 0 {
		return Config{}, ErrNoSettings
	}

	values := make(map[string]string, len(settings))
	for key, value := range settings {
		if text := toString(value); text != "" {
			values[canonicalKey(key)] = text
		}
	}

	cfg := Config{
		DSN:      pick(values, dsnKeys),
		Host:     pick(values, hostKeys),
		Port:     pick(values, portKeys),
		Database: pick(values, databaseKeys),
		User:     pick(values, userKeys),
		Password: pick(values, passwordKeys),
		SSLMode:  pick(values, sslKeys),
	}

	if cfg.DSN == "" && cfg.Host == "" {
		// Something was configured, but none of it is a Postgres connection —
		// most likely the wrong profile is selected on the node.
		return Config{}, fmt.Errorf("%w (the selected profile has none of these: %s)", ErrNoSettings, strings.Join(sortedKeys(settings), ", "))
	}
	return cfg, nil
}

// ConnString renders the config as the keyword/value DSN pgx parses. A DSN given
// verbatim wins outright — a user who pasted a full connection string means it,
// and re-deriving one from half-filled fields would only fight them.
func (c Config) ConnString() string {
	if strings.TrimSpace(c.DSN) != "" {
		return strings.TrimSpace(c.DSN)
	}

	parts := make([]string, 0, 6)
	add := func(key, value string) {
		if value = strings.TrimSpace(value); value != "" {
			parts = append(parts, key+"="+quoteDSN(value))
		}
	}
	add("host", c.Host)
	add("port", c.Port)
	add("dbname", c.Database)
	add("user", c.User)
	add("password", c.Password)
	add("sslmode", firstNonEmpty(c.SSLMode, "prefer"))
	return strings.Join(parts, " ")
}

// quoteDSN wraps a value in single quotes when it holds a space or quote, which
// is how libpq keyword/value strings escape them — so a password with a space
// does not split the DSN into two settings.
func quoteDSN(value string) string {
	if !strings.ContainsAny(value, " '\\") {
		return value
	}
	replacer := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	return "'" + replacer.Replace(value) + "'"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// Field synonyms, in preference order. Each entry is already canonical (lower
// case, letters and digits only) — see canonicalKey.
var (
	dsnKeys      = []string{"dsn", "connectionstring", "connstring", "connectionurl", "url", "uri", "connuri"}
	hostKeys     = []string{"host", "hostname", "server", "address", "endpoint"}
	portKeys     = []string{"port"}
	databaseKeys = []string{"database", "dbname", "db", "schema"}
	userKeys     = []string{"user", "username", "role", "login", "account"}
	passwordKeys = []string{"password", "pass", "pwd", "secret", "apikey", "token"}
	sslKeys      = []string{"sslmode", "ssl", "tls", "sslmodel"}
)

func pick(values map[string]string, keys []string) string {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return value
		}
	}
	return ""
}

// canonicalKey reduces a settings key to letters and digits, lower case, so
// "Host name", "host_name" and "hostName" are one key.
func canonicalKey(key string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(key) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// toString renders a profile value. The settings editor stores anything that
// parses as JSON (numbers, booleans) as that type, so values are not always
// strings — a port arrives as a float64.
func toString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	case float64:
		if typed == float64(int64(typed)) {
			return fmt.Sprintf("%d", int64(typed))
		}
		return fmt.Sprintf("%v", typed)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func sortedKeys(settings map[string]any) []string {
	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// Pool hands out one client per distinct connection, so every job using the
// same settings profile shares a pgx connection pool instead of dialling
// afresh. Clients are safe for concurrent use.
type Pool struct {
	mu      sync.Mutex
	clients map[string]*Client
}

// maxPooledClients bounds the cache: a handful of profiles is the norm, and the
// pool is drained rather than grown without limit if that ever changes.
const maxPooledClients = 16

func NewPool() *Pool { return &Pool{clients: map[string]*Client{}} }

// Client resolves the settings profile shipped with a call into a client,
// opening (and caching) a pgx pool for it on first use.
func (p *Pool) Client(settings map[string]any) (*Client, error) {
	cfg, err := ParseConfig(settings)
	if err != nil {
		return nil, err
	}
	return p.For(cfg)
}

// For returns the client for an already-parsed config.
func (p *Pool) For(cfg Config) (*Client, error) {
	key := cfg.fingerprint()

	p.mu.Lock()
	defer p.mu.Unlock()

	if client, ok := p.clients[key]; ok {
		return client, nil
	}
	client, err := New(cfg)
	if err != nil {
		return nil, err
	}
	if len(p.clients) >= maxPooledClients {
		for k, c := range p.clients {
			c.Close()
			delete(p.clients, k)
		}
	}
	p.clients[key] = client
	return client, nil
}

// fingerprint identifies a connection without keeping the password in a map key.
func (c Config) fingerprint() string {
	sum := sha256.Sum256([]byte(c.ConnString()))
	return hex.EncodeToString(sum[:])
}

// Copyright Mondoo, Inc. 2024, 2026
// SPDX-License-Identifier: BUSL-1.1

package connection

import (
	"context"
	"database/sql"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.mondoo.com/mql/v13/providers-sdk/v1/inventory"
	"go.mondoo.com/mql/v13/providers-sdk/v1/plugin"
	"go.mondoo.com/mql/v13/providers-sdk/v1/vault"
)

type PostgresConnection struct {
	plugin.Connection
	Conf  *inventory.Config
	asset *inventory.Asset
	db    *sql.DB

	// Selection bits captured at connect time so the provider's detect step
	// can build a stable, descriptive asset identity.
	host     string
	port     int
	database string
	user     string
}

func NewPostgresConnection(id uint32, asset *inventory.Asset, conf *inventory.Config) (*PostgresConnection, error) {
	conn := &PostgresConnection{
		Connection: plugin.NewConnection(id, asset),
		Conf:       conf,
		asset:      asset,
	}

	dsn, host, port, database, user, err := buildDSN(conf)
	if err != nil {
		return nil, err
	}
	conn.host = host
	conn.port = port
	conn.database = database
	conn.user = user

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	// Limit the pool — we issue short, sequential metadata queries. A small
	// pool is plenty and keeps the server-side connection count predictable.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(2 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}

	conn.db = db
	return conn, nil
}

func (c *PostgresConnection) Name() string {
	return "postgres"
}

func (c *PostgresConnection) Asset() *inventory.Asset {
	return c.asset
}

func (c *PostgresConnection) DB() *sql.DB {
	return c.db
}

func (c *PostgresConnection) Host() string     { return c.host }
func (c *PostgresConnection) Port() int        { return c.port }
func (c *PostgresConnection) Database() string { return c.database }
func (c *PostgresConnection) User() string     { return c.user }

func (c *PostgresConnection) Close() {
	if c.db != nil {
		c.db.Close()
	}
}

// buildDSN turns the inventory Config into a libpq connection string. Order
// of precedence: a single `dsn` option (which is treated as authoritative,
// only overlaying the supplied password), then the individual host/port/user
// /database/sslmode options together with a password credential. Anything
// left unset falls through to libpq's environment-variable defaults (PGHOST,
// PGPORT, PGUSER, PGDATABASE, PGSSLMODE, PGPASSWORD).
func buildDSN(conf *inventory.Config) (dsn, host string, port int, database, user string, err error) {
	opts := conf.Options
	if opts == nil {
		opts = map[string]string{}
	}

	var password string
	for _, cred := range conf.Credentials {
		if cred.Type == vault.CredentialType_password {
			if cred.User != "" {
				user = cred.User
			}
			password = string(cred.Secret)
		}
	}

	if rawDSN := strings.TrimSpace(opts["dsn"]); rawDSN != "" {
		host, port, database, user, err = parseDSNComponents(rawDSN)
		if err != nil {
			return "", "", 0, "", "", err
		}
		// If a password credential was supplied separately, fold it into the DSN
		// rather than expecting the user to encode credentials twice.
		if password != "" {
			rawDSN, err = mergePasswordIntoDSN(rawDSN, password)
			if err != nil {
				return "", "", 0, "", "", err
			}
		}
		return rawDSN, host, port, database, user, nil
	}

	host = firstNonEmpty(opts["host"], conf.Host)
	if v := opts["port"]; v != "" {
		port, _ = strconv.Atoi(v)
	} else if conf.Port != 0 {
		port = int(conf.Port)
	}
	if user == "" {
		user = opts["user"]
	}
	database = firstNonEmpty(opts["database"], opts["db"])
	sslmode := opts["sslmode"]

	// Assemble libpq key=value pairs. Any unset key is intentionally omitted so
	// libpq's environment fallback (PGHOST, PGPORT, PGUSER, PGDATABASE, PGSSLMODE,
	// PGPASSWORD) still takes effect.
	var parts []string
	if host != "" {
		parts = append(parts, "host="+libpqQuote(host))
	}
	if port != 0 {
		parts = append(parts, "port="+strconv.Itoa(port))
	}
	if user != "" {
		parts = append(parts, "user="+libpqQuote(user))
	}
	if password != "" {
		parts = append(parts, "password="+libpqQuote(password))
	}
	if database != "" {
		parts = append(parts, "dbname="+libpqQuote(database))
	}
	if sslmode != "" {
		parts = append(parts, "sslmode="+libpqQuote(sslmode))
	}
	parts = append(parts, "application_name=mql")

	return strings.Join(parts, " "), host, port, database, user, nil
}

func parseDSNComponents(dsn string) (host string, port int, database, user string, err error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, parseErr := url.Parse(dsn)
		if parseErr != nil {
			return "", 0, "", "", parseErr
		}
		host = u.Hostname()
		if p := u.Port(); p != "" {
			port, _ = strconv.Atoi(p)
		}
		database = strings.TrimPrefix(u.Path, "/")
		if u.User != nil {
			user = u.User.Username()
		}
		return host, port, database, user, nil
	}

	// libpq key=value form. Best-effort parse; we only need the descriptive
	// bits for asset detection — the DSN itself is passed unchanged to pgx.
	for _, tok := range splitKV(dsn) {
		eq := strings.IndexByte(tok, '=')
		if eq < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(tok[:eq]))
		val := libpqUnquote(strings.TrimSpace(tok[eq+1:]))
		switch key {
		case "host":
			host = val
		case "port":
			port, _ = strconv.Atoi(val)
		case "dbname", "database":
			database = val
		case "user":
			user = val
		}
	}
	return host, port, database, user, nil
}

func mergePasswordIntoDSN(dsn, password string) (string, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", err
		}
		username := ""
		if u.User != nil {
			username = u.User.Username()
		}
		u.User = url.UserPassword(username, password)
		return u.String(), nil
	}
	// libpq key=value form. Strip any existing password= token before appending
	// the merged one so we don't end up with two — pgx/libpq would honor the
	// later one, but emitting duplicates is fragile and easy to misread.
	tokens := splitKV(dsn)
	kept := make([]string, 0, len(tokens)+1)
	for _, tok := range tokens {
		eq := strings.IndexByte(tok, '=')
		if eq < 0 {
			kept = append(kept, tok)
			continue
		}
		key := strings.ToLower(strings.TrimSpace(tok[:eq]))
		if key == "password" {
			continue
		}
		kept = append(kept, tok)
	}
	kept = append(kept, "password="+libpqQuote(password))
	return strings.Join(kept, " "), nil
}

func libpqQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " '\\") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('\'')
	for _, r := range s {
		if r == '\'' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('\'')
	return b.String()
}

func libpqUnquote(s string) string {
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		inner := s[1 : len(s)-1]
		var b strings.Builder
		b.Grow(len(inner))
		escape := false
		for _, r := range inner {
			if escape {
				b.WriteRune(r)
				escape = false
				continue
			}
			if r == '\\' {
				escape = true
				continue
			}
			b.WriteRune(r)
		}
		return b.String()
	}
	return s
}

// splitKV splits a libpq key=value DSN respecting single-quoted values.
func splitKV(s string) []string {
	var tokens []string
	var cur strings.Builder
	inQuote := false
	escape := false
	for _, r := range s {
		switch {
		case escape:
			cur.WriteRune(r)
			escape = false
		case r == '\\' && inQuote:
			cur.WriteRune(r)
			escape = true
		case r == '\'':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ' ' && !inQuote:
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// HostPort returns the connection's host:port pair for asset identification.
// Empty values fall back to libpq defaults ("localhost" and 5432).
func (c *PostgresConnection) HostPort() string {
	host := c.host
	if host == "" {
		host = "localhost"
	}
	port := c.port
	if port == 0 {
		port = 5432
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

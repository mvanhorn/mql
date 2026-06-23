// Copyright Mondoo, Inc. 2024, 2026
// SPDX-License-Identifier: BUSL-1.1

package postgresql

import (
	"reflect"
	"sort"
	"testing"
)

func TestParseConf_Basic(t *testing.T) {
	reader := func(path string) (string, error) {
		if path != "/etc/postgresql/16/main/postgresql.conf" {
			t.Fatalf("unexpected read of %q", path)
		}
		return `# comment
listen_addresses = 'localhost,10.0.0.5'
port = 5432
ssl = on
ssl_cert_file = '/etc/ssl/server.crt'   # inline comment
shared_buffers = 128MB
log_line_prefix = '%m [%p] '
`, nil
	}
	cfg, err := ParseConf("/etc/postgresql/16/main/postgresql.conf", reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"listen_addresses": "localhost,10.0.0.5",
		"port":             "5432",
		"ssl":              "on",
		"ssl_cert_file":    "/etc/ssl/server.crt",
		"shared_buffers":   "128MB",
		"log_line_prefix":  "%m [%p] ",
	}
	if !reflect.DeepEqual(cfg.Params, want) {
		t.Fatalf("Params = %v, want %v", cfg.Params, want)
	}
}

func TestParseConf_Includes(t *testing.T) {
	files := map[string]string{
		"/main.conf": `port = 5432
include 'tuning.conf'
include_if_exists 'missing.conf'
include_dir 'conf.d'
log_statement = 'ddl'
`,
		"/tuning.conf": `shared_buffers = 256MB
log_statement = 'all'
`,
		"/conf.d/01-replication.conf": `wal_level = replica
`,
		"/conf.d/README":            `not parsed`,
		"/conf.d/99-overrides.conf": `port = 6543`,
	}
	reader := func(path string) (string, error) {
		v, ok := files[path]
		if !ok {
			return "", &notFoundError{path}
		}
		return v, nil
	}
	dirLister := func(dir string) ([]string, error) {
		var out []string
		for p := range files {
			if len(p) > len(dir)+1 && p[:len(dir)+1] == dir+"/" && !contains(p[len(dir)+1:], "/") {
				out = append(out, p)
			}
		}
		sort.Strings(out)
		return out, nil
	}

	cfg, err := ParseConf("/main.conf", reader, dirLister)
	if err != nil {
		t.Fatal(err)
	}

	// Last-write-wins: log_statement ddl is overwritten by tuning.conf's "all",
	// then we keep going to conf.d entries; port should reflect the 99-overrides.
	if cfg.Params["log_statement"] != "ddl" {
		// Wait — the main.conf assigns log_statement = 'ddl' AFTER the include
		// so 'ddl' should win. Confirm.
		t.Errorf("log_statement = %q, want ddl", cfg.Params["log_statement"])
	}
	if cfg.Params["shared_buffers"] != "256MB" {
		t.Errorf("shared_buffers = %q, want 256MB", cfg.Params["shared_buffers"])
	}
	if cfg.Params["wal_level"] != "replica" {
		t.Errorf("wal_level = %q, want replica", cfg.Params["wal_level"])
	}
	if cfg.Params["port"] != "6543" {
		t.Errorf("port = %q, want 6543 (override from 99-overrides.conf)", cfg.Params["port"])
	}
	// README without .conf suffix must be skipped.
	if _, present := cfg.Params["not"]; present {
		t.Errorf("non-.conf file in include_dir was parsed")
	}
}

type notFoundError struct{ path string }

func (e *notFoundError) Error() string { return "not found: " + e.path }

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestParseConf_QuotedAndEscaped(t *testing.T) {
	reader := func(path string) (string, error) {
		return `application_name = 'it''s working'
search_path = '"$user", public'
`, nil
	}
	cfg, err := ParseConf("/etc/main.conf", reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Params["application_name"] != "it's working" {
		t.Errorf("application_name = %q", cfg.Params["application_name"])
	}
	if cfg.Params["search_path"] != `"$user", public` {
		t.Errorf("search_path = %q", cfg.Params["search_path"])
	}
}

func TestSplitListParam(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"localhost", []string{"localhost"}},
		{"localhost, 10.0.0.5", []string{"localhost", "10.0.0.5"}},
		{`"localhost",10.0.0.5`, []string{"localhost", "10.0.0.5"}},
		{"pg_stat_statements,auto_explain", []string{"pg_stat_statements", "auto_explain"}},
	}
	for _, tc := range tests {
		got := SplitListParam(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("SplitListParam(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestIsTruthy(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"on", true}, {"On", true}, {"ON", true},
		{"true", true}, {"yes", true}, {"1", true},
		{"off", false}, {"false", false}, {"no", false}, {"0", false},
		{"", false}, {"  on  ", true},
	} {
		if got := IsTruthy(tc.in); got != tc.want {
			t.Errorf("IsTruthy(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseHba_BasicRules(t *testing.T) {
	content := `# comment
local   all             postgres                                peer
local   all             all                                     scram-sha-256
host    all             all             127.0.0.1/32            md5
host    all             all             ::1/128                 trust
hostssl replication     repuser         10.0.0.0/8              cert clientcert=verify-full
host    db1,db2         "user one"      192.168.1.0  255.255.255.0  md5  ldapserver=ldap.example.com
`
	rules := ParseHba(content)
	if len(rules) != 6 {
		t.Fatalf("got %d rules, want 6", len(rules))
	}

	if rules[0].Type != "local" || rules[0].Database != "all" || rules[0].User != "postgres" || rules[0].AuthMethod != "peer" {
		t.Errorf("rule 0 wrong: %+v", rules[0])
	}

	if rules[2].Type != "host" || rules[2].Address != "127.0.0.1/32" || rules[2].AuthMethod != "md5" {
		t.Errorf("rule 2 wrong: %+v", rules[2])
	}

	if rules[4].Type != "hostssl" || rules[4].Database != "replication" || rules[4].User != "repuser" {
		t.Errorf("rule 4 wrong: %+v", rules[4])
	}
	if rules[4].Options["clientcert"] != "verify-full" {
		t.Errorf("rule 4 options = %v", rules[4].Options)
	}

	// Two-token address (IP netmask) form
	if rules[5].Address != "192.168.1.0 255.255.255.0" {
		t.Errorf("rule 5 address = %q", rules[5].Address)
	}
	if rules[5].AuthMethod != "md5" {
		t.Errorf("rule 5 auth = %q", rules[5].AuthMethod)
	}
	if rules[5].Options["ldapserver"] != "ldap.example.com" {
		t.Errorf("rule 5 options = %v", rules[5].Options)
	}
	if rules[5].User != "user one" {
		t.Errorf("rule 5 user (quoted) = %q", rules[5].User)
	}
}

func TestParseHba_SkipsMalformed(t *testing.T) {
	content := `local
host all all
not_a_real_type all all 127.0.0.1/32 md5
`
	rules := ParseHba(content)
	if len(rules) != 0 {
		t.Errorf("malformed lines produced %d rules: %+v", len(rules), rules)
	}
}

func TestParseIdent(t *testing.T) {
	content := `# map system-username postgres-username
mymap   /^(.*)@example\.com$      \1
mymap   alice                     postgres
peer    "/^(.*)$"                 \1
`
	maps := ParseIdent(content)
	if len(maps) != 3 {
		t.Fatalf("got %d, want 3: %+v", len(maps), maps)
	}
	if maps[0].MapName != "mymap" || maps[0].PgUsername != `\1` {
		t.Errorf("entry 0 = %+v", maps[0])
	}
	if maps[1].SystemUsername != "alice" || maps[1].PgUsername != "postgres" {
		t.Errorf("entry 1 = %+v", maps[1])
	}
}

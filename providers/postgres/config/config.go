// Copyright Mondoo, Inc. 2024, 2026
// SPDX-License-Identifier: BUSL-1.1

package config

import (
	"go.mondoo.com/mql/v13/providers-sdk/v1/plugin"
	"go.mondoo.com/mql/v13/providers/postgres/provider"
)

var Config = plugin.Provider{
	Name:            "postgres",
	ID:              "go.mondoo.com/mql/providers/postgres",
	Version:         "13.0.0",
	ConnectionTypes: []string{provider.DefaultConnectionType},
	Connectors: []plugin.Connector{
		{
			Name:    "postgres",
			Aliases: []string{"postgresql"},
			Use:     "postgres [--dsn URL] [--host HOST] [--port PORT] [--user USER] [--password PASS] [--database DB] [--sslmode MODE]",
			Short:   "a PostgreSQL server",
			Long: `Use the postgres provider to inspect a running PostgreSQL server.

You can either supply a single ` + "`--dsn`" + ` (a libpq URL like
` + "`postgresql://user:pass@host:5432/dbname?sslmode=require`" + `), or
provide the individual ` + "`--host`" + `, ` + "`--port`" + `, ` + "`--user`" + `,
` + "`--password`" + `, ` + "`--database`" + `, and ` + "`--sslmode`" + ` flags. When
neither is given, the standard libpq environment variables (PGHOST,
PGPORT, PGUSER, PGPASSWORD, PGDATABASE, PGSSLMODE) are honored.`,
			Discovery: []string{},
			Flags: []plugin.Flag{
				{
					Long:    "dsn",
					Type:    plugin.FlagType_String,
					Default: "",
					Desc:    "PostgreSQL connection string (e.g. postgresql://user:pass@host:5432/db?sslmode=require)",
				},
				{
					Long:    "host",
					Type:    plugin.FlagType_String,
					Default: "",
					Desc:    "Server host (defaults to PGHOST or localhost)",
				},
				{
					Long:    "port",
					Type:    plugin.FlagType_String,
					Default: "",
					Desc:    "Server port (defaults to PGPORT or 5432)",
				},
				{
					Long:    "user",
					Short:   "u",
					Type:    plugin.FlagType_String,
					Default: "",
					Desc:    "Login user (defaults to PGUSER or current OS user)",
				},
				{
					Long:    "password",
					Type:    plugin.FlagType_String,
					Default: "",
					Desc:    "Login password (defaults to PGPASSWORD)",
				},
				{
					Long:    "database",
					Short:   "d",
					Type:    plugin.FlagType_String,
					Default: "",
					Desc:    "Database to connect to (defaults to PGDATABASE or the user name)",
				},
				{
					Long:    "sslmode",
					Type:    plugin.FlagType_String,
					Default: "",
					Desc:    "SSL mode: disable, allow, prefer (default), require, verify-ca, verify-full",
				},
				{
					Long:    "ask-pass",
					Type:    plugin.FlagType_Bool,
					Default: "false",
					Desc:    "Prompt for the password interactively",
				},
			},
		},
	},
}

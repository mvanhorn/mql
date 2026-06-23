// Copyright Mondoo, Inc. 2024, 2026
// SPDX-License-Identifier: BUSL-1.1

package resources

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"go.mondoo.com/mql/v13/llx"
	"go.mondoo.com/mql/v13/providers/postgres/connection"
	"go.mondoo.com/mql/v13/types"
)

// id returns the cache key for the singleton postgresql resource. The asset
// identity already encodes the host/port/database, so a static string is
// stable and unique within the runtime.
func (r *mqlPostgresql) id() (string, error) {
	return "postgresql", nil
}

func (r *mqlPostgresql) db() *sql.DB {
	return r.MqlRuntime.Connection.(*connection.PostgresConnection).DB()
}

// queryCtx returns a context with a sensible per-query timeout. PostgreSQL
// metadata queries should complete in milliseconds; a hard ceiling here keeps
// a wedged server from blocking a whole scan.
func queryCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func (r *mqlPostgresql) version() (string, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	var v string
	if err := r.db().QueryRowContext(ctx, "SHOW server_version").Scan(&v); err != nil {
		return "", err
	}
	return v, nil
}

func (r *mqlPostgresql) versionNum() (int64, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	var s string
	if err := r.db().QueryRowContext(ctx, "SHOW server_version_num").Scan(&s); err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}

func (r *mqlPostgresql) currentDatabase() (string, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	var v string
	err := r.db().QueryRowContext(ctx, "SELECT current_database()").Scan(&v)
	return v, err
}

func (r *mqlPostgresql) currentUser() (string, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	var v string
	err := r.db().QueryRowContext(ctx, "SELECT current_user").Scan(&v)
	return v, err
}

func (r *mqlPostgresql) startedAt() (*time.Time, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	var v time.Time
	if err := r.db().QueryRowContext(ctx, "SELECT pg_postmaster_start_time()").Scan(&v); err != nil {
		return nil, err
	}
	return &v, nil
}

func (r *mqlPostgresql) inRecovery() (bool, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	var v bool
	err := r.db().QueryRowContext(ctx, "SELECT pg_is_in_recovery()").Scan(&v)
	return v, err
}

func (r *mqlPostgresql) databases() ([]any, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := r.db().QueryContext(ctx, `
SELECT
  d.oid::bigint,
  d.datname,
  COALESCE(pg_catalog.pg_get_userbyid(d.datdba), ''),
  pg_catalog.pg_encoding_to_char(d.encoding),
  d.datcollate,
  d.datctype,
  COALESCE(t.spcname, ''),
  d.datallowconn,
  d.datistemplate,
  d.datconnlimit,
  CASE WHEN d.datallowconn THEN pg_catalog.pg_database_size(d.datname) ELSE 0 END
FROM pg_catalog.pg_database d
LEFT JOIN pg_catalog.pg_tablespace t ON t.oid = d.dattablespace
ORDER BY d.datname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []any{}
	for rows.Next() {
		var (
			oid              int64
			name             string
			owner            string
			encoding         string
			collation        string
			ctype            string
			tablespace       string
			allowConnections bool
			isTemplate       bool
			connectionLimit  int64
			sizeBytes        int64
		)
		if err := rows.Scan(&oid, &name, &owner, &encoding, &collation, &ctype, &tablespace,
			&allowConnections, &isTemplate, &connectionLimit, &sizeBytes); err != nil {
			return nil, err
		}
		res, err := CreateResource(r.MqlRuntime, "postgresql.database", map[string]*llx.RawData{
			"__id":             llx.StringData("postgresql.database/" + name),
			"oid":              llx.IntData(oid),
			"name":             llx.StringData(name),
			"owner":            llx.StringData(owner),
			"encoding":         llx.StringData(encoding),
			"collation":        llx.StringData(collation),
			"ctype":            llx.StringData(ctype),
			"tablespace":       llx.StringData(tablespace),
			"allowConnections": llx.BoolData(allowConnections),
			"isTemplate":       llx.BoolData(isTemplate),
			"connectionLimit":  llx.IntData(connectionLimit),
			"sizeBytes":        llx.IntData(sizeBytes),
		})
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

func (r *mqlPostgresql) roles() ([]any, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := r.db().QueryContext(ctx, `
SELECT
  r.oid::bigint,
  r.rolname,
  r.rolsuper,
  r.rolinherit,
  r.rolcreatedb,
  r.rolcreaterole,
  r.rolcanlogin,
  r.rolreplication,
  r.rolbypassrls,
  r.rolconnlimit,
  r.rolvaliduntil,
  COALESCE(ARRAY(
    SELECT b.rolname
    FROM pg_catalog.pg_auth_members m
    JOIN pg_catalog.pg_roles b ON b.oid = m.roleid
    WHERE m.member = r.oid
    ORDER BY b.rolname
  ), ARRAY[]::name[])
FROM pg_catalog.pg_roles r
ORDER BY r.rolname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []any{}
	for rows.Next() {
		var (
			oid             int64
			name            string
			superuser       bool
			canBeInherited  bool
			createDb        bool
			createRole      bool
			login           bool
			replication     bool
			bypassRls       bool
			connectionLimit int64
			validUntil      sql.NullTime
			memberOf        []string
		)
		if err := rows.Scan(&oid, &name, &superuser, &canBeInherited, &createDb, &createRole, &login,
			&replication, &bypassRls, &connectionLimit, &validUntil, pqStringArray{&memberOf}); err != nil {
			return nil, err
		}
		args := map[string]*llx.RawData{
			"__id":            llx.StringData("postgresql.role/" + name),
			"oid":             llx.IntData(oid),
			"name":            llx.StringData(name),
			"superuser":       llx.BoolData(superuser),
			"canBeInherited":  llx.BoolData(canBeInherited),
			"createDb":        llx.BoolData(createDb),
			"createRole":      llx.BoolData(createRole),
			"login":           llx.BoolData(login),
			"replication":     llx.BoolData(replication),
			"bypassRls":       llx.BoolData(bypassRls),
			"connectionLimit": llx.IntData(connectionLimit),
			"memberOf":        llx.ArrayData(stringsToAny(memberOf), types.String),
		}
		if validUntil.Valid {
			args["validUntil"] = llx.TimeData(validUntil.Time)
		} else {
			args["validUntil"] = llx.NilData
		}
		res, err := CreateResource(r.MqlRuntime, "postgresql.role", args)
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

func (r *mqlPostgresql) extensions() ([]any, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := r.db().QueryContext(ctx, `
SELECT
  e.oid::bigint,
  e.extname,
  e.extversion,
  COALESCE(n.nspname, ''),
  COALESCE(pg_catalog.pg_get_userbyid(e.extowner), ''),
  e.extrelocatable
FROM pg_catalog.pg_extension e
LEFT JOIN pg_catalog.pg_namespace n ON n.oid = e.extnamespace
ORDER BY e.extname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []any{}
	for rows.Next() {
		var (
			oid         int64
			name        string
			version     string
			schema      string
			owner       string
			relocatable bool
		)
		if err := rows.Scan(&oid, &name, &version, &schema, &owner, &relocatable); err != nil {
			return nil, err
		}
		res, err := CreateResource(r.MqlRuntime, "postgresql.extension", map[string]*llx.RawData{
			"__id":        llx.StringData("postgresql.extension/" + name),
			"oid":         llx.IntData(oid),
			"name":        llx.StringData(name),
			"version":     llx.StringData(version),
			"schema":      llx.StringData(schema),
			"owner":       llx.StringData(owner),
			"relocatable": llx.BoolData(relocatable),
		})
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

func (r *mqlPostgresql) availableExtensions() ([]any, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := r.db().QueryContext(ctx, `
SELECT name, default_version, installed_version, COALESCE(comment, '')
FROM pg_catalog.pg_available_extensions
ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []any{}
	for rows.Next() {
		var (
			name             string
			defaultVersion   sql.NullString
			installedVersion sql.NullString
			comment          string
		)
		if err := rows.Scan(&name, &defaultVersion, &installedVersion, &comment); err != nil {
			return nil, err
		}
		res, err := CreateResource(r.MqlRuntime, "postgresql.availableExtension", map[string]*llx.RawData{
			"__id":             llx.StringData("postgresql.availableExtension/" + name),
			"name":             llx.StringData(name),
			"defaultVersion":   llx.StringData(defaultVersion.String),
			"installedVersion": llx.StringData(installedVersion.String),
			"comment":          llx.StringData(comment),
		})
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

func (r *mqlPostgresql) settings() ([]any, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := r.db().QueryContext(ctx, `
SELECT
  name,
  COALESCE(setting, ''),
  COALESCE(unit, ''),
  COALESCE(category, ''),
  COALESCE(short_desc, ''),
  COALESCE(extra_desc, ''),
  COALESCE(source, ''),
  COALESCE(sourcefile, ''),
  COALESCE(sourceline, 0),
  COALESCE(context, ''),
  COALESCE(vartype, ''),
  COALESCE(enumvals, ARRAY[]::text[]),
  COALESCE(min_val, ''),
  COALESCE(max_val, ''),
  COALESCE(boot_val, ''),
  COALESCE(reset_val, ''),
  pending_restart
FROM pg_catalog.pg_settings
ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []any{}
	for rows.Next() {
		var (
			name, value, unit, category, shortDesc, extraDesc, source string
			sourceFile                                                string
			sourceLine                                                int64
			context_, vartype                                         string
			enumValues                                                []string
			minVal, maxVal, bootVal, resetVal                         string
			pendingRestart                                            bool
		)
		if err := rows.Scan(&name, &value, &unit, &category, &shortDesc, &extraDesc, &source,
			&sourceFile, &sourceLine, &context_, &vartype, pqStringArray{&enumValues},
			&minVal, &maxVal, &bootVal, &resetVal, &pendingRestart); err != nil {
			return nil, err
		}
		res, err := CreateResource(r.MqlRuntime, "postgresql.setting", map[string]*llx.RawData{
			"__id":           llx.StringData("postgresql.setting/" + name),
			"name":           llx.StringData(name),
			"value":          llx.StringData(value),
			"unit":           llx.StringData(unit),
			"category":       llx.StringData(category),
			"shortDesc":      llx.StringData(shortDesc),
			"extraDesc":      llx.StringData(extraDesc),
			"source":         llx.StringData(source),
			"sourceFile":     llx.StringData(sourceFile),
			"sourceLine":     llx.IntData(sourceLine),
			"context":        llx.StringData(context_),
			"vartype":        llx.StringData(vartype),
			"enumValues":     llx.ArrayData(stringsToAny(enumValues), types.String),
			"minVal":         llx.StringData(minVal),
			"maxVal":         llx.StringData(maxVal),
			"bootVal":        llx.StringData(bootVal),
			"resetVal":       llx.StringData(resetVal),
			"pendingRestart": llx.BoolData(pendingRestart),
		})
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

func (r *mqlPostgresql) hbaRules() ([]any, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	// pg_hba_file_rules is available from PostgreSQL 10. Only superusers may
	// read it; non-superusers get an empty set and a single row containing an
	// `error` describing the permission denial — we surface that as-is so
	// audits can detect "no rule data available" explicitly.
	rows, err := r.db().QueryContext(ctx, `
SELECT
  COALESCE(line_number, 0),
  COALESCE(file_name, ''),
  COALESCE(type, ''),
  COALESCE(database, ARRAY[]::text[]),
  COALESCE(user_name, ARRAY[]::text[]),
  COALESCE(address, ''),
  COALESCE(netmask, ''),
  COALESCE(auth_method, ''),
  COALESCE(options, ARRAY[]::text[]),
  COALESCE(error, '')
FROM pg_catalog.pg_hba_file_rules
ORDER BY rule_number`)
	if err != nil {
		// Fall back without rule_number ordering on PG < 16 where the column
		// doesn't exist. The driver surfaces this as PgError code 42703
		// (undefined_column); anything else is a real failure.
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42703" {
			return nil, err
		}
		rows, err = r.db().QueryContext(ctx, `
SELECT
  COALESCE(line_number, 0),
  COALESCE(file_name, ''),
  COALESCE(type, ''),
  COALESCE(database, ARRAY[]::text[]),
  COALESCE(user_name, ARRAY[]::text[]),
  COALESCE(address, ''),
  COALESCE(netmask, ''),
  COALESCE(auth_method, ''),
  COALESCE(options, ARRAY[]::text[]),
  COALESCE(error, '')
FROM pg_catalog.pg_hba_file_rules
ORDER BY line_number`)
		if err != nil {
			return nil, err
		}
	}
	defer rows.Close()

	out := []any{}
	idx := 0
	for rows.Next() {
		var (
			lineNumber int64
			fileName   string
			typ        string
			databases  []string
			userNames  []string
			address    string
			netmask    string
			authMethod string
			options    []string
			errStr     string
		)
		if err := rows.Scan(&lineNumber, &fileName, &typ, pqStringArray{&databases},
			pqStringArray{&userNames}, &address, &netmask, &authMethod,
			pqStringArray{&options}, &errStr); err != nil {
			return nil, err
		}
		idx++
		res, err := CreateResource(r.MqlRuntime, "postgresql.hbaRule", map[string]*llx.RawData{
			"__id":       llx.StringData(file_lineID("postgresql.hbaRule", fileName, lineNumber, idx)),
			"lineNumber": llx.IntData(lineNumber),
			"fileName":   llx.StringData(fileName),
			"type":       llx.StringData(typ),
			"databases":  llx.ArrayData(stringsToAny(databases), types.String),
			"userNames":  llx.ArrayData(stringsToAny(userNames), types.String),
			"address":    llx.StringData(address),
			"netmask":    llx.StringData(netmask),
			"authMethod": llx.StringData(authMethod),
			"options":    llx.MapData(optionsListToMap(options), types.String),
			"error":      llx.StringData(errStr),
		})
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

func (r *mqlPostgresql) identRules() ([]any, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := r.db().QueryContext(ctx, `
SELECT
  COALESCE(line_number, 0),
  COALESCE(file_name, ''),
  COALESCE(map_name, ''),
  COALESCE(sys_name, ''),
  COALESCE(pg_username, ''),
  COALESCE(error, '')
FROM pg_catalog.pg_ident_file_mappings
ORDER BY line_number`)
	if err != nil {
		// View available from PG 15+; older servers don't have it.
		return []any{}, nil
	}
	defer rows.Close()

	out := []any{}
	idx := 0
	for rows.Next() {
		var (
			lineNumber int64
			fileName   string
			mapName    string
			sysName    string
			pgUsername string
			errStr     string
		)
		if err := rows.Scan(&lineNumber, &fileName, &mapName, &sysName, &pgUsername, &errStr); err != nil {
			return nil, err
		}
		idx++
		res, err := CreateResource(r.MqlRuntime, "postgresql.identRule", map[string]*llx.RawData{
			"__id":           llx.StringData(file_lineID("postgresql.identRule", fileName, lineNumber, idx)),
			"lineNumber":     llx.IntData(lineNumber),
			"fileName":       llx.StringData(fileName),
			"mapName":        llx.StringData(mapName),
			"systemUsername": llx.StringData(sysName),
			"pgUsername":     llx.StringData(pgUsername),
			"error":          llx.StringData(errStr),
		})
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

func (r *mqlPostgresql) replicationSlots() ([]any, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	// wal_status was added in PG 13 and two_phase in PG 14. Try the full
	// query first, then fall back to progressively older column sets on
	// undefined_column (PgError code 42703).
	type slotCols int
	const (
		slotColsFull    slotCols = iota // wal_status + two_phase
		slotColsNoPhase                 // wal_status only (PG 13)
		slotColsMinimal                 // neither column (PG < 13)
	)

	queries := []struct {
		cols slotCols
		sql  string
	}{
		{slotColsFull, `
SELECT
  slot_name,
  COALESCE(plugin, ''),
  COALESCE(slot_type, ''),
  COALESCE(database, ''),
  active,
  COALESCE(active_pid, 0),
  temporary,
  COALESCE(restart_lsn::text, ''),
  COALESCE(confirmed_flush_lsn::text, ''),
  COALESCE(wal_status::text, ''),
  COALESCE(two_phase, false)
FROM pg_catalog.pg_replication_slots
ORDER BY slot_name`},
		{slotColsNoPhase, `
SELECT
  slot_name,
  COALESCE(plugin, ''),
  COALESCE(slot_type, ''),
  COALESCE(database, ''),
  active,
  COALESCE(active_pid, 0),
  temporary,
  COALESCE(restart_lsn::text, ''),
  COALESCE(confirmed_flush_lsn::text, ''),
  COALESCE(wal_status::text, '')
FROM pg_catalog.pg_replication_slots
ORDER BY slot_name`},
		{slotColsMinimal, `
SELECT
  slot_name,
  COALESCE(plugin, ''),
  COALESCE(slot_type, ''),
  COALESCE(database, ''),
  active,
  COALESCE(active_pid, 0),
  temporary,
  COALESCE(restart_lsn::text, ''),
  COALESCE(confirmed_flush_lsn::text, '')
FROM pg_catalog.pg_replication_slots
ORDER BY slot_name`},
	}

	var (
		rows     *sql.Rows
		err      error
		colsUsed slotCols
	)
	for _, q := range queries {
		rows, err = r.db().QueryContext(ctx, q.sql)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "42703" {
				// undefined_column — try the next fallback
				continue
			}
			return nil, err
		}
		colsUsed = q.cols
		break
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []any{}
	for rows.Next() {
		var (
			slotName, pluginName, slotType, database string
			active                                   bool
			activePid                                int64
			temporary                                bool
			restartLsn, confirmedFlushLsn            string
			walStatus                                string
			twoPhase                                 bool
		)
		switch colsUsed {
		case slotColsFull:
			if err := rows.Scan(&slotName, &pluginName, &slotType, &database, &active, &activePid,
				&temporary, &restartLsn, &confirmedFlushLsn, &walStatus, &twoPhase); err != nil {
				return nil, err
			}
		case slotColsNoPhase:
			if err := rows.Scan(&slotName, &pluginName, &slotType, &database, &active, &activePid,
				&temporary, &restartLsn, &confirmedFlushLsn, &walStatus); err != nil {
				return nil, err
			}
		default: // slotColsMinimal
			if err := rows.Scan(&slotName, &pluginName, &slotType, &database, &active, &activePid,
				&temporary, &restartLsn, &confirmedFlushLsn); err != nil {
				return nil, err
			}
		}
		res, err := CreateResource(r.MqlRuntime, "postgresql.replicationSlot", map[string]*llx.RawData{
			"__id":              llx.StringData("postgresql.replicationSlot/" + slotName),
			"slotName":          llx.StringData(slotName),
			"plugin":            llx.StringData(pluginName),
			"slotType":          llx.StringData(slotType),
			"database":          llx.StringData(database),
			"active":            llx.BoolData(active),
			"activePid":         llx.IntData(activePid),
			"temporary":         llx.BoolData(temporary),
			"restartLsn":        llx.StringData(restartLsn),
			"confirmedFlushLsn": llx.StringData(confirmedFlushLsn),
			"walStatus":         llx.StringData(walStatus),
			"twoPhase":          llx.BoolData(twoPhase),
		})
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

func (r *mqlPostgresql) publications() ([]any, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := r.db().QueryContext(ctx, `
SELECT
  p.oid::bigint,
  p.pubname,
  COALESCE(pg_catalog.pg_get_userbyid(p.pubowner), ''),
  p.puballtables,
  p.pubinsert,
  p.pubupdate,
  p.pubdelete,
  p.pubtruncate
FROM pg_catalog.pg_publication p
ORDER BY p.pubname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []any{}
	for rows.Next() {
		var (
			oid                                                 int64
			name, owner                                         string
			allTables, insertOn, updateOn, deleteOn, truncateOn bool
		)
		if err := rows.Scan(&oid, &name, &owner, &allTables, &insertOn, &updateOn, &deleteOn, &truncateOn); err != nil {
			return nil, err
		}
		res, err := CreateResource(r.MqlRuntime, "postgresql.publication", map[string]*llx.RawData{
			"__id":            llx.StringData("postgresql.publication/" + name),
			"oid":             llx.IntData(oid),
			"name":            llx.StringData(name),
			"owner":           llx.StringData(owner),
			"allTables":       llx.BoolData(allTables),
			"insertEnabled":   llx.BoolData(insertOn),
			"updateEnabled":   llx.BoolData(updateOn),
			"deleteEnabled":   llx.BoolData(deleteOn),
			"truncateEnabled": llx.BoolData(truncateOn),
		})
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

func (r *mqlPostgresql) subscriptions() ([]any, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	// pg_subscription's `subconninfo` is only visible to superusers; ordinary
	// roles get an empty string. The view itself works for all roles though.
	rows, err := r.db().QueryContext(ctx, `
SELECT
  s.oid::bigint,
  s.subname,
  COALESCE(pg_catalog.pg_get_userbyid(s.subowner), ''),
  s.subenabled,
  COALESCE(s.subconninfo, ''),
  COALESCE(s.subslotname, ''),
  COALESCE(s.subsynccommit, ''),
  COALESCE(s.subpublications, ARRAY[]::text[])
FROM pg_catalog.pg_subscription s
ORDER BY s.subname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []any{}
	for rows.Next() {
		var (
			oid                                   int64
			name, owner                           string
			enabled                               bool
			connInfo, slotName, synchronousCommit string
			publicationNames                      []string
		)
		if err := rows.Scan(&oid, &name, &owner, &enabled, &connInfo, &slotName, &synchronousCommit,
			pqStringArray{&publicationNames}); err != nil {
			return nil, err
		}
		res, err := CreateResource(r.MqlRuntime, "postgresql.subscription", map[string]*llx.RawData{
			"__id":              llx.StringData("postgresql.subscription/" + name),
			"oid":               llx.IntData(oid),
			"name":              llx.StringData(name),
			"owner":             llx.StringData(owner),
			"enabled":           llx.BoolData(enabled),
			"connInfo":          llx.StringData(redactConnInfo(connInfo)),
			"slotName":          llx.StringData(slotName),
			"synchronousCommit": llx.StringData(synchronousCommit),
			"publicationNames":  llx.ArrayData(stringsToAny(publicationNames), types.String),
		})
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

func (r *mqlPostgresql) tablespaces() ([]any, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := r.db().QueryContext(ctx, `
SELECT
  t.oid::bigint,
  t.spcname,
  COALESCE(pg_catalog.pg_get_userbyid(t.spcowner), ''),
  COALESCE(pg_catalog.pg_tablespace_location(t.oid), '')
FROM pg_catalog.pg_tablespace t
ORDER BY t.spcname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []any{}
	for rows.Next() {
		var (
			oid                   int64
			name, owner, location string
		)
		if err := rows.Scan(&oid, &name, &owner, &location); err != nil {
			return nil, err
		}
		res, err := CreateResource(r.MqlRuntime, "postgresql.tablespace", map[string]*llx.RawData{
			"__id":     llx.StringData("postgresql.tablespace/" + name),
			"oid":      llx.IntData(oid),
			"name":     llx.StringData(name),
			"owner":    llx.StringData(owner),
			"location": llx.StringData(location),
		})
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

func (r *mqlPostgresql) languages() ([]any, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := r.db().QueryContext(ctx, `
SELECT
  l.oid::bigint,
  l.lanname,
  COALESCE(pg_catalog.pg_get_userbyid(l.lanowner), ''),
  l.lanpltrusted
FROM pg_catalog.pg_language l
ORDER BY l.lanname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []any{}
	for rows.Next() {
		var (
			oid         int64
			name, owner string
			trusted     bool
		)
		if err := rows.Scan(&oid, &name, &owner, &trusted); err != nil {
			return nil, err
		}
		res, err := CreateResource(r.MqlRuntime, "postgresql.language", map[string]*llx.RawData{
			"__id":    llx.StringData("postgresql.language/" + name),
			"oid":     llx.IntData(oid),
			"name":    llx.StringData(name),
			"owner":   llx.StringData(owner),
			"trusted": llx.BoolData(trusted),
		})
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

// id() methods for child resources. CreateResource already passes "__id" in
// args, but the generated code requires an id() receiver to exist.
func (r *mqlPostgresqlDatabase) id() (string, error)           { return r.__id, nil }
func (r *mqlPostgresqlRole) id() (string, error)               { return r.__id, nil }
func (r *mqlPostgresqlExtension) id() (string, error)          { return r.__id, nil }
func (r *mqlPostgresqlAvailableExtension) id() (string, error) { return r.__id, nil }
func (r *mqlPostgresqlSetting) id() (string, error)            { return r.__id, nil }
func (r *mqlPostgresqlHbaRule) id() (string, error)            { return r.__id, nil }
func (r *mqlPostgresqlIdentRule) id() (string, error)          { return r.__id, nil }
func (r *mqlPostgresqlReplicationSlot) id() (string, error)    { return r.__id, nil }
func (r *mqlPostgresqlPublication) id() (string, error)        { return r.__id, nil }
func (r *mqlPostgresqlSubscription) id() (string, error)       { return r.__id, nil }
func (r *mqlPostgresqlTablespace) id() (string, error)         { return r.__id, nil }
func (r *mqlPostgresqlLanguage) id() (string, error)           { return r.__id, nil }

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func stringsToAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// file_lineID builds a stable __id for rules sourced from a file when the file
// name is known. Falls back to a positional index when the source file is
// blank (e.g. an error row from pg_hba_file_rules with no line context).
func file_lineID(resource, file string, line int64, idx int) string {
	if file == "" {
		return resource + "/idx/" + strconv.Itoa(idx)
	}
	return resource + "/" + file + ":" + strconv.Itoa(int(line)) + "/" + strconv.Itoa(idx)
}

// optionsListToMap turns the libpq-style ["key=value", "flag"] options array
// into a map[string]any with string values. Bare flags become empty-string
// entries so audits can still assert key presence.
func optionsListToMap(in []string) map[string]any {
	out := make(map[string]any, len(in))
	for _, kv := range in {
		eq := -1
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				eq = i
				break
			}
		}
		if eq < 0 {
			out[kv] = ""
		} else {
			out[kv[:eq]] = kv[eq+1:]
		}
	}
	return out
}

// redactConnInfo removes the password value from a libpq connection string so
// that plaintext credentials are not stored in scan results. Both the
// single-quoted form (password='secret') and the bare-word form
// (password=secret) are replaced with password=***.
func redactConnInfo(connInfo string) string {
	out := connInfo
	// Single-quoted form: password='...'
	for {
		idx := strings.Index(out, "password='")
		if idx < 0 {
			break
		}
		start := idx + len("password='")
		end := strings.Index(out[start:], "'")
		if end < 0 {
			// Unclosed quote — redact to end of string.
			out = out[:idx] + "password=***"
			break
		}
		out = out[:idx] + "password=***" + out[start+end+1:]
	}
	// Bare-word form: password=... (value ends at next space or end of string)
	for {
		idx := strings.Index(out, "password=")
		if idx < 0 {
			break
		}
		// Skip if already redacted.
		if strings.HasPrefix(out[idx:], "password=***") {
			break
		}
		start := idx + len("password=")
		end := strings.IndexByte(out[start:], ' ')
		if end < 0 {
			out = out[:idx] + "password=***"
			break
		}
		out = out[:idx] + "password=***" + out[start+end:]
	}
	return out
}

// pqStringArray is a minimal text-format implementation of sql.Scanner for
// PostgreSQL text arrays. The pgx stdlib driver returns text-protocol arrays
// as []byte / string by default; rather than pull in pgx-native query
// machinery we parse the array form here.
type pqStringArray struct {
	out *[]string
}

func (p pqStringArray) Scan(src any) error {
	if src == nil {
		*p.out = nil
		return nil
	}
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return errors.New("pqStringArray: unsupported source type")
	}
	*p.out = parsePqArray(s)
	return nil
}

// parsePqArray parses PostgreSQL's text array format: `{a,b,c}`, with quoted
// elements `"foo bar"`, escaped quotes `\"`, and `NULL` literals. Returns nil
// for the array literal `{}` (empty array) — callers treating nil as
// "no value" should compare length instead.
func parsePqArray(s string) []string {
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil
	}
	inner := s[1 : len(s)-1]
	if inner == "" {
		return []string{}
	}

	var out []string
	var cur []byte
	quoted := false
	escape := false
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if escape {
			cur = append(cur, c)
			escape = false
			continue
		}
		switch {
		case c == '\\':
			escape = true
		case c == '"':
			quoted = !quoted
		case c == ',' && !quoted:
			out = append(out, string(cur))
			cur = cur[:0:0]
		default:
			cur = append(cur, c)
		}
	}
	out = append(out, string(cur))
	// Unquoted `NULL` → empty string.
	for i, v := range out {
		if v == "NULL" {
			out[i] = ""
		}
	}
	return out
}

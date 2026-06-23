// Copyright Mondoo, Inc. 2024, 2026
// SPDX-License-Identifier: BUSL-1.1

package provider

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"go.mondoo.com/mql/v13/llx"
	"go.mondoo.com/mql/v13/providers-sdk/v1/inventory"
	"go.mondoo.com/mql/v13/providers-sdk/v1/plugin"
	"go.mondoo.com/mql/v13/providers-sdk/v1/upstream"
	"go.mondoo.com/mql/v13/providers-sdk/v1/vault"
	"go.mondoo.com/mql/v13/providers/postgres/connection"
	"go.mondoo.com/mql/v13/providers/postgres/resources"
)

const (
	DefaultConnectionType = "postgres"
)

type Service struct {
	*plugin.Service
}

func Init() *Service {
	return &Service{
		Service: plugin.NewService(),
	}
}

func (s *Service) ParseCLI(req *plugin.ParseCLIReq) (*plugin.ParseCLIRes, error) {
	flags := req.Flags
	if flags == nil {
		flags = map[string]*llx.Primitive{}
	}

	conf := &inventory.Config{
		Type:    req.Connector,
		Options: map[string]string{},
	}

	stringFlag := func(name string) string {
		if v, ok := flags[name]; ok && len(v.Value) != 0 {
			return string(v.Value)
		}
		return ""
	}

	if v := stringFlag("dsn"); v != "" {
		conf.Options["dsn"] = v
	}
	if v := stringFlag("host"); v != "" {
		conf.Options["host"] = v
		conf.Host = v
	}
	if v := stringFlag("port"); v != "" {
		conf.Options["port"] = v
		if p, err := strconv.Atoi(v); err == nil {
			conf.Port = int32(p)
		}
	}
	if v := stringFlag("database"); v != "" {
		conf.Options["database"] = v
	}
	if v := stringFlag("sslmode"); v != "" {
		conf.Options["sslmode"] = v
	}

	user := stringFlag("user")
	password := stringFlag("password")
	if user != "" {
		conf.Options["user"] = user
	}
	if password != "" {
		conf.Credentials = append(conf.Credentials, vault.NewPasswordCredential(user, password))
	}

	asset := inventory.Asset{
		Connections: []*inventory.Config{conf},
	}
	return &plugin.ParseCLIRes{Asset: &asset}, nil
}

func (s *Service) Connect(req *plugin.ConnectReq, callback plugin.ProviderCallback) (*plugin.ConnectRes, error) {
	if req == nil || req.Asset == nil {
		return nil, errors.New("no connection data provided")
	}

	conn, err := s.connect(req, callback)
	if err != nil {
		return nil, err
	}

	if req.Asset.Platform == nil {
		if err := s.detect(req.Asset, conn); err != nil {
			return nil, err
		}
	}

	return &plugin.ConnectRes{
		Id:        conn.ID(),
		Name:      conn.Name(),
		Asset:     req.Asset,
		Inventory: nil,
	}, nil
}

func (s *Service) connect(req *plugin.ConnectReq, callback plugin.ProviderCallback) (*connection.PostgresConnection, error) {
	if len(req.Asset.Connections) == 0 {
		return nil, errors.New("no connection options for asset")
	}

	asset := req.Asset
	conf := asset.Connections[0]
	runtime, err := s.AddRuntime(conf, func(connId uint32) (*plugin.Runtime, error) {
		conn, err := connection.NewPostgresConnection(connId, asset, conf)
		if err != nil {
			return nil, err
		}

		var up *upstream.UpstreamClient
		if req.Upstream != nil && !req.Upstream.Incognito {
			up, err = req.Upstream.InitClient(context.Background())
			if err != nil {
				return nil, err
			}
		}

		asset.Connections[0].Id = conn.ID()
		return plugin.NewRuntime(
			conn,
			callback,
			req.HasRecording,
			resources.CreateResource,
			resources.NewResource,
			resources.GetData,
			resources.SetData,
			up), nil
	})
	if err != nil {
		return nil, err
	}

	return runtime.Connection.(*connection.PostgresConnection), nil
}

func (s *Service) detect(asset *inventory.Asset, conn *connection.PostgresConnection) error {
	hostPort := conn.HostPort()
	dbName := conn.Database()

	name := "postgres://" + hostPort
	if dbName != "" {
		name += "/" + dbName
	}

	asset.Name = name
	if asset.Id == "" {
		asset.Id = name
	}

	version, fullVersion := serverVersion(conn)

	asset.Platform = &inventory.Platform{
		Name:    "postgresql",
		Family:  []string{"postgresql"},
		Kind:    "api",
		Runtime: "postgresql",
		Title:   "PostgreSQL",
		Version: version,
	}

	// platform IDs are required so the upstream platform knows how to identify
	// the asset. We use a stable, descriptive URN derived from host:port + db.
	platformID := "//platformid.api.mondoo.app/runtime/postgresql/" + strings.ReplaceAll(hostPort, ":", "/")
	if dbName != "" {
		platformID += "/db/" + dbName
	}
	asset.PlatformIds = []string{platformID}

	// Surface the full version string as an asset label for visibility, when
	// available — useful when the platform table shows just the bare version.
	if fullVersion != "" && asset.Labels == nil {
		asset.Labels = map[string]string{"postgres.version": fullVersion}
	} else if fullVersion != "" {
		asset.Labels["postgres.version"] = fullVersion
	}
	return nil
}

// serverVersion returns (short, full) for the connected server. Short is just
// the numeric version (e.g. "16.4"); full is the verbose version() banner
// (e.g. "PostgreSQL 16.4 on x86_64-pc-linux-gnu, compiled by ...").
func serverVersion(conn *connection.PostgresConnection) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var full string
	if err := conn.DB().QueryRowContext(ctx, "SELECT version()").Scan(&full); err == nil {
		// "PostgreSQL 16.4 on x86_64-pc-linux-gnu, compiled by ..."
		short := full
		if strings.HasPrefix(short, "PostgreSQL ") {
			short = strings.TrimPrefix(short, "PostgreSQL ")
			if idx := strings.IndexByte(short, ' '); idx >= 0 {
				short = short[:idx]
			}
		}
		return short, full
	}
	return "", ""
}

func (s *Service) MockConnect(req *plugin.ConnectReq, callback plugin.ProviderCallback) (*plugin.ConnectRes, error) {
	return nil, errors.New("mock connect not yet implemented")
}

// Package backend implements the Harbor secrets engine.
package backend

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"

	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/harbor"
)

// Version is set by main from the build-time version.
var Version = "dev"

const (
	// walRollbackMinAge is how old a WAL entry must be before rollback deletes
	// its robot; a normal creds request never takes anywhere near this long.
	walRollbackMinAge = 10 * time.Minute

	// envWALRollbackMinAge overrides walRollbackMinAge (Go duration syntax).
	// Intended for integration tests; set via `vault plugin register -env`.
	envWALRollbackMinAge = "VAULT_PLUGIN_SECRETS_HARBOR_WAL_ROLLBACK_MIN_AGE"
)

// Factory returns a new backend as logical.Backend.
func Factory(ctx context.Context, conf *logical.BackendConfig) (logical.Backend, error) {
	b := newBackend()
	if err := b.Setup(ctx, conf); err != nil {
		return nil, err
	}
	return b, nil
}

type harborBackend struct {
	*framework.Backend

	clientMu sync.RWMutex
	client   *harbor.Client
	// clientCfg is the config the cached client was built from; used to detect
	// staleness cheaply without hashing.
	clientCfg *config
}

func newBackend() *harborBackend {
	b := &harborBackend{}
	b.Backend = &framework.Backend{
		Help:           strings.TrimSpace(backendHelp),
		BackendType:    logical.TypeLogical,
		RunningVersion: normalizeVersion(Version),
		PathsSpecial: &logical.Paths{
			SealWrapStorage: []string{configStoragePath},
		},
		Paths: framework.PathAppend(
			[]*framework.Path{
				b.pathConfig(),
				b.pathConfigRotateRoot(),
				b.pathRolesList(),
				b.pathRoles(),
				b.pathCreds(),
			},
		),
		Secrets: []*framework.Secret{
			b.secretRobot(),
		},
		WALRollback:       b.walRollback,
		WALRollbackMinAge: walRollbackMinAgeFromEnv(),
		Invalidate:        b.invalidate,
	}
	harbor.UserAgent = "vault-plugin-secrets-harbor/" + Version
	return b
}

func walRollbackMinAgeFromEnv() time.Duration {
	if v := os.Getenv(envWALRollbackMinAge); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return walRollbackMinAge
}

// invalidate is called by Vault when storage under key changes on another node.
func (b *harborBackend) invalidate(_ context.Context, key string) {
	if key == configStoragePath {
		b.resetClient()
	}
}

func (b *harborBackend) resetClient() {
	b.clientMu.Lock()
	b.client = nil
	b.clientCfg = nil
	b.clientMu.Unlock()
}

// getClient returns a Harbor client for the stored configuration, building and
// caching it on first use. It settles a pending root rotation if one exists.
func (b *harborBackend) getClient(ctx context.Context, s logical.Storage) (*harbor.Client, *config, error) {
	b.clientMu.RLock()
	if b.client != nil && b.clientCfg != nil {
		c, cfg := b.client, b.clientCfg
		b.clientMu.RUnlock()
		return c, cfg, nil
	}
	b.clientMu.RUnlock()

	b.clientMu.Lock()
	defer b.clientMu.Unlock()
	if b.client != nil && b.clientCfg != nil {
		return b.client, b.clientCfg, nil
	}

	cfg, err := getConfig(ctx, s)
	if err != nil {
		return nil, nil, err
	}
	if cfg == nil {
		return nil, nil, errNotConfigured
	}
	if cfg.PendingPassword != "" {
		if err := b.settlePendingRotation(ctx, s, cfg); err != nil {
			return nil, nil, err
		}
	}
	c, err := harbor.New(cfg.clientConfig())
	if err != nil {
		return nil, nil, err
	}
	b.client = c
	b.clientCfg = cfg
	return c, cfg, nil
}

const backendHelp = `
The Harbor secrets engine dynamically generates Harbor robot accounts based on
configured roles. Each credential is bound to a Vault lease: renewing the lease
extends the robot's expiry in Harbor and revoking it deletes the robot.

Configure the engine with 'config' (Harbor URL plus a user or robot credential
that may manage robots), define roles under 'roles/<name>' with the robot level,
Harbor permissions and TTLs, and read 'creds/<name>' to obtain credentials.
`

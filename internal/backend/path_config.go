package backend

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"

	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/harbor"
)

const (
	configStoragePath = "config"

	authTypeUser  = "user"
	authTypeRobot = "robot"

	defaultTimeout = 30 * time.Second
)

// config is the stored backend configuration.
type config struct {
	URL                string        `json:"url"`
	Username           string        `json:"username"`
	Password           string        `json:"password"`
	AuthType           string        `json:"auth_type"`
	CACert             string        `json:"ca_cert"`
	InsecureSkipVerify bool          `json:"insecure_skip_verify"`
	Timeout            time.Duration `json:"timeout"`
	RobotNamePrefix    string        `json:"robot_name_prefix"`
	LastRotated        time.Time     `json:"last_rotated"`

	// PendingPassword holds a freshly generated root credential that has been
	// persisted but whose activation in Harbor is not yet confirmed. See
	// rotateRoot for the two-phase protocol.
	PendingPassword string `json:"pending_password,omitempty"`
}

func (c *config) clientConfig() harbor.Config {
	return harbor.Config{
		URL:                c.URL,
		Username:           c.Username,
		Password:           c.Password,
		CACertPEM:          c.CACert,
		InsecureSkipVerify: c.InsecureSkipVerify,
		Timeout:            c.Timeout,
	}
}

func getConfig(ctx context.Context, s logical.Storage) (*config, error) {
	entry, err := s.Get(ctx, configStoragePath)
	if err != nil {
		return nil, fmt.Errorf("reading configuration: %w", err)
	}
	if entry == nil {
		return nil, nil
	}
	cfg := &config{}
	if err := entry.DecodeJSON(cfg); err != nil {
		return nil, fmt.Errorf("decoding configuration: %w", err)
	}
	return cfg, nil
}

func putConfig(ctx context.Context, s logical.Storage, cfg *config) error {
	entry, err := logical.StorageEntryJSON(configStoragePath, cfg)
	if err != nil {
		return fmt.Errorf("encoding configuration: %w", err)
	}
	if err := s.Put(ctx, entry); err != nil {
		return fmt.Errorf("storing configuration: %w", err)
	}
	return nil
}

func (b *harborBackend) pathConfig() *framework.Path {
	return &framework.Path{
		Pattern: "config",
		DisplayAttrs: &framework.DisplayAttributes{
			OperationPrefix: "harbor",
		},
		Fields: map[string]*framework.FieldSchema{
			"url": {
				Type:        framework.TypeString,
				Description: "Harbor base URL, e.g. https://harbor.example.com.",
				Required:    true,
			},
			"username": {
				Type:        framework.TypeString,
				Description: "Harbor user name, or the full robot account name (e.g. robot$vault-issuer) when auth_type=robot.",
				Required:    true,
			},
			"password": {
				Type:         framework.TypeString,
				Description:  "Password of the Harbor user, or the robot secret when auth_type=robot.",
				Required:     true,
				DisplayAttrs: &framework.DisplayAttributes{Sensitive: true},
			},
			"auth_type": {
				Type:          framework.TypeString,
				Description:   `Kind of root credential: "user" (Harbor local user) or "robot" (Harbor robot account). Default "user".`,
				Default:       authTypeUser,
				AllowedValues: []any{authTypeUser, authTypeRobot},
			},
			"ca_cert": {
				Type:        framework.TypeString,
				Description: "PEM-encoded CA certificate bundle used to verify Harbor's TLS certificate.",
			},
			"insecure_skip_verify": {
				Type:        framework.TypeBool,
				Description: "Skip TLS certificate verification. Not recommended.",
				Default:     false,
			},
			"timeout": {
				Type:        framework.TypeDurationSecond,
				Description: "Per-request timeout for Harbor API calls. Default 30s.",
				Default:     30,
			},
			"robot_name_prefix": {
				Type:        framework.TypeString,
				Description: `Prefix for generated robot names (before Harbor's own "robot$" prefix). Default "vault".`,
				Default:     defaultRobotNamePrefix,
			},
			"verify_connection": {
				Type:        framework.TypeBool,
				Description: "Verify the credential against Harbor before storing. Default true.",
				Default:     true,
			},
		},
		ExistenceCheck: b.configExistenceCheck,
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ReadOperation: &framework.PathOperation{
				Callback: b.pathConfigRead,
				DisplayAttrs: &framework.DisplayAttributes{
					OperationSuffix: "configuration",
				},
			},
			logical.CreateOperation: &framework.PathOperation{
				Callback: b.pathConfigWrite,
				DisplayAttrs: &framework.DisplayAttributes{
					OperationVerb: "configure",
				},
			},
			logical.UpdateOperation: &framework.PathOperation{
				Callback: b.pathConfigWrite,
				DisplayAttrs: &framework.DisplayAttributes{
					OperationVerb: "configure",
				},
			},
			logical.DeleteOperation: &framework.PathOperation{
				Callback: b.pathConfigDelete,
				DisplayAttrs: &framework.DisplayAttributes{
					OperationSuffix: "configuration",
				},
			},
		},
		HelpSynopsis:    "Configure the Harbor endpoint and root credential.",
		HelpDescription: pathConfigHelp,
	}
}

func (b *harborBackend) configExistenceCheck(ctx context.Context, req *logical.Request, _ *framework.FieldData) (bool, error) {
	cfg, err := getConfig(ctx, req.Storage)
	if err != nil {
		return false, err
	}
	return cfg != nil, nil
}

func (b *harborBackend) pathConfigRead(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	cfg, err := getConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}
	data := map[string]any{
		"url":                  cfg.URL,
		"username":             cfg.Username,
		"auth_type":            cfg.AuthType,
		"insecure_skip_verify": cfg.InsecureSkipVerify,
		"timeout":              int64(cfg.Timeout.Seconds()),
		"ca_cert_set":          cfg.CACert != "",
		"robot_name_prefix":    cfg.RobotNamePrefix,
	}
	if !cfg.LastRotated.IsZero() {
		data["last_rotated"] = cfg.LastRotated.UTC().Format(time.RFC3339)
	}
	return &logical.Response{Data: data}, nil
}

func (b *harborBackend) pathConfigWrite(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	cfg, err := getConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	isNew := cfg == nil
	if isNew {
		cfg = &config{
			AuthType:        authTypeUser,
			Timeout:         defaultTimeout,
			RobotNamePrefix: defaultRobotNamePrefix,
		}
	}

	if v, ok := d.GetOk("url"); ok {
		cfg.URL = strings.TrimSpace(v.(string))
	}
	if v, ok := d.GetOk("username"); ok {
		cfg.Username = v.(string)
	}
	if v, ok := d.GetOk("password"); ok {
		cfg.Password = v.(string)
		cfg.PendingPassword = ""
	}
	if v, ok := d.GetOk("auth_type"); ok {
		cfg.AuthType = strings.ToLower(v.(string))
	}
	if v, ok := d.GetOk("ca_cert"); ok {
		cfg.CACert = v.(string)
	}
	if v, ok := d.GetOk("insecure_skip_verify"); ok {
		cfg.InsecureSkipVerify = v.(bool)
	}
	if v, ok := d.GetOk("timeout"); ok {
		cfg.Timeout = time.Duration(v.(int)) * time.Second
	}
	if v, ok := d.GetOk("robot_name_prefix"); ok {
		cfg.RobotNamePrefix = v.(string)
	}

	// Validation with named fields.
	switch {
	case cfg.URL == "":
		return logical.ErrorResponse("missing required field: url"), nil
	case cfg.Username == "":
		return logical.ErrorResponse("missing required field: username"), nil
	case cfg.Password == "":
		return logical.ErrorResponse("missing required field: password"), nil
	case cfg.AuthType != authTypeUser && cfg.AuthType != authTypeRobot:
		return logical.ErrorResponse("invalid auth_type %q: must be one of %q, %q", cfg.AuthType, authTypeUser, authTypeRobot), nil
	case cfg.Timeout <= 0:
		return logical.ErrorResponse("timeout must be a positive duration"), nil
	}
	if p := normalizeSegment(cfg.RobotNamePrefix); p == "" && cfg.RobotNamePrefix != "" {
		return logical.ErrorResponse("robot_name_prefix %q contains no characters usable in a Harbor robot name", cfg.RobotNamePrefix), nil
	}

	client, err := harbor.New(cfg.clientConfig())
	if err != nil {
		return logical.ErrorResponse("invalid configuration: %s", err), nil
	}

	if d.Get("verify_connection").(bool) {
		if err := verifyConnection(ctx, client, cfg.AuthType); err != nil {
			return logical.ErrorResponse("failed to verify connection to Harbor: %s", err), nil
		}
	}

	if err := putConfig(ctx, req.Storage, cfg); err != nil {
		return nil, err
	}
	b.resetClient()
	return nil, nil
}

// verifyConnection authenticates against Harbor using an endpoint appropriate
// for the credential kind. Users can call /users/current. Robots cannot; they
// perform a robot listing instead, where Harbor answers 401 for a bad
// credential and 403 for a valid one that lacks system-wide robot:list (the
// normal case for a project-scoped issuer) - both 200 and 403 prove the
// credential is accepted.
func verifyConnection(ctx context.Context, client *harbor.Client, authType string) error {
	switch authType {
	case authTypeRobot:
		if _, err := client.ListRobots(ctx, harbor.ListRobotsOptions{PageSize: 1}); err != nil && !harbor.IsForbidden(err) {
			return err
		}
	default:
		if _, err := client.CurrentUser(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (b *harborBackend) pathConfigDelete(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	if err := req.Storage.Delete(ctx, configStoragePath); err != nil {
		return nil, fmt.Errorf("deleting configuration: %w", err)
	}
	b.resetClient()
	return nil, nil
}

// ---- rotate-root -------------------------------------------------------------

func (b *harborBackend) pathConfigRotateRoot() *framework.Path {
	return &framework.Path{
		Pattern: "config/rotate-root",
		DisplayAttrs: &framework.DisplayAttributes{
			OperationPrefix: "harbor",
			OperationVerb:   "rotate",
			OperationSuffix: "root-credentials",
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.UpdateOperation: &framework.PathOperation{
				Callback:                    b.pathRotateRoot,
				ForwardPerformanceStandby:   true,
				ForwardPerformanceSecondary: true,
			},
		},
		HelpSynopsis: "Rotate the root credential Vault uses to talk to Harbor (auth_type=user only).",
		HelpDescription: `Generates a new password for the configured Harbor user, installs it in
Harbor and stores it in Vault; the new password is never returned.

Not available for auth_type=robot: Harbor does not let a robot account refresh
its own secret (there is no robot:update permission). Refresh the issuer
robot's secret as an administrator and write it to config instead.`,
	}
}

func (b *harborBackend) pathRotateRoot(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	b.clientMu.Lock()
	defer b.clientMu.Unlock()
	// Always drop the cached client: whatever happens below, the credential
	// may have changed.
	b.client, b.clientCfg = nil, nil

	cfg, err := getConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return logical.ErrorResponse(errNotConfigured.Error()), nil
	}
	if cfg.AuthType == authTypeRobot {
		return logical.ErrorResponse(errRobotRotateUnsupported.Error()), nil
	}
	if cfg.PendingPassword != "" {
		if err := b.settlePendingRotation(ctx, req.Storage, cfg); err != nil {
			return nil, err
		}
	}

	newSecret, err := generateSecret()
	if err != nil {
		return nil, fmt.Errorf("generating new secret: %w", err)
	}

	// Phase 1: persist the candidate before touching Harbor so a crash after
	// Harbor accepts it cannot lock Vault out.
	cfg.PendingPassword = newSecret
	if err := putConfig(ctx, req.Storage, cfg); err != nil {
		return nil, err
	}

	client, err := harbor.New(cfg.clientConfig())
	if err != nil {
		return nil, err
	}
	// Phase 2: install in Harbor.
	if err := installSecret(ctx, client, cfg, newSecret); err != nil {
		// Harbor rejected or was unreachable: clear the candidate.
		cfg.PendingPassword = ""
		if perr := putConfig(ctx, req.Storage, cfg); perr != nil {
			return nil, fmt.Errorf("rotation failed (%w) and clearing pending credential also failed: %w", err, perr)
		}
		return logical.ErrorResponse("failed to rotate root credential: %s", err), nil
	}
	// Phase 3: commit.
	cfg.Password = newSecret
	cfg.PendingPassword = ""
	cfg.LastRotated = time.Now()
	if err := putConfig(ctx, req.Storage, cfg); err != nil {
		return nil, err
	}
	return nil, nil
}

// installSecret makes newSecret the active credential in Harbor (user mode).
func installSecret(ctx context.Context, client *harbor.Client, cfg *config, newSecret string) error {
	me, err := client.CurrentUser(ctx)
	if err != nil {
		return fmt.Errorf("resolving current user: %w", err)
	}
	return client.ChangeUserPassword(ctx, me.UserID, cfg.Password, newSecret)
}

// settlePendingRotation is called when a previous rotation left a pending
// candidate. It probes Harbor to find out which credential is live and commits
// the outcome. Callers must hold clientMu.
func (b *harborBackend) settlePendingRotation(ctx context.Context, s logical.Storage, cfg *config) error {
	probe := func(pw string) error {
		c, err := harbor.New(harbor.Config{
			URL: cfg.URL, Username: cfg.Username, Password: pw,
			CACertPEM: cfg.CACert, InsecureSkipVerify: cfg.InsecureSkipVerify, Timeout: cfg.Timeout,
		})
		if err != nil {
			return err
		}
		return verifyConnection(ctx, c, cfg.AuthType)
	}
	switch err := probe(cfg.Password); {
	case err == nil:
		// Old credential still works: pending never took effect.
		cfg.PendingPassword = ""
	case harbor.IsUnauthorized(err) || harbor.IsForbidden(err):
		if perr := probe(cfg.PendingPassword); perr != nil {
			return fmt.Errorf("neither the stored nor the pending root credential is accepted by Harbor; reconfigure with a valid credential: %w", perr)
		}
		cfg.Password = cfg.PendingPassword
		cfg.PendingPassword = ""
		cfg.LastRotated = time.Now()
	default:
		// Transient error; leave pending in place and surface it.
		return fmt.Errorf("cannot settle pending root rotation: %w", err)
	}
	return putConfig(ctx, s, cfg)
}

const (
	secretLower  = "abcdefghijklmnopqrstuvwxyz"
	secretUpper  = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	secretDigits = "0123456789"
	secretAll    = secretLower + secretUpper + secretDigits
	secretLength = 32
)

// generateSecret returns a random secret satisfying Harbor's policy: 8–128
// characters with at least one lowercase letter, one uppercase letter and one
// digit. Alphanumerics only, so it is safe in URLs, shells and Docker configs.
func generateSecret() (string, error) {
	pick := func(set string) (byte, error) {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		if err != nil {
			return 0, err
		}
		return set[n.Int64()], nil
	}
	buf := make([]byte, secretLength)
	var err error
	// Guarantee one of each class in the first three positions, then shuffle.
	if buf[0], err = pick(secretLower); err != nil {
		return "", err
	}
	if buf[1], err = pick(secretUpper); err != nil {
		return "", err
	}
	if buf[2], err = pick(secretDigits); err != nil {
		return "", err
	}
	for i := 3; i < secretLength; i++ {
		if buf[i], err = pick(secretAll); err != nil {
			return "", err
		}
	}
	for i := len(buf) - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", err
		}
		buf[i], buf[j.Int64()] = buf[j.Int64()], buf[i]
	}
	if !harborSecretOK(string(buf)) {
		return "", errors.New("generated secret does not satisfy policy")
	}
	return string(buf), nil
}

func harborSecretOK(s string) bool {
	var lower, upper, digit bool
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		}
	}
	return len(s) >= 8 && len(s) <= 128 && lower && upper && digit
}

const pathConfigHelp = `
This path configures how the secrets engine reaches Harbor and which credential
it uses to manage robot accounts.

auth_type=user: 'username'/'password' are a Harbor local user. A system
administrator can issue robots at any level; a project administrator can issue
project-level robots for their projects.

auth_type=robot: 'username' is the full robot name (including Harbor's robot
prefix, e.g. "robot$vault-issuer") and 'password' its secret. The robot must
hold robot list/create/read/update/delete permissions in the scopes you want to
issue for; Harbor (>= 2.12.1) additionally requires every issued robot's
permissions to be a subset of the issuer's.

Writing this path replaces the configuration; secret fields omitted on an
update are retained. Reads never return secrets.
`

package backend

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"

	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/harbor"
)

const walKindRobot = "robot"

// walRobot is written before a robot is created so that a crash between the
// Harbor call and lease issuance can be rolled back.
type walRobot struct {
	Name      string    `json:"name"`      // short robot name sent to Harbor
	Level     string    `json:"level"`     // system | project
	Namespace string    `json:"namespace"` // project name for project-level robots
	Role      string    `json:"role"`
	RobotID   int64     `json:"robot_id"` // 0 until known
	CreatedAt time.Time `json:"created_at"`
}

func (b *harborBackend) pathCreds() *framework.Path {
	return &framework.Path{
		Pattern: "creds/" + framework.GenericNameRegex("name"),
		DisplayAttrs: &framework.DisplayAttributes{
			OperationPrefix: "harbor",
			OperationVerb:   "generate",
			OperationSuffix: "credentials",
		},
		Fields: map[string]*framework.FieldSchema{
			"name": {
				Type:        framework.TypeLowerCaseString,
				Description: "Name of the role.",
				Required:    true,
			},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ReadOperation: &framework.PathOperation{Callback: b.pathCredsRead},
		},
		HelpSynopsis:    "Generate a Harbor robot account for a role.",
		HelpDescription: pathCredsHelp,
	}
}

func (b *harborBackend) pathCredsRead(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	roleName := d.Get("name").(string)
	r, err := getRole(ctx, req.Storage, roleName)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return logical.ErrorResponse("role %q does not exist", roleName), nil
	}

	client, cfg, err := b.getClient(ctx, req.Storage)
	if err != nil {
		if errors.Is(err, errNotConfigured) {
			return logical.ErrorResponse(err.Error()), nil
		}
		return nil, err
	}

	// Effective TTLs: role → mount defaults. Vault caps further on its own.
	ttl := r.TTL
	if ttl <= 0 {
		ttl = b.System().DefaultLeaseTTL()
	}
	maxTTL := r.MaxTTL
	if maxTTL <= 0 || maxTTL > b.System().MaxLeaseTTL() {
		maxTTL = b.System().MaxLeaseTTL()
	}
	if ttl > maxTTL {
		ttl = maxTTL
	}

	name, err := robotName(cfg.RobotNamePrefix, roleName)
	if err != nil {
		return nil, fmt.Errorf("generating robot name: %w", err)
	}
	wal := walRobot{Name: name, Level: r.Level, Role: roleName, CreatedAt: time.Now()}
	if r.Level == levelProject && len(r.Permissions) > 0 {
		wal.Namespace = r.Permissions[0].Namespace
	}
	walID, err := framework.PutWAL(ctx, req.Storage, walKindRobot, wal)
	if err != nil {
		return nil, fmt.Errorf("writing WAL entry: %w", err)
	}

	created, err := client.CreateRobot(ctx, harbor.RobotCreate{
		Name:        name,
		Description: r.description(req.MountPoint),
		Level:       r.Level,
		Duration:    ttlToDays(maxTTL),
		Permissions: r.Permissions,
	})
	if err != nil {
		// Nothing was created; drop the WAL entry (best effort) and report.
		_ = framework.DeleteWAL(ctx, req.Storage, walID)
		var apiErr *harbor.APIError
		if errors.As(err, &apiErr) {
			return logical.ErrorResponse("harbor rejected robot creation: %s", err), nil
		}
		return nil, fmt.Errorf("creating robot in harbor: %w", err)
	}

	// The robot exists. From here on, any failure must delete it, because a
	// WAL entry that survives this handler means "no lease was issued".
	cleanup := func(cause error) (*logical.Response, error) {
		if derr := client.DeleteRobot(ctx, created.ID); derr != nil && !harbor.IsNotFound(derr) {
			b.Logger().Warn("failed to delete robot after error; WAL rollback will retry",
				"robot_id", created.ID, "username", created.Name, "error", derr)
			return nil, cause
		}
		_ = framework.DeleteWAL(ctx, req.Storage, walID)
		return nil, cause
	}

	if err := framework.DeleteWAL(ctx, req.Storage, walID); err != nil {
		return cleanup(fmt.Errorf("deleting WAL entry: %w", err))
	}

	creationTime, err := time.Parse(time.RFC3339Nano, created.CreationTime)
	if err != nil {
		// Harbor always returns RFC 3339; fall back to now rather than fail.
		creationTime = time.Now()
	}
	expiresAt := "-1"
	if created.ExpiresAt > 0 {
		expiresAt = time.Unix(created.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}

	resp := b.Secret(secretTypeRobot).Response(
		map[string]any{
			"username":   created.Name,
			"secret":     created.Secret,
			"robot_id":   created.ID,
			"expires_at": expiresAt,
			"auth":       base64.StdEncoding.EncodeToString([]byte(created.Name + ":" + created.Secret)),
		},
		map[string]any{
			internalRobotID:      created.ID,
			internalRobotName:    created.Name,
			internalCreationTime: creationTime.UTC().Format(time.RFC3339Nano),
			internalExpiresAt:    created.ExpiresAt,
			internalRole:         roleName,
		},
	)
	resp.Secret.TTL = ttl
	resp.Secret.MaxTTL = maxTTL
	resp.Secret.Renewable = true

	b.Logger().Debug("created harbor robot", "robot_id", created.ID, "username", created.Name, "role", roleName)
	return resp, nil
}

// walRollback deletes robots whose WAL entry outlived the request that created
// them. It is invoked by the framework for entries older than WALRollbackMinAge.
func (b *harborBackend) walRollback(ctx context.Context, req *logical.Request, kind string, data any) error {
	if kind != walKindRobot {
		return fmt.Errorf("unknown WAL kind %q", kind)
	}
	var entry walRobot
	if err := decodeWAL(data, &entry); err != nil {
		return err
	}
	client, _, err := b.getClient(ctx, req.Storage)
	if err != nil {
		if errors.Is(err, errNotConfigured) {
			// No config means no credential to clean up with; drop the entry.
			b.Logger().Warn("dropping WAL entry: backend not configured", "robot_name", entry.Name)
			return nil
		}
		return err
	}
	if entry.RobotID != 0 {
		if err := client.DeleteRobot(ctx, entry.RobotID); err != nil && !harbor.IsNotFound(err) {
			return err
		}
		b.Logger().Info("rolled back orphaned harbor robot", "robot_id", entry.RobotID, "robot_name", entry.Name)
		return nil
	}

	// Locate by the short name we sent (project-level robots need the project
	// filter; unfiltered listings only show system-level robots).
	robot, err := client.FindRobotByShortName(ctx, entry.Level, entry.Namespace, entry.Name)
	if err != nil {
		return err
	}
	if robot == nil {
		b.Logger().Debug("WAL rollback: robot not found, nothing to do", "robot_name", entry.Name)
		return nil
	}
	if err := client.DeleteRobot(ctx, robot.ID); err != nil && !harbor.IsNotFound(err) {
		return err
	}
	b.Logger().Info("rolled back orphaned harbor robot", "robot_id", robot.ID, "username", robot.Name)
	return nil
}

func decodeWAL(data any, out *walRobot) error {
	m, ok := data.(map[string]any)
	if !ok {
		return fmt.Errorf("unexpected WAL data type %T", data)
	}
	out.Name, _ = m["name"].(string)
	out.Level, _ = m["level"].(string)
	out.Namespace, _ = m["namespace"].(string)
	out.Role, _ = m["role"].(string)
	if id, err := internalInt64(m, "robot_id"); err == nil {
		out.RobotID = id
	}
	if out.Name == "" && out.RobotID == 0 {
		return errors.New("WAL entry has neither name nor robot_id")
	}
	return nil
}

const pathCredsHelp = `
Reading this path creates a Harbor robot account according to the named role
and returns its credentials together with a renewable lease. Renewing the lease
extends the robot's expiry in Harbor; revoking it deletes the robot.

The "auth" field is base64(username:secret) and can be placed directly into a
Docker config.json / Kubernetes dockerconfigjson secret.
`

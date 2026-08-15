package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"

	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/harbor"
)

const secretTypeRobot = "harbor_robot"

// Internal-data keys stored on the lease.
const (
	internalRobotID      = "robot_id"
	internalRobotName    = "robot_name"
	internalCreationTime = "creation_time" // RFC 3339
	internalExpiresAt    = "expires_at"    // unix seconds, or -1
	internalRole         = "role"
)

func (b *harborBackend) secretRobot() *framework.Secret {
	return &framework.Secret{
		Type: secretTypeRobot,
		Fields: map[string]*framework.FieldSchema{
			"username": {
				Type:        framework.TypeString,
				Description: "Full robot account name as returned by Harbor (e.g. robot$vault-ci-1a2b3c4d).",
			},
			"secret": {
				Type:         framework.TypeString,
				Description:  "Robot account secret.",
				DisplayAttrs: &framework.DisplayAttributes{Sensitive: true},
			},
			"robot_id": {
				Type:        framework.TypeInt,
				Description: "Harbor robot account ID.",
			},
			"expires_at": {
				Type:        framework.TypeString,
				Description: "Harbor-side expiry of the robot (RFC 3339), or \"-1\" if it never expires.",
			},
			"auth": {
				Type:         framework.TypeString,
				Description:  "base64(username:secret), usable as the \"auth\" value in a Docker config.json.",
				DisplayAttrs: &framework.DisplayAttributes{Sensitive: true},
			},
		},
		Renew:  b.secretRobotRenew,
		Revoke: b.secretRobotRevoke,
	}
}

type leaseRobot struct {
	ID           int64
	Name         string
	CreationTime time.Time
	ExpiresAt    int64
	Role         string
}

func leaseRobotFromInternal(in map[string]any) (*leaseRobot, error) {
	lr := &leaseRobot{}
	var err error
	if lr.ID, err = internalInt64(in, internalRobotID); err != nil {
		return nil, err
	}
	lr.Name, _ = in[internalRobotName].(string)
	lr.Role, _ = in[internalRole].(string)
	if ct, ok := in[internalCreationTime].(string); ok && ct != "" {
		if lr.CreationTime, err = time.Parse(time.RFC3339Nano, ct); err != nil {
			return nil, fmt.Errorf("secret is missing a valid %s: %w", internalCreationTime, err)
		}
	}
	if lr.ExpiresAt, err = internalInt64(in, internalExpiresAt); err != nil {
		lr.ExpiresAt = -1
	}
	return lr, nil
}

func internalInt64(in map[string]any, key string) (int64, error) {
	switch v := in[key].(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case float64:
		return int64(v), nil
	case json.Number:
		return v.Int64()
	case string:
		return strconv.ParseInt(v, 10, 64)
	default:
		return 0, fmt.Errorf("secret is missing internal data %q", key)
	}
}

func (b *harborBackend) secretRobotRenew(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	lr, err := leaseRobotFromInternal(req.Secret.InternalData)
	if err != nil {
		return nil, err
	}
	r, err := getRole(ctx, req.Storage, lr.Role)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("role %q no longer exists; cannot renew", lr.Role)
	}

	resp := &logical.Response{Secret: req.Secret}
	resp.Secret.TTL = r.TTL
	resp.Secret.MaxTTL = r.MaxTTL

	// Upper bound on the new lease expiry: Vault may cap it further, which only
	// makes the Harbor expiry more generous than needed, never too short.
	ttl := r.TTL
	if ttl <= 0 {
		ttl = b.System().DefaultLeaseTTL()
	}
	if inc := req.Secret.Increment; inc > ttl {
		ttl = inc
	}
	wantExpiry := time.Now().Add(ttl)

	if lr.ExpiresAt == -1 || time.Unix(lr.ExpiresAt, 0).After(wantExpiry) {
		return resp, nil // Harbor expiry already covers the new lease.
	}

	client, _, err := b.getClient(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	robot, err := client.GetRobot(ctx, lr.ID)
	if err != nil {
		if harbor.IsNotFound(err) {
			return nil, fmt.Errorf("robot %q (id %d) no longer exists in Harbor; cannot renew", lr.Name, lr.ID)
		}
		return nil, err
	}
	created := lr.CreationTime
	if created.IsZero() {
		if created, err = time.Parse(time.RFC3339Nano, robot.CreationTime); err != nil {
			return nil, fmt.Errorf("parsing robot creation time %q: %w", robot.CreationTime, err)
		}
	}
	robot.Duration = durationForExpiry(created, wantExpiry)
	if uerr := client.UpdateRobot(ctx, robot); uerr != nil {
		// Harbor refused (robot-mode issuers cannot update robots: there is no
		// robot:update permission) or failed. Fall back to capping the lease at
		// the robot's remaining lifetime so the credential never outlives it.
		return b.capRenewToRobot(resp, lr, robot.ExpiresAt, uerr)
	}
	// Re-read to learn the authoritative expiry (Harbor recomputes it).
	updated, err := client.GetRobot(ctx, lr.ID)
	if err != nil {
		return nil, fmt.Errorf("re-reading robot after extension: %w", err)
	}
	if updated.ExpiresAt != -1 && time.Unix(updated.ExpiresAt, 0).Before(wantExpiry) {
		return b.capRenewToRobot(resp, lr, updated.ExpiresAt,
			fmt.Errorf("harbor expiry %s is still before requested lease expiry %s after extension",
				time.Unix(updated.ExpiresAt, 0).UTC().Format(time.RFC3339), wantExpiry.UTC().Format(time.RFC3339)))
	}
	resp.Secret.InternalData[internalExpiresAt] = updated.ExpiresAt
	b.Logger().Debug("extended harbor robot expiry", "robot_id", lr.ID, "username", lr.Name, "expires_at", updated.ExpiresAt)
	return resp, nil
}

// capRenewToRobot limits the renewed lease so it ends no later than the
// robot's Harbor expiry, and explains why in a warning. If the robot has
// already expired the renewal fails.
func (b *harborBackend) capRenewToRobot(resp *logical.Response, lr *leaseRobot, expiresAt int64, cause error) (*logical.Response, error) {
	if expiresAt == -1 {
		return resp, nil
	}
	remaining := time.Until(time.Unix(expiresAt, 0))
	if remaining <= 0 {
		return nil, fmt.Errorf("robot %q (id %d) has expired in Harbor and its expiry could not be extended: %w", lr.Name, lr.ID, cause)
	}
	if resp.Secret.TTL <= 0 || resp.Secret.TTL > remaining {
		resp.Secret.TTL = remaining
	}
	if resp.Secret.MaxTTL <= 0 || resp.Secret.MaxTTL > remaining {
		resp.Secret.MaxTTL = remaining
	}
	resp.AddWarning(fmt.Sprintf("lease capped at the robot's Harbor expiry (%s) because the expiry could not be extended: %s; request new credentials before then",
		time.Unix(expiresAt, 0).UTC().Format(time.RFC3339), cause))
	b.Logger().Debug("capped renewal at harbor robot expiry", "robot_id", lr.ID, "username", lr.Name, "expires_at", expiresAt, "error", cause)
	return resp, nil
}

func (b *harborBackend) secretRobotRevoke(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	lr, err := leaseRobotFromInternal(req.Secret.InternalData)
	if err != nil {
		return nil, err
	}
	client, _, err := b.getClient(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if err := client.DeleteRobot(ctx, lr.ID); err != nil {
		if harbor.IsNotFound(err) {
			b.Logger().Debug("harbor robot already deleted", "robot_id", lr.ID, "username", lr.Name)
			return nil, nil
		}
		return nil, fmt.Errorf("deleting robot %q (id %d): %w", lr.Name, lr.ID, err)
	}
	b.Logger().Debug("deleted harbor robot", "robot_id", lr.ID, "username", lr.Name)
	return nil, nil
}

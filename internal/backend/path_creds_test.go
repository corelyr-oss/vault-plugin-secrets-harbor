package backend

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"
	"github.com/stretchr/testify/require"

	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/harbor"
	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/harbor/harbortest"
)

func TestCreds_Issue(t *testing.T) {
	e := newTestEnv(t)
	e.configureUser()
	e.writeRole("CI_Builder", map[string]any{"level": "project", "permissions": permsJSON(t, pullPerms("library")), "ttl": "1h", "max_ttl": "1h"})

	resp := e.creds("CI_Builder")
	require.NotNil(t, resp.Secret)
	require.Equal(t, secretTypeRobot, resp.Secret.InternalData["secret_type"])
	require.True(t, resp.Secret.Renewable)
	require.Equal(t, time.Hour, resp.Secret.TTL)
	require.Equal(t, time.Hour, resp.Secret.MaxTTL)

	username := resp.Data["username"].(string)
	secret := resp.Data["secret"].(string)
	require.True(t, strings.HasPrefix(username, "robot$library+vault-ci-builder-"), username)
	require.Regexp(t, harborRobotNameRe, harbor.ShortName(username))
	require.NotEmpty(t, secret)
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte(username+":"+secret)), resp.Data["auth"])
	robotID := resp.Data["robot_id"].(int64)
	require.NotZero(t, robotID)
	expiresAt, err := time.Parse(time.RFC3339, resp.Data["expires_at"].(string))
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(24*time.Hour), expiresAt, time.Minute)

	// robot exists in Harbor with the role's permissions and works for auth
	stored := e.harbor.Robot(robotID)
	require.NotNil(t, stored)
	require.Equal(t, int64(1), stored.Duration)
	require.Equal(t, pullPerms("library"), stored.Permissions)
	require.Equal(t, "Managed by Vault (harbor/ci_builder)", stored.Description)
	rc, err := harbor.New(harbor.Config{URL: e.harbor.URL, Username: username, Password: secret})
	require.NoError(t, err)
	_, err = rc.ListRobots(context.Background(), harbor.ListRobotsOptions{PageSize: 1})
	require.True(t, err == nil || harbor.IsForbidden(err), "issued robot must authenticate (403 = valid but unprivileged): %v", err)

	// internal data
	require.Equal(t, robotID, resp.Secret.InternalData[internalRobotID])
	require.Equal(t, "ci_builder", resp.Secret.InternalData[internalRole])

	// no WAL left behind
	wals, err := framework.ListWAL(e.ctx, e.storage)
	require.NoError(t, err)
	require.Empty(t, wals)
}

func TestCreds_UnknownRole(t *testing.T) {
	e := newTestEnv(t)
	e.configureUser()
	msg := e.mustErr(logical.ReadOperation, "creds/nope", nil)
	require.Contains(t, msg, "does not exist")
	require.Empty(t, e.harbor.Robots())
}

func TestCreds_HarborCreateFails(t *testing.T) {
	e := newTestEnv(t)
	e.configureUser()
	e.writeRole("ci", map[string]any{"level": "project", "permissions": permsJSON(t, pullPerms("library"))})
	e.harbor.FailNext["POST /robots"] = harbortest.Failure{Status: 500, Code: "INTERNAL", Message: "db down"}
	msg := e.mustErr(logical.ReadOperation, "creds/ci", nil)
	require.Contains(t, msg, "500")
	require.Contains(t, msg, "db down")
	wals, _ := framework.ListWAL(e.ctx, e.storage)
	require.Empty(t, wals)
}

func TestCreds_RobotModeScopeError(t *testing.T) {
	e := newTestEnv(t)
	e.configureRobot(issuerPerms("library", harbor.Access{Resource: "repository", Action: "pull"}))
	e.writeRole("push", map[string]any{"level": "project", "permissions": permsJSON(t, []harbor.RobotPermission{{Kind: "project", Namespace: "library",
		Access: []harbor.Access{{Resource: "repository", Action: "push"}}}})})
	msg := e.mustErr(logical.ReadOperation, "creds/push", nil)
	require.Contains(t, msg, "permission scope is invalid")
	require.Len(t, e.harbor.Robots(), 1, "only the issuer robot")

	e.writeRole("pull", map[string]any{"level": "project", "permissions": permsJSON(t, pullPerms("library"))})
	e.creds("pull")
	require.Len(t, e.harbor.Robots(), 2)
}

func TestCreds_DurationFromMaxTTL(t *testing.T) {
	e := newTestEnv(t)
	e.configureUser()
	e.writeRole("h1", map[string]any{"level": "project", "permissions": permsJSON(t, pullPerms("library")), "max_ttl": "1h"})
	e.writeRole("h47", map[string]any{"level": "project", "permissions": permsJSON(t, pullPerms("library")), "max_ttl": "47h"})
	r1 := e.creds("h1")
	r2 := e.creds("h47")
	require.Equal(t, int64(1), e.harbor.Robot(r1.Data["robot_id"].(int64)).Duration)
	require.Equal(t, int64(2), e.harbor.Robot(r2.Data["robot_id"].(int64)).Duration)
	// lease never outlives the robot
	for _, r := range []*logical.Response{r1, r2} {
		exp, _ := time.Parse(time.RFC3339, r.Data["expires_at"].(string))
		require.True(t, time.Now().Add(r.Secret.MaxTTL).Before(exp))
	}
}

func TestCreds_RenewExtendsRobot(t *testing.T) {
	e := newTestEnv(t)
	e.configureUser()
	e.writeRole("k8s", map[string]any{"level": "project", "permissions": permsJSON(t, pullPerms("library")), "ttl": "24h", "max_ttl": "48h"})
	resp := e.creds("k8s")
	id := resp.Data["robot_id"].(int64)
	before := e.harbor.Robot(id)
	require.Equal(t, int64(2), before.Duration)

	// Renew within current expiry: no Harbor update needed.
	rresp, err := e.renew(resp.Secret, time.Hour)
	require.NoError(t, err)
	require.Equal(t, 24*time.Hour, rresp.Secret.TTL)
	require.Equal(t, before.ExpiresAt, e.harbor.Robot(id).ExpiresAt)

	// Simulate time passing: move Harbor's clock so the robot's expiry is near.
	// Instead, request a large increment that exceeds the current expiry.
	rresp, err = e.renew(resp.Secret, 5*24*time.Hour)
	require.NoError(t, err)
	after := e.harbor.Robot(id)
	require.Greater(t, after.Duration, before.Duration)
	require.GreaterOrEqual(t, after.ExpiresAt, time.Now().Add(5*24*time.Hour).Unix()-1)
	require.Equal(t, after.ExpiresAt, rresp.Secret.InternalData[internalExpiresAt])

	// Repeated renewals keep succeeding and always cover the lease.
	for i := 6; i < 12; i++ {
		rresp, err = e.renew(rresp.Secret, time.Duration(i)*24*time.Hour)
		require.NoError(t, err)
		require.GreaterOrEqual(t, e.harbor.Robot(id).ExpiresAt, time.Now().Add(time.Duration(i)*24*time.Hour).Unix()-1)
	}
	require.Contains(t, e.harbor.Requests(), "PUT /api/v2.0/robots/"+itoa(id))
}

func TestCreds_RenewRobotMode_CapsAtRobotExpiry(t *testing.T) {
	e := newTestEnv(t)
	e.configureRobot(issuerPerms("library", harbor.Access{Resource: "repository", Action: "pull"}))
	e.writeRole("k8s", map[string]any{"level": "project", "permissions": permsJSON(t, pullPerms("library")), "ttl": "24h", "max_ttl": "48h"})
	resp := e.creds("k8s")
	id := resp.Data["robot_id"].(int64)
	before := e.harbor.Robot(id)

	// Renewal within the robot's expiry: plain success.
	rresp, err := e.renew(resp.Secret, time.Hour)
	require.NoError(t, err)
	require.Empty(t, rresp.Warnings)

	// Renewal that would need an extension: issuer robots cannot update robots,
	// so the lease is capped at the robot's expiry instead of failing.
	rresp, err = e.renew(resp.Secret, 5*24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, before.ExpiresAt, e.harbor.Robot(id).ExpiresAt, "expiry unchanged")
	require.Len(t, rresp.Warnings, 1)
	require.Contains(t, rresp.Warnings[0], "capped")
	require.LessOrEqual(t, rresp.Secret.TTL, time.Until(time.Unix(before.ExpiresAt, 0)))
	require.Greater(t, rresp.Secret.TTL, time.Duration(0))
}

func TestCreds_WALRollback_RobotMode(t *testing.T) {
	e := newTestEnv(t)
	e.configureRobot(issuerPerms("library", harbor.Access{Resource: "repository", Action: "pull"}))
	// Orphan created by the issuer robot itself.
	cfg, err := getConfig(e.ctx, e.storage)
	require.NoError(t, err)
	ic, err := harbor.New(harbor.Config{URL: e.harbor.URL, Username: cfg.Username, Password: cfg.Password})
	require.NoError(t, err)
	created, err := ic.CreateRobot(e.ctx, harbor.RobotCreate{Name: "vault-ci-cafebabe", Level: "project", Duration: 30, Permissions: pullPerms("library")})
	require.NoError(t, err)
	err = e.b.walRollback(e.ctx, e.req(logical.RollbackOperation, "", nil), walKindRobot,
		map[string]any{"name": "vault-ci-cafebabe", "level": "project", "namespace": "library", "role": "ci"})
	require.NoError(t, err)
	require.Nil(t, e.harbor.Robot(created.ID))
}

func TestCreds_RenewRobotGone(t *testing.T) {
	e := newTestEnv(t)
	e.configureUser()
	e.writeRole("ci", map[string]any{"level": "project", "permissions": permsJSON(t, pullPerms("library")), "ttl": "1h", "max_ttl": "1h"})
	resp := e.creds("ci")
	id := resp.Data["robot_id"].(int64)
	rc := adminClient(t, e.harbor)
	require.NoError(t, rc.DeleteRobot(e.ctx, id))
	_, err := e.renew(resp.Secret, 48*time.Hour)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no longer exists")
}

func TestCreds_Revoke(t *testing.T) {
	e := newTestEnv(t)
	e.configureUser()
	e.writeRole("ci", map[string]any{"level": "project", "permissions": permsJSON(t, pullPerms("library"))})
	resp := e.creds("ci")
	id := resp.Data["robot_id"].(int64)
	username, secret := resp.Data["username"].(string), resp.Data["secret"].(string)

	_, err := e.revoke(resp.Secret)
	require.NoError(t, err)
	require.Nil(t, e.harbor.Robot(id))
	rc, _ := harbor.New(harbor.Config{URL: e.harbor.URL, Username: username, Password: secret})
	_, err = rc.ListRobots(e.ctx, harbor.ListRobotsOptions{PageSize: 1})
	require.True(t, harbor.IsUnauthorized(err), "old credentials must not work: %v", err)

	// Already gone: still success.
	_, err = e.revoke(resp.Secret)
	require.NoError(t, err)

	// Other errors propagate for retry.
	resp2 := e.creds("ci")
	e.harbor.FailNext["DELETE /robots/"] = harbortest.Failure{Status: 500, Code: "INTERNAL", Message: "boom"}
	_, err = e.revoke(resp2.Secret)
	require.Error(t, err)
	require.NotNil(t, e.harbor.Robot(resp2.Data["robot_id"].(int64)))
}

func TestCreds_WALRollbackDeletesOrphan(t *testing.T) {
	e := newTestEnv(t)
	e.configureUser()
	// Simulate a crash after Harbor confirmed creation but before the WAL was
	// deleted: create the robot directly and leave a stale WAL entry.
	rc := adminClient(t, e.harbor)
	name := "vault-ci-deadbeef"
	created, err := rc.CreateRobot(e.ctx, harbor.RobotCreate{Name: name, Level: "project", Duration: 30, Permissions: pullPerms("library")})
	require.NoError(t, err)
	// A sibling with a similar (fuzzy-matching) name must survive.
	sibling, err := rc.CreateRobot(e.ctx, harbor.RobotCreate{Name: name + "-2", Level: "project", Duration: 30, Permissions: pullPerms("library")})
	require.NoError(t, err)

	err = e.b.walRollback(e.ctx, e.req(logical.RollbackOperation, "", nil), walKindRobot,
		map[string]any{"name": name, "level": "project", "namespace": "library", "role": "ci"})
	require.NoError(t, err)
	require.Nil(t, e.harbor.Robot(created.ID))
	require.NotNil(t, e.harbor.Robot(sibling.ID))

	// With robot_id recorded, delete by id; 404 is fine.
	err = e.b.walRollback(e.ctx, e.req(logical.RollbackOperation, "", nil), walKindRobot,
		map[string]any{"name": name, "robot_id": float64(created.ID)})
	require.NoError(t, err)

	// Unknown kind is an error.
	require.Error(t, e.b.walRollback(e.ctx, e.req(logical.RollbackOperation, "", nil), "other", map[string]any{}))
}

func TestCreds_WALDeleteFailureCleansUp(t *testing.T) {
	e := newTestEnv(t)
	e.configureUser()
	e.writeRole("ci", map[string]any{"level": "project", "permissions": permsJSON(t, pullPerms("library"))})
	fs := &failingStorage{Storage: e.storage, failDeletePrefix: framework.WALPrefix}
	req := &logical.Request{Operation: logical.ReadOperation, Path: "creds/ci", Storage: fs, MountPoint: testMount}
	_, err := e.b.HandleRequest(e.ctx, req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "WAL")
	require.Empty(t, e.harbor.Robots(), "robot must be deleted when the lease cannot be issued")
}

func TestCreds_SensitiveFieldsMarked(t *testing.T) {
	e := newTestEnv(t)
	sec := e.b.Secret(secretTypeRobot)
	require.True(t, sec.Fields["secret"].DisplayAttrs.Sensitive)
	require.True(t, sec.Fields["auth"].DisplayAttrs.Sensitive)
	require.Nil(t, sec.Fields["username"].DisplayAttrs)
	require.Nil(t, sec.Fields["robot_id"].DisplayAttrs)
}

// --- helpers ---

type failingStorage struct {
	logical.Storage
	failDeletePrefix string
}

func (f *failingStorage) Delete(ctx context.Context, key string) error {
	if strings.HasPrefix(key, f.failDeletePrefix) {
		return context.DeadlineExceeded
	}
	return f.Storage.Delete(ctx, key)
}

func adminClient(t *testing.T, s *harbortest.Server) *harbor.Client {
	t.Helper()
	c, err := harbor.New(harbor.Config{URL: s.URL, Username: "admin", Password: "Harbor12345"})
	require.NoError(t, err)
	return c
}

func itoa(i int64) string { return fmtInt(i) }

package backend

import (
	"encoding/pem"
	"testing"

	"github.com/hashicorp/vault/sdk/logical"
	"github.com/stretchr/testify/require"

	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/harbor"
	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/harbor/harbortest"
)

func TestConfig_UserMode(t *testing.T) {
	e := newTestEnv(t)
	e.configureUser()

	resp := e.mustOK(logical.ReadOperation, "config", nil)
	require.Equal(t, e.harbor.URL, resp.Data["url"])
	require.Equal(t, "admin", resp.Data["username"])
	require.Equal(t, "user", resp.Data["auth_type"])
	require.Equal(t, false, resp.Data["insecure_skip_verify"])
	require.Equal(t, int64(30), resp.Data["timeout"])
	require.Equal(t, false, resp.Data["ca_cert_set"])
	require.Equal(t, "vault", resp.Data["robot_name_prefix"])
	require.NotContains(t, resp.Data, "password")
	require.NotContains(t, resp.Data, "last_rotated")
	for k := range resp.Data {
		require.NotContains(t, []string{"password", "ca_cert", "pending_password"}, k)
	}
}

func TestConfig_RobotMode(t *testing.T) {
	e := newTestEnv(t)
	issuer := e.configureRobot(issuerPerms("library"))
	resp := e.mustOK(logical.ReadOperation, "config", nil)
	require.Equal(t, "robot", resp.Data["auth_type"])
	require.Equal(t, issuer.Name, resp.Data["username"])

	// wrong robot secret is rejected (401), unlike the 403 a valid one gets
	msg := e.mustErr(logical.UpdateOperation, "config", map[string]any{"password": "wrong"})
	require.Contains(t, msg, "failed to verify connection")
}

func TestConfig_MissingFields(t *testing.T) {
	e := newTestEnv(t)
	msg := e.mustErr(logical.CreateOperation, "config", map[string]any{"username": "a", "password": "b"})
	require.Contains(t, msg, "url")
	msg = e.mustErr(logical.CreateOperation, "config", map[string]any{"url": e.harbor.URL, "password": "b"})
	require.Contains(t, msg, "username")
	msg = e.mustErr(logical.CreateOperation, "config", map[string]any{"url": e.harbor.URL, "username": "a"})
	require.Contains(t, msg, "password")
	// nothing stored
	require.Nil(t, e.do(logical.ReadOperation, "config", nil))
}

func TestConfig_InvalidAuthType(t *testing.T) {
	e := newTestEnv(t)
	msg := e.mustErr(logical.CreateOperation, "config", map[string]any{
		"url": e.harbor.URL, "username": "admin", "password": "Harbor12345", "auth_type": "token"})
	require.Contains(t, msg, "auth_type")
	require.Contains(t, msg, `"user"`)
	require.Contains(t, msg, `"robot"`)
}

func TestConfig_WrongCredentials_KeepsPrevious(t *testing.T) {
	e := newTestEnv(t)
	e.configureUser()
	msg := e.mustErr(logical.UpdateOperation, "config", map[string]any{
		"url": e.harbor.URL, "username": "admin", "password": "nope"})
	require.Contains(t, msg, "failed to verify connection")
	cfg, err := getConfig(e.ctx, e.storage)
	require.NoError(t, err)
	require.Equal(t, "Harbor12345", cfg.Password)
}

func TestConfig_UpdateRetainsSecrets(t *testing.T) {
	e := newTestEnv(t)
	e.configureUser()
	e.mustOK(logical.UpdateOperation, "config", map[string]any{"timeout": "10s", "robot_name_prefix": "Prod Vault"})
	cfg, err := getConfig(e.ctx, e.storage)
	require.NoError(t, err)
	require.Equal(t, "Harbor12345", cfg.Password)
	require.Equal(t, int64(10), int64(cfg.Timeout.Seconds()))
	require.Equal(t, "Prod Vault", cfg.RobotNamePrefix)
	msg := e.mustErr(logical.UpdateOperation, "config", map[string]any{"robot_name_prefix": "!!!"})
	require.Contains(t, msg, "robot_name_prefix")
}

func TestConfig_CustomCA(t *testing.T) {
	e := newTestEnv(t)
	tlsHarbor := harbortest.NewTLS()
	defer tlsHarbor.Close()
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsHarbor.Certificate().Raw}))

	msg := e.mustErr(logical.CreateOperation, "config", map[string]any{
		"url": tlsHarbor.URL, "username": "admin", "password": "Harbor12345"})
	require.Contains(t, msg, "failed to verify connection")

	e.mustOK(logical.CreateOperation, "config", map[string]any{
		"url": tlsHarbor.URL, "username": "admin", "password": "Harbor12345", "ca_cert": caPEM})
	resp := e.mustOK(logical.ReadOperation, "config", nil)
	require.Equal(t, true, resp.Data["ca_cert_set"])
	require.NotContains(t, resp.Data, "ca_cert")

	msg = e.mustErr(logical.UpdateOperation, "config", map[string]any{"ca_cert": "garbage"})
	require.Contains(t, msg, "ca_cert")
}

func TestConfig_VerifySkipped(t *testing.T) {
	e := newTestEnv(t)
	e.mustOK(logical.CreateOperation, "config", map[string]any{
		"url": "https://harbor.invalid:1", "username": "admin", "password": "x", "verify_connection": false})
	resp := e.mustOK(logical.ReadOperation, "config", nil)
	require.Equal(t, "https://harbor.invalid:1", resp.Data["url"])
}

func TestConfig_ReadBeforeWrite(t *testing.T) {
	e := newTestEnv(t)
	require.Nil(t, e.do(logical.ReadOperation, "config", nil))
}

func TestConfig_DeleteThenCreds(t *testing.T) {
	e := newTestEnv(t)
	e.configureUser()
	e.writeRole("ci", map[string]any{"level": "project", "permissions": permsJSON(t, pullPerms("library"))})
	e.creds("ci")
	e.mustOK(logical.DeleteOperation, "config", nil)
	msg := e.mustErr(logical.ReadOperation, "creds/ci", nil)
	require.Contains(t, msg, "not configured")
}

func TestRotateRoot_UserMode(t *testing.T) {
	e := newTestEnv(t)
	e.configureUser()
	resp := e.mustOK(logical.UpdateOperation, "config/rotate-root", nil)
	require.Nil(t, resp)

	cfg, err := getConfig(e.ctx, e.storage)
	require.NoError(t, err)
	require.NotEqual(t, "Harbor12345", cfg.Password)
	require.Empty(t, cfg.PendingPassword)
	require.True(t, harborSecretOK(cfg.Password))
	require.Equal(t, cfg.Password, e.harbor.User("admin").Password)
	require.False(t, cfg.LastRotated.IsZero())

	read := e.mustOK(logical.ReadOperation, "config", nil)
	require.Contains(t, read.Data, "last_rotated")

	// Engine keeps working with the new credential.
	e.writeRole("ci", map[string]any{"level": "project", "permissions": permsJSON(t, pullPerms("library"))})
	e.creds("ci")
}

func TestRotateRoot_RobotModeUnsupported(t *testing.T) {
	e := newTestEnv(t)
	issuer := e.configureRobot(issuerPerms("library", harbor.Access{Resource: "repository", Action: "pull"}))
	msg := e.mustErr(logical.UpdateOperation, "config/rotate-root", nil)
	require.Contains(t, msg, "not supported for auth_type=robot")
	cfg, err := getConfig(e.ctx, e.storage)
	require.NoError(t, err)
	require.Equal(t, "IssuerSecret1", cfg.Password)
	require.Equal(t, "IssuerSecret1", e.harbor.Robot(issuer.ID).Secret)

	// engine still works
	e.writeRole("ci", map[string]any{"level": "project", "permissions": permsJSON(t, pullPerms("library"))})
	e.creds("ci")
}

func TestRotateRoot_HarborRejects(t *testing.T) {
	e := newTestEnv(t)
	e.configureUser()
	e.harbor.FailNext["PUT /users/"] = harbortest.Failure{Status: 500, Code: "INTERNAL", Message: "boom"}
	msg := e.mustErr(logical.UpdateOperation, "config/rotate-root", nil)
	require.Contains(t, msg, "boom")
	cfg, err := getConfig(e.ctx, e.storage)
	require.NoError(t, err)
	require.Equal(t, "Harbor12345", cfg.Password)
	require.Empty(t, cfg.PendingPassword)
}

func TestRotateRoot_Unconfigured(t *testing.T) {
	e := newTestEnv(t)
	msg := e.mustErr(logical.UpdateOperation, "config/rotate-root", nil)
	require.Contains(t, msg, "not configured")
}

func TestRotateRoot_SettlesPending(t *testing.T) {
	e := newTestEnv(t)
	e.configureUser()
	// Simulate: Harbor accepted the new password but Vault crashed before commit.
	cfg, err := getConfig(e.ctx, e.storage)
	require.NoError(t, err)
	cfg.PendingPassword = "PendingPass123"
	require.NoError(t, putConfig(e.ctx, e.storage, cfg))
	e.harbor.User("admin") // exists
	e.harbor.AddUser(&harbortest.User{ID: 1, Username: "admin", Password: "PendingPass123", Sysadmin: true})
	e.b.resetClient()

	e.writeRole("ci", map[string]any{"level": "project", "permissions": permsJSON(t, pullPerms("library"))})
	e.creds("ci") // must settle and succeed
	cfg, err = getConfig(e.ctx, e.storage)
	require.NoError(t, err)
	require.Equal(t, "PendingPass123", cfg.Password)
	require.Empty(t, cfg.PendingPassword)

	// The other direction: pending never took effect → dropped.
	cfg.PendingPassword = "NeverApplied1"
	require.NoError(t, putConfig(e.ctx, e.storage, cfg))
	e.b.resetClient()
	e.creds("ci")
	cfg, err = getConfig(e.ctx, e.storage)
	require.NoError(t, err)
	require.Equal(t, "PendingPass123", cfg.Password)
	require.Empty(t, cfg.PendingPassword)
}

func TestGenerateSecret(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		s, err := generateSecret()
		require.NoError(t, err)
		require.True(t, harborSecretOK(s), s)
		require.True(t, harbortest.IsValidSecret(s), s)
		require.False(t, seen[s])
		seen[s] = true
	}
}

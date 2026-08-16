//go:build integration

package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/harbor"
)

func TestPluginVersionReported(t *testing.T) {
	m := vault.mount(t)
	mounts, err := vault.client.Sys().ListMounts()
	require.NoError(t, err)
	require.Equal(t, pluginVersion, mounts[m.path+"/"].PluginVersion)
	require.Equal(t, pluginVersion, mounts[m.path+"/"].RunningVersion)
}

func TestConfig_UserMode(t *testing.T) {
	m := vault.mount(t)

	// wrong credentials rejected, nothing stored
	_, err := m.write("config", map[string]any{"url": harborURL, "username": adminUser, "password": "nope"})
	require.ErrorContains(t, err, "failed to verify connection")
	s, err := m.read("config")
	require.NoError(t, err)
	require.Nil(t, s)

	// missing field
	_, err = m.write("config", map[string]any{"url": harborURL, "username": adminUser})
	require.ErrorContains(t, err, "password")

	// invalid auth_type
	_, err = m.write("config", map[string]any{"url": harborURL, "username": adminUser, "password": adminPass, "auth_type": "token"})
	require.ErrorContains(t, err, "auth_type")

	m.configureAdmin()
	s = m.mustRead("config")
	require.Equal(t, harborURL, s.Data["url"])
	require.Equal(t, adminUser, s.Data["username"])
	require.Equal(t, "user", s.Data["auth_type"])
	require.NotContains(t, s.Data, "password")

	// verify skipped
	_, err = m.write("config", map[string]any{"url": "http://harbor.invalid:1", "username": "x", "password": "y", "verify_connection": false})
	require.NoError(t, err)
	// restore
	m.configureAdmin()

	// delete → creds error
	_, err = vault.client.Logical().Delete(m.path + "/config")
	require.NoError(t, err)
	m.mustWrite("roles/ci", map[string]any{"level": "project", "permissions": mustJSON(pullPerms("library"))})
	_, err = m.read("creds/ci")
	require.ErrorContains(t, err, "not configured")
}

func TestConfig_RobotMode(t *testing.T) {
	m := vault.mount(t)
	project, _ := newProject(t)
	name, secret := issuerRobot(t, project, "project")

	_, err := m.write("config", map[string]any{"url": harborURL, "username": name, "password": "wrong", "auth_type": "robot"})
	require.ErrorContains(t, err, "failed to verify connection")

	m.mustWrite("config", map[string]any{"url": harborURL, "username": name, "password": secret, "auth_type": "robot"})
	s := m.mustRead("config")
	require.Equal(t, "robot", s.Data["auth_type"])
	require.Equal(t, name, s.Data["username"])

	// rotate-root is not possible for robots
	_, err = m.write("config/rotate-root", nil)
	require.ErrorContains(t, err, "not supported for auth_type=robot")
}

func TestRotateRoot_UserMode(t *testing.T) {
	m := vault.mount(t)
	project, projectID := newProject(t)
	// A dedicated service user (project admin) so the shared admin password is untouched.
	username := fmt.Sprintf("vault-svc-%s", runID)
	uid, err := createUser(username, "SvcPassword1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = adminDelete(fmt.Sprintf("/users/%d", uid)) })
	require.NoError(t, addProjectAdmin(projectID, uid))

	m.mustWrite("config", map[string]any{"url": harborURL, "username": username, "password": "SvcPassword1"})
	m.mustWrite("roles/ci", map[string]any{"level": "project", "permissions": mustJSON(pullPerms(project)), "ttl": "10m", "max_ttl": "1h"})
	m.mustRead("creds/ci")

	_, err = m.write("config/rotate-root", nil)
	require.NoError(t, err)
	s := m.mustRead("config")
	require.Contains(t, s.Data, "last_rotated")

	// old password no longer works in Harbor
	oldClient, err := harbor.New(harbor.Config{URL: harborURL, Username: username, Password: "SvcPassword1"})
	require.NoError(t, err)
	_, err = oldClient.CurrentUser(context.Background())
	require.True(t, harbor.IsUnauthorized(err), "old password must be rejected: %v", err)

	// engine keeps working with the rotated password
	m.mustRead("creds/ci")

	// second rotation also works
	_, err = m.write("config/rotate-root", nil)
	require.NoError(t, err)
	m.mustRead("creds/ci")
}

func TestRoles_CRUD(t *testing.T) {
	m := vault.mount(t)
	m.mustWrite("roles/a", map[string]any{"level": "project", "permissions": mustJSON(pullPerms("library")), "ttl": "1h", "max_ttl": "24h"})
	m.mustWrite("roles/b", map[string]any{"level": "system", "permissions": []any{
		map[string]any{"kind": "system", "namespace": "/", "access": []any{map[string]any{"resource": "project", "action": "list"}}},
	}})
	s := m.mustRead("roles/a")
	require.Equal(t, "project", s.Data["level"])
	require.Equal(t, json.Number("3600"), s.Data["ttl"])
	list, err := vault.client.Logical().List(m.path + "/roles")
	require.NoError(t, err)
	require.ElementsMatch(t, []any{"a", "b"}, list.Data["keys"])

	_, err = m.write("roles/bad", map[string]any{"level": "project", "permissions": mustJSON([]map[string]any{{"kind": "system", "namespace": "/", "access": []map[string]any{{"resource": "project", "action": "list"}}}})})
	require.ErrorContains(t, err, "kind")
	_, err = m.write("roles/bad", map[string]any{"level": "project", "permissions": mustJSON(pullPerms("library")), "ttl": "2h", "max_ttl": "1h"})
	require.ErrorContains(t, err, "max_ttl")

	_, err = vault.client.Logical().Delete(m.path + "/roles/a")
	require.NoError(t, err)
	list, err = vault.client.Logical().List(m.path + "/roles")
	require.NoError(t, err)
	require.ElementsMatch(t, []any{"b"}, list.Data["keys"])
}

func TestCreds_UserMode_FullLifecycle(t *testing.T) {
	m := vault.mount(t)
	project, projectID := newProject(t)
	repo := fmtRepo(project, "hello")
	pushTinyImage(t, adminUser, adminPass, repo, "v1")

	m.configureAdmin()
	m.mustWrite("roles/CI_Pull", map[string]any{"level": "project", "permissions": mustJSON(pullPerms(project)), "ttl": "1h", "max_ttl": "2h"})

	s := m.mustRead("creds/CI_Pull")
	require.NotEmpty(t, s.LeaseID)
	require.True(t, s.Renewable)
	require.Equal(t, 3600, s.LeaseDuration)
	username := s.Data["username"].(string)
	secret := s.Data["secret"].(string)
	require.True(t, strings.HasPrefix(username, "robot$"+project+"+vault-ci-pull-"), username)
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte(username+":"+secret)), s.Data["auth"])
	robotID, _ := s.Data["robot_id"].(json.Number).Int64()
	expiresAt, err := time.Parse(time.RFC3339, s.Data["expires_at"].(string))
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(24*time.Hour), expiresAt, 2*time.Minute, "max_ttl=2h → duration 1 day")

	// robot exists in Harbor with the role's permissions
	rb := findRobot(projectRobots(t, projectID), username)
	require.NotNil(t, rb)
	require.Equal(t, robotID, rb.ID)
	require.Equal(t, int64(1), rb.Duration)
	require.Len(t, rb.Permissions, 1)
	require.Equal(t, []harbor.Access{{Resource: "repository", Action: "pull", Effect: "allow"}}, stripEffectDefault(rb.Permissions[0].Access))

	// real registry pull works, push is denied
	require.Equal(t, http.StatusOK, pullManifestStatus(t, username, secret, repo, "v1"))
	require.False(t, canPush(t, username, secret, repo))
	dockerLoginPull(t, username, secret, repo, "v1")

	// renew within max_ttl: fine, no Harbor change needed
	renewed, err := vault.client.Sys().Renew(s.LeaseID, 1800)
	require.NoError(t, err)
	require.Equal(t, 1800, renewed.LeaseDuration)

	// raise the role's max_ttl so a renewal must extend the robot in Harbor
	m.mustWrite("roles/CI_Pull", map[string]any{"max_ttl": "60h", "ttl": "50h"})
	renewed, err = vault.client.Sys().Renew(s.LeaseID, 50*3600)
	require.NoError(t, err)
	require.Greater(t, renewed.LeaseDuration, 3600)
	rb2 := findRobot(projectRobots(t, projectID), username)
	require.NotNil(t, rb2)
	require.Greater(t, rb2.ExpiresAt, rb.ExpiresAt, "Harbor expiry must be extended")
	require.GreaterOrEqual(t, rb2.ExpiresAt, time.Now().Add(time.Duration(renewed.LeaseDuration)*time.Second).Unix()-5)

	// revoke → robot deleted, credentials dead
	require.NoError(t, vault.client.Sys().Revoke(s.LeaseID))
	waitFor(t, 30*time.Second, "robot deletion", func() bool {
		return findRobot(projectRobots(t, projectID), username) == nil
	})
	st := pullManifestStatus(t, username, secret, repo, "v1")
	require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, st)
}

func TestCreds_RobotMode(t *testing.T) {
	// A project-level issuer works on every supported Harbor; a system-level
	// issuer (one issuer for many projects) needs Harbor >= 2.13, where the
	// creator is taken from the security context instead of a per-project lookup.
	t.Run("project-level-issuer", func(t *testing.T) { testCredsRobotMode(t, "project") })
	t.Run("system-level-issuer", func(t *testing.T) {
		if !harborAtLeast(t, 2, 13) {
			t.Skip("system-level issuer robots need Harbor >= 2.13")
		}
		testCredsRobotMode(t, "system")
	})
}

func testCredsRobotMode(t *testing.T, issuerLevel string) {
	m := vault.mount(t)
	project, projectID := newProject(t)
	repo := fmtRepo(project, "hello")
	pushTinyImage(t, adminUser, adminPass, repo, "v1")
	name, secret := issuerRobot(t, project, issuerLevel)
	m.mustWrite("config", map[string]any{"url": harborURL, "username": name, "password": secret, "auth_type": "robot"})

	// role broader than the issuer → Harbor's 403 DENIED passed through
	// (message is "permission scope is invalid…" on 2.15, plain "denied" on 2.12)
	m.mustWrite("roles/push", map[string]any{"level": "project", "permissions": mustJSON(pushPullPerms(project))})
	_, err := m.read("creds/push")
	require.Error(t, err)
	require.Contains(t, err.Error(), "harbor rejected robot creation")
	require.Contains(t, err.Error(), "403 DENIED")

	// role within scope
	m.mustWrite("roles/pull", map[string]any{"level": "project", "permissions": mustJSON(pullPerms(project)), "ttl": "1h", "max_ttl": "2h"})
	s := m.mustRead("creds/pull")
	username := s.Data["username"].(string)
	robotSecret := s.Data["secret"].(string)
	require.Equal(t, http.StatusOK, pullManifestStatus(t, username, robotSecret, repo, "v1"))
	rb := findRobot(projectRobots(t, projectID), username)
	require.NotNil(t, rb)
	require.Equal(t, "robot", rb.CreatorType)

	// renewal that would need extension: capped with a warning instead of failing
	m.mustWrite("roles/pull", map[string]any{"max_ttl": "60h", "ttl": "50h"})
	renewed, err := vault.client.Sys().Renew(s.LeaseID, 50*3600)
	require.NoError(t, err)
	require.NotEmpty(t, renewed.Warnings)
	require.Contains(t, strings.Join(renewed.Warnings, " "), "capped")
	require.LessOrEqual(t, int64(renewed.LeaseDuration), rb.ExpiresAt-time.Now().Unix()+5)

	// revoke works with the issuer robot's robot:delete
	require.NoError(t, vault.client.Sys().Revoke(s.LeaseID))
	waitFor(t, 30*time.Second, "robot deletion", func() bool {
		return findRobot(projectRobots(t, projectID), username) == nil
	})
}

func TestWALRollback_DeletesOrphan(t *testing.T) {
	m := vault.mount(t)
	project, projectID := newProject(t)
	m.configureAdmin()

	// Simulate a crash between "Harbor confirmed creation" and "WAL removed":
	// create the robot directly and inject a matching, already-old WAL entry
	// into the mount's storage via sys/raw. The plugin was registered with
	// VAULT_PLUGIN_SECRETS_HARBOR_WAL_ROLLBACK_MIN_AGE=1s.
	short := fmt.Sprintf("vault-orphan-%s", runID)
	created, err := admin.CreateRobot(context.Background(), harbor.RobotCreate{
		Name: short, Level: "project", Duration: 30,
		Permissions: []harbor.RobotPermission{{Kind: "project", Namespace: project, Access: []harbor.Access{{Resource: "repository", Action: "pull"}}}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.DeleteRobot(context.Background(), created.ID) })

	walID := "0000-orphan-" + runID
	// Shape of framework.WALEntry: {"type","data","created_at" (unix seconds)}.
	entry := map[string]any{
		"type":       "robot",
		"data":       map[string]any{"name": short, "level": "project", "namespace": project, "role": "ci", "robot_id": 0},
		"created_at": time.Now().Add(-time.Hour).Unix(),
	}
	raw, _ := json.Marshal(entry)
	_, err = vault.client.Logical().Write("sys/raw/logical/"+m.uuid()+"/wal/"+walID, map[string]any{"value": string(raw)})
	require.NoError(t, err)

	// The rollback manager runs roughly once a minute per mount.
	waitFor(t, 3*time.Minute, "WAL rollback to delete the orphaned robot", func() bool {
		return findRobot(projectRobots(t, projectID), created.Name) == nil
	})
	// And the WAL entry is gone.
	waitFor(t, time.Minute, "WAL entry removal", func() bool {
		s, err := vault.client.Logical().Read("sys/raw/logical/" + m.uuid() + "/wal/" + walID)
		return err == nil && s == nil
	})
}

// dockerLoginPull runs a real `docker login` + `docker pull` when INTEGRATION_DOCKER=1.
func dockerLoginPull(t *testing.T, username, secret, repo, tag string) {
	t.Helper()
	if os.Getenv("INTEGRATION_DOCKER") != "1" {
		t.Log("INTEGRATION_DOCKER != 1; skipping docker CLI check")
		return
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatal("INTEGRATION_DOCKER=1 but docker CLI not found")
	}
	cfgDir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("docker", args...)
		cmd.Env = append(os.Environ(), "DOCKER_CONFIG="+cfgDir)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "docker %s: %s", strings.Join(args, " "), out)
		return string(out)
	}
	run("login", harborRegistry, "-u", username, "-p", secret)
	image := harborRegistry + "/" + repo + ":" + tag
	run("pull", image)
	run("rmi", image)
	run("logout", harborRegistry)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// stripEffectDefault normalizes Harbor's returned effect ("" vs "allow").
func stripEffectDefault(a []harbor.Access) []harbor.Access {
	out := make([]harbor.Access, len(a))
	for i, x := range a {
		if x.Effect == "" {
			x.Effect = "allow"
		}
		out[i] = x
	}
	return out
}

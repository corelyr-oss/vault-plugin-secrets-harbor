//go:build integration

// Package integration exercises the plugin end-to-end against a real Harbor
// (see harbor/up.sh) and a real Vault or OpenBao dev server.
//
// Environment:
//
//	HARBOR_URL              Harbor API base URL (default http://127.0.0.1:8090)
//	HARBOR_REGISTRY         registry host:port for docker CLI (default localhost:8090)
//	HARBOR_ADMIN_USER       default admin
//	HARBOR_ADMIN_PASSWORD   default Harbor12345
//	VAULT_BIN               vault or bao binary (default "vault")
//	INTEGRATION_DOCKER      "1" to also run docker login/pull with issued creds
package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
	"github.com/stretchr/testify/require"

	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/harbor"
)

const (
	pluginName    = "vault-plugin-secrets-harbor"
	pluginVersion = "v0.0.0-integration"
	adminPassword = "Harbor12345"
)

var (
	harborURL      = envOr("HARBOR_URL", "http://127.0.0.1:8090")
	harborRegistry = envOr("HARBOR_REGISTRY", "localhost:8090")
	adminUser      = envOr("HARBOR_ADMIN_USER", "admin")
	adminPass      = envOr("HARBOR_ADMIN_PASSWORD", adminPassword)
	vaultBin       = envOr("VAULT_BIN", "vault")

	pluginDir string       // holds the built plugin binary
	pluginSHA string       // sha256 of the binary
	vault     *vaultServer // shared dev server
	admin     *harbor.Client
	runID     string
)

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration setup failed:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	ctx := context.Background()
	var err error
	admin, err = harbor.New(harbor.Config{URL: harborURL, Username: adminUser, Password: adminPass})
	if err != nil {
		return 1, err
	}
	if _, err := admin.CurrentUser(ctx); err != nil {
		return 1, fmt.Errorf("harbor at %s not reachable as admin (start it with test/integration/harbor/up.sh): %w", harborURL, err)
	}
	runID = fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)

	pluginDir, err = os.MkdirTemp("", "harbor-plugin-it-")
	if err != nil {
		return 1, err
	}
	defer os.RemoveAll(pluginDir)
	// Vault compares the plugin path against the configured directory after
	// resolving symlinks (macOS: /var -> /private/var).
	if pluginDir, err = filepath.EvalSymlinks(pluginDir); err != nil {
		return 1, err
	}
	if err := buildPlugin(pluginDir); err != nil {
		return 1, err
	}

	vault, err = startVault(pluginDir)
	if err != nil {
		return 1, err
	}
	defer vault.stop()
	if err := vault.registerPlugin(); err != nil {
		return 1, err
	}
	return m.Run(), nil
}

// buildPlugin compiles the plugin with a fixed semver version.
func buildPlugin(dir string) error {
	out := filepath.Join(dir, pluginName)
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-X main.version="+pluginVersion, "-o", out, "../../cmd/"+pluginName)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("building plugin: %w\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	pluginSHA = hex.EncodeToString(sum[:])
	return nil
}

// ---- Vault / OpenBao dev server ------------------------------------------------

type vaultServer struct {
	cmd     *exec.Cmd
	addr    string
	client  *vaultapi.Client
	logFile *os.File
}

func startVault(pluginDir string) (*vaultServer, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	cfgDir, err := os.MkdirTemp("", "harbor-plugin-vault-")
	if err != nil {
		return nil, err
	}
	// raw_storage_endpoint lets the WAL test inject an entry into the plugin's storage.
	cfgFile := filepath.Join(cfgDir, "dev.hcl")
	if err := os.WriteFile(cfgFile, []byte("raw_storage_endpoint = true\n"), 0o600); err != nil {
		return nil, err
	}
	logFile, err := os.Create(filepath.Join(cfgDir, "server.log"))
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(vaultBin, "server", "-dev",
		"-dev-listen-address="+addr,
		"-dev-root-token-id=root",
		"-dev-plugin-dir="+pluginDir,
		"-config="+cfgFile,
		"-log-level=info")
	// Never inherit a developer's VAULT_* environment.
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "VAULT_") && !strings.HasPrefix(kv, "BAO_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", vaultBin, err)
	}
	vs := &vaultServer{cmd: cmd, addr: addr, logFile: logFile}

	cfg := vaultapi.DefaultConfig()
	cfg.Address = "http://" + addr
	client, err := vaultapi.NewClient(cfg)
	if err != nil {
		vs.stop()
		return nil, err
	}
	client.SetToken("root")
	vs.client = client

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := client.Sys().Health(); err == nil {
			return vs, nil
		}
		if cmd.ProcessState != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	vs.stop()
	logs, _ := os.ReadFile(logFile.Name())
	return nil, fmt.Errorf("%s did not become healthy; log:\n%s", vaultBin, tail(string(logs), 40))
}

func (v *vaultServer) stop() {
	if v.cmd != nil && v.cmd.Process != nil {
		_ = v.cmd.Process.Kill()
		_ = v.cmd.Wait()
	}
	if v.logFile != nil {
		_ = v.logFile.Close()
	}
}

func (v *vaultServer) registerPlugin() error {
	// -dev-plugin-dir already auto-registers plugins found in the directory
	// (with their self-reported version); registering explicitly by SHA and
	// version mirrors what operators do and exercises the version check.
	err := v.client.Sys().RegisterPluginWithContext(context.Background(), &vaultapi.RegisterPluginInput{
		Name:    pluginName,
		Type:    vaultapi.PluginTypeSecrets,
		Command: pluginName,
		SHA256:  pluginSHA,
		Version: pluginVersion,
		Env:     []string{"VAULT_PLUGIN_SECRETS_HARBOR_WAL_ROLLBACK_MIN_AGE=1s"},
	})
	if err != nil {
		return fmt.Errorf("registering plugin: %w", err)
	}
	return nil
}

// mount enables the plugin at a fresh path and returns a helper bound to it.
func (v *vaultServer) mount(t *testing.T) *mount {
	t.Helper()
	path := fmt.Sprintf("harbor-%s-%s", strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")), runID)
	err := v.client.Sys().Mount(path, &vaultapi.MountInput{
		Type:    pluginName,
		Options: map[string]string{},
		Config:  vaultapi.MountConfigInput{DefaultLeaseTTL: "1h", MaxLeaseTTL: "72h"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = v.client.Sys().Unmount(path) })
	return &mount{t: t, c: v.client, path: path}
}

type mount struct {
	t    *testing.T
	c    *vaultapi.Client
	path string
}

func (m *mount) write(p string, data map[string]any) (*vaultapi.Secret, error) {
	return m.c.Logical().Write(m.path+"/"+p, data)
}

func (m *mount) mustWrite(p string, data map[string]any) *vaultapi.Secret {
	m.t.Helper()
	s, err := m.write(p, data)
	require.NoError(m.t, err, "write %s/%s", m.path, p)
	return s
}

func (m *mount) read(p string) (*vaultapi.Secret, error) {
	return m.c.Logical().Read(m.path + "/" + p)
}

func (m *mount) mustRead(p string) *vaultapi.Secret {
	m.t.Helper()
	s, err := m.read(p)
	require.NoError(m.t, err, "read %s/%s", m.path, p)
	require.NotNil(m.t, s, "read %s/%s returned nil", m.path, p)
	return s
}

func (m *mount) uuid() string {
	m.t.Helper()
	mounts, err := m.c.Sys().ListMounts()
	require.NoError(m.t, err)
	mo, ok := mounts[m.path+"/"]
	require.True(m.t, ok, "mount %s not found", m.path)
	return mo.UUID
}

func (m *mount) configureAdmin() {
	m.t.Helper()
	m.mustWrite("config", map[string]any{"url": harborURL, "username": adminUser, "password": adminPass})
}

// ---- Harbor fixtures -----------------------------------------------------------

// newProject creates a private project for the test and returns its name/id.
func newProject(t *testing.T) (string, int64) {
	t.Helper()
	name := fmt.Sprintf("vault-it-%s-%s", strings.ToLower(sanitize(t.Name())), runID)
	if len(name) > 60 {
		name = name[:60]
	}
	require.NoError(t, adminPost("/projects", map[string]any{"project_name": name, "public": false}, nil))
	p, err := admin.GetProject(context.Background(), name)
	require.NoError(t, err)
	t.Cleanup(func() { _ = adminDelete("/projects/" + name) })
	return name, p.ProjectID
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func pullPerms(project string) []map[string]any {
	return []map[string]any{{"kind": "project", "namespace": project,
		"access": []map[string]any{{"resource": "repository", "action": "pull"}}}}
}

func pushPullPerms(project string) []map[string]any {
	return []map[string]any{{"kind": "project", "namespace": project,
		"access": []map[string]any{{"resource": "repository", "action": "pull"}, {"resource": "repository", "action": "push"}}}}
}

// issuerRobot creates a system-level robot able to manage robots in project.
func issuerRobot(t *testing.T, project string) (name, secret string) {
	t.Helper()
	created, err := admin.CreateRobot(context.Background(), harbor.RobotCreate{
		Name: fmt.Sprintf("vault-issuer-%s", runID), Level: "system", Duration: -1,
		Permissions: []harbor.RobotPermission{{Kind: "project", Namespace: project, Access: []harbor.Access{
			{Resource: "robot", Action: "create"}, {Resource: "robot", Action: "read"},
			{Resource: "robot", Action: "list"}, {Resource: "robot", Action: "delete"},
			{Resource: "repository", Action: "pull"},
		}}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.DeleteRobot(context.Background(), created.ID) })
	return created.Name, created.Secret
}

// projectRobots lists the project's robots as admin.
func projectRobots(t *testing.T, projectID int64) []harbor.Robot {
	t.Helper()
	robots, err := admin.ListRobots(context.Background(), harbor.ListRobotsOptions{ProjectID: projectID, PageSize: 100})
	require.NoError(t, err)
	return robots
}

func findRobot(robots []harbor.Robot, fullName string) *harbor.Robot {
	for i := range robots {
		if robots[i].Name == fullName {
			return &robots[i]
		}
	}
	return nil
}

// ---- misc ---------------------------------------------------------------------

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func tail(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

package backend

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hashicorp/vault/sdk/logical"
	"github.com/stretchr/testify/require"

	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/harbor"
	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/harbor/harbortest"
)

const testMount = "harbor/"

type testEnv struct {
	t       *testing.T
	ctx     context.Context
	b       *harborBackend
	storage logical.Storage
	harbor  *harbortest.Server
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	cfg := logical.TestBackendConfig()
	cfg.StorageView = &logical.InmemStorage{}
	b := newBackend()
	require.NoError(t, b.Setup(context.Background(), cfg))
	hs := harbortest.New()
	t.Cleanup(hs.Close)
	return &testEnv{t: t, ctx: context.Background(), b: b, storage: cfg.StorageView, harbor: hs}
}

func (e *testEnv) req(op logical.Operation, path string, data map[string]any) *logical.Request {
	return &logical.Request{Operation: op, Path: path, Data: data, Storage: e.storage, MountPoint: testMount}
}

// do runs a request and fails the test on transport-level errors; logical
// (user-facing) errors are returned in the response for assertion.
func (e *testEnv) do(op logical.Operation, path string, data map[string]any) *logical.Response {
	e.t.Helper()
	resp, err := e.b.HandleRequest(e.ctx, e.req(op, path, data))
	require.NoError(e.t, err)
	return resp
}

func (e *testEnv) mustOK(op logical.Operation, path string, data map[string]any) *logical.Response {
	e.t.Helper()
	resp := e.do(op, path, data)
	if resp != nil {
		require.False(e.t, resp.IsError(), "unexpected error response: %v", resp.Error())
	}
	return resp
}

func (e *testEnv) mustErr(op logical.Operation, path string, data map[string]any) string {
	e.t.Helper()
	resp := e.do(op, path, data)
	require.NotNil(e.t, resp, "expected an error response, got nil")
	require.True(e.t, resp.IsError(), "expected an error response, got %v", resp.Data)
	return resp.Error().Error()
}

func (e *testEnv) configureUser() {
	e.t.Helper()
	e.mustOK(logical.CreateOperation, "config", map[string]any{
		"url": e.harbor.URL, "username": "admin", "password": "Harbor12345",
	})
}

// issuerPerms is the minimum permission set for a robot-mode issuer on one project.
func issuerPerms(project string, extra ...harbor.Access) []harbor.RobotPermission {
	access := []harbor.Access{
		{Resource: "robot", Action: "create"}, {Resource: "robot", Action: "read"},
		{Resource: "robot", Action: "list"}, {Resource: "robot", Action: "delete"},
	}
	access = append(access, extra...)
	return []harbor.RobotPermission{{Kind: "project", Namespace: project, Access: access}}
}

func (e *testEnv) configureRobot(perms []harbor.RobotPermission) *harbortest.StoredRobot {
	e.t.Helper()
	issuer := e.harbor.AddRobot("vault-issuer", "IssuerSecret1", "system", perms)
	e.mustOK(logical.CreateOperation, "config", map[string]any{
		"url": e.harbor.URL, "username": issuer.Name, "password": "IssuerSecret1", "auth_type": "robot",
	})
	return issuer
}

func pullPerms(project string) []harbor.RobotPermission {
	return []harbor.RobotPermission{{Kind: "project", Namespace: project,
		Access: []harbor.Access{{Resource: "repository", Action: "pull"}}}}
}

func permsJSON(t *testing.T, p []harbor.RobotPermission) string {
	t.Helper()
	b, err := json.Marshal(p)
	require.NoError(t, err)
	return string(b)
}

func (e *testEnv) writeRole(name string, data map[string]any) {
	e.t.Helper()
	e.mustOK(logical.CreateOperation, "roles/"+name, data)
}

func (e *testEnv) creds(role string) *logical.Response {
	e.t.Helper()
	return e.mustOK(logical.ReadOperation, "creds/"+role, nil)
}

func (e *testEnv) renew(secret *logical.Secret, increment time.Duration) (*logical.Response, error) {
	e.t.Helper()
	secret.Increment = increment
	req := &logical.Request{Operation: logical.RenewOperation, Storage: e.storage, Secret: secret, MountPoint: testMount}
	return e.b.HandleRequest(e.ctx, req)
}

func (e *testEnv) revoke(secret *logical.Secret) (*logical.Response, error) {
	e.t.Helper()
	req := &logical.Request{Operation: logical.RevokeOperation, Storage: e.storage, Secret: secret, MountPoint: testMount}
	return e.b.HandleRequest(e.ctx, req)
}

func TestFactory(t *testing.T) {
	cfg := logical.TestBackendConfig()
	cfg.StorageView = &logical.InmemStorage{}
	b, err := Factory(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, b)
	require.Equal(t, logical.TypeLogical, b.Type())
}

package backend

import (
	"testing"

	"github.com/hashicorp/vault/sdk/logical"
	"github.com/stretchr/testify/require"

	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/harbor"
)

func TestRoles_CRUD(t *testing.T) {
	e := newTestEnv(t)
	e.writeRole("a", map[string]any{"level": "project", "permissions": permsJSON(t, pullPerms("library")), "ttl": "1h", "max_ttl": "24h", "description": "pull only"})
	e.writeRole("b", map[string]any{"level": "system", "permissions": permsJSON(t, []harbor.RobotPermission{
		{Kind: "system", Namespace: "/", Access: []harbor.Access{{Resource: "project", Action: "list"}}},
		{Kind: "project", Namespace: "library", Access: []harbor.Access{{Resource: "repository", Action: "push"}, {Resource: "repository", Action: "pull"}}},
		{Kind: "project", Namespace: "other", Access: []harbor.Access{{Resource: "repository", Action: "pull"}}},
	})})

	resp := e.mustOK(logical.ReadOperation, "roles/a", nil)
	require.Equal(t, "project", resp.Data["level"])
	require.Equal(t, int64(3600), resp.Data["ttl"])
	require.Equal(t, int64(86400), resp.Data["max_ttl"])
	require.Equal(t, "pull only", resp.Data["description"])
	perms := resp.Data["permissions"].([]any)
	require.Len(t, perms, 1)
	require.Equal(t, "library", perms[0].(map[string]any)["namespace"])

	list := e.mustOK(logical.ListOperation, "roles/", nil)
	require.ElementsMatch(t, []string{"a", "b"}, list.Data["keys"])

	e.mustOK(logical.DeleteOperation, "roles/a", nil)
	list = e.mustOK(logical.ListOperation, "roles/", nil)
	require.ElementsMatch(t, []string{"b"}, list.Data["keys"])
	require.Nil(t, e.do(logical.ReadOperation, "roles/a", nil))
}

func TestRoles_PermissionsInputForms(t *testing.T) {
	e := newTestEnv(t)
	// native list via API
	e.writeRole("native", map[string]any{"level": "project", "permissions": []any{
		map[string]any{"kind": "project", "namespace": "library", "access": []any{map[string]any{"resource": "repository", "action": "pull"}}},
	}})
	// wrapped object form
	e.writeRole("wrapped", map[string]any{"level": "project", "permissions": `{"permissions":[{"kind":"project","namespace":"library","access":[{"resource":"repository","action":"pull"}]}]}`})
	// unknown field rejected
	msg := e.mustErr(logical.CreateOperation, "roles/bad", map[string]any{"level": "project",
		"permissions": `[{"kind":"project","namespace":"library","acces":[{"resource":"repository","action":"pull"}]}]`})
	require.Contains(t, msg, "invalid permissions")
	msg = e.mustErr(logical.CreateOperation, "roles/bad", map[string]any{"level": "project", "permissions": "not json"})
	require.Contains(t, msg, "invalid permissions")
}

func TestRoles_Validation(t *testing.T) {
	e := newTestEnv(t)
	sysPerm := permsJSON(t, []harbor.RobotPermission{{Kind: "system", Namespace: "/", Access: []harbor.Access{{Resource: "project", Action: "list"}}}})

	msg := e.mustErr(logical.CreateOperation, "roles/x", map[string]any{"level": "project", "permissions": sysPerm})
	require.Contains(t, msg, "kind")

	msg = e.mustErr(logical.CreateOperation, "roles/x", map[string]any{"level": "project", "permissions": permsJSON(t, []harbor.RobotPermission{
		{Kind: "project", Namespace: "a", Access: []harbor.Access{{Resource: "repository", Action: "pull"}}},
		{Kind: "project", Namespace: "b", Access: []harbor.Access{{Resource: "repository", Action: "pull"}}},
	})})
	require.Contains(t, msg, "namespace")

	msg = e.mustErr(logical.CreateOperation, "roles/x", map[string]any{"level": "project", "permissions": permsJSON(t, pullPerms("library")), "ttl": "2h", "max_ttl": "1h"})
	require.Contains(t, msg, "ttl")
	require.Contains(t, msg, "max_ttl")

	msg = e.mustErr(logical.CreateOperation, "roles/x", map[string]any{"level": "project", "permissions": "[]"})
	require.Contains(t, msg, "permissions")

	msg = e.mustErr(logical.CreateOperation, "roles/x", map[string]any{"level": "cluster", "permissions": sysPerm})
	require.Contains(t, msg, "level")

	msg = e.mustErr(logical.CreateOperation, "roles/x", map[string]any{"level": "system", "permissions": `[{"kind":"system","namespace":"/","access":[{"resource":"project"}]}]`})
	require.Contains(t, msg, "action")
	msg = e.mustErr(logical.CreateOperation, "roles/x", map[string]any{"level": "system", "permissions": `[{"kind":"system","namespace":"/","access":[{"action":"list"}]}]`})
	require.Contains(t, msg, "resource")
	msg = e.mustErr(logical.CreateOperation, "roles/x", map[string]any{"level": "system", "permissions": `[{"kind":"system","namespace":"/","access":[{"resource":"project","action":"list","effect":"maybe"}]}]`})
	require.Contains(t, msg, "effect")

	// nothing stored
	require.Nil(t, e.do(logical.ReadOperation, "roles/x", nil))
}

func TestRoles_TTLCappedByMount(t *testing.T) {
	e := newTestEnv(t)
	e.configureUser()
	// Mount max in TestSystemView is 48h.
	e.writeRole("long", map[string]any{"level": "project", "permissions": permsJSON(t, pullPerms("library")), "ttl": "72h", "max_ttl": "100h"})
	resp := e.creds("long")
	require.LessOrEqual(t, resp.Secret.TTL, e.b.System().MaxLeaseTTL())
	require.LessOrEqual(t, resp.Secret.MaxTTL, e.b.System().MaxLeaseTTL())
	// Harbor duration derived from the effective (capped) max TTL: 48h → 2 days.
	robots := e.harbor.Robots()
	require.Len(t, robots, 1)
	require.Equal(t, int64(2), robots[0].Duration)
}

package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"

	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/harbor"
)

const (
	rolesStoragePrefix = "roles/"

	levelSystem  = "system"
	levelProject = "project"
)

// role is a stored role definition.
type role struct {
	Name        string                   `json:"name"`
	Level       string                   `json:"level"`
	Permissions []harbor.RobotPermission `json:"permissions"`
	TTL         time.Duration            `json:"ttl"`
	MaxTTL      time.Duration            `json:"max_ttl"`
	Description string                   `json:"description"`
}

func (r *role) description(mount string) string {
	if r.Description != "" {
		return r.Description
	}
	return fmt.Sprintf("Managed by Vault (%s%s)", mount, r.Name)
}

func getRole(ctx context.Context, s logical.Storage, name string) (*role, error) {
	entry, err := s.Get(ctx, rolesStoragePrefix+name)
	if err != nil {
		return nil, fmt.Errorf("reading role %q: %w", name, err)
	}
	if entry == nil {
		return nil, nil
	}
	r := &role{}
	if err := entry.DecodeJSON(r); err != nil {
		return nil, fmt.Errorf("decoding role %q: %w", name, err)
	}
	if r.Name == "" {
		r.Name = name
	}
	return r, nil
}

func (b *harborBackend) pathRolesList() *framework.Path {
	return &framework.Path{
		Pattern: "roles/?$",
		DisplayAttrs: &framework.DisplayAttributes{
			OperationPrefix: "harbor",
			OperationSuffix: "roles",
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ListOperation: &framework.PathOperation{Callback: b.pathRoleList},
		},
		HelpSynopsis: "List configured roles.",
	}
}

func (b *harborBackend) pathRoles() *framework.Path {
	return &framework.Path{
		Pattern: "roles/" + framework.GenericNameRegex("name"),
		DisplayAttrs: &framework.DisplayAttributes{
			OperationPrefix: "harbor",
			OperationSuffix: "role",
		},
		Fields: map[string]*framework.FieldSchema{
			"name": {
				Type:        framework.TypeLowerCaseString,
				Description: "Name of the role.",
				Required:    true,
			},
			"level": {
				Type:          framework.TypeString,
				Description:   `Robot account level: "system" or "project".`,
				Required:      true,
				AllowedValues: []any{levelSystem, levelProject},
			},
			"permissions": {
				Type: framework.TypeSlice,
				Description: `Harbor robot permissions as a JSON array of objects ` +
					`{"kind": "project"|"system", "namespace": "<project>"|"/", "access": [{"resource": "...", "action": "...", "effect": "allow"|"deny"}]}. ` +
					`May be given as a JSON string (e.g. permissions=@file.json) or as a native list via the API.`,
				Required: true,
			},
			"ttl": {
				Type:        framework.TypeDurationSecond,
				Description: "Default lease TTL for credentials issued with this role. Defaults to the mount's default lease TTL.",
			},
			"max_ttl": {
				Type:        framework.TypeDurationSecond,
				Description: "Maximum lease TTL for credentials issued with this role. Defaults to the mount's max lease TTL. Also sets the robot's expiry in Harbor.",
			},
			"description": {
				Type:        framework.TypeString,
				Description: `Description stored on created robots. Default "Managed by Vault (<mount>/<role>)".`,
			},
		},
		ExistenceCheck: b.roleExistenceCheck,
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ReadOperation:   &framework.PathOperation{Callback: b.pathRoleRead},
			logical.CreateOperation: &framework.PathOperation{Callback: b.pathRoleWrite},
			logical.UpdateOperation: &framework.PathOperation{Callback: b.pathRoleWrite},
			logical.DeleteOperation: &framework.PathOperation{Callback: b.pathRoleDelete},
		},
		HelpSynopsis:    "Manage roles that describe the robot accounts to issue.",
		HelpDescription: pathRolesHelp,
	}
}

func (b *harborBackend) roleExistenceCheck(ctx context.Context, req *logical.Request, d *framework.FieldData) (bool, error) {
	r, err := getRole(ctx, req.Storage, d.Get("name").(string))
	if err != nil {
		return false, err
	}
	return r != nil, nil
}

func (b *harborBackend) pathRoleList(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	names, err := req.Storage.List(ctx, rolesStoragePrefix)
	if err != nil {
		return nil, fmt.Errorf("listing roles: %w", err)
	}
	return logical.ListResponse(names), nil
}

func (b *harborBackend) pathRoleRead(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	r, err := getRole(ctx, req.Storage, d.Get("name").(string))
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, nil
	}
	perms, err := permissionsToData(r.Permissions)
	if err != nil {
		return nil, err
	}
	return &logical.Response{Data: map[string]any{
		"level":       r.Level,
		"permissions": perms,
		"ttl":         int64(r.TTL.Seconds()),
		"max_ttl":     int64(r.MaxTTL.Seconds()),
		"description": r.Description,
	}}, nil
}

func (b *harborBackend) pathRoleWrite(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	name := d.Get("name").(string)
	r, err := getRole(ctx, req.Storage, name)
	if err != nil {
		return nil, err
	}
	if r == nil {
		r = &role{Name: name}
	}

	if v, ok := d.GetOk("level"); ok {
		r.Level = strings.ToLower(v.(string))
	}
	if v, ok := d.GetOk("permissions"); ok {
		perms, err := parsePermissions(v)
		if err != nil {
			return logical.ErrorResponse("invalid permissions: %s", err), nil
		}
		r.Permissions = perms
	}
	if v, ok := d.GetOk("ttl"); ok {
		r.TTL = time.Duration(v.(int)) * time.Second
	}
	if v, ok := d.GetOk("max_ttl"); ok {
		r.MaxTTL = time.Duration(v.(int)) * time.Second
	}
	if v, ok := d.GetOk("description"); ok {
		r.Description = v.(string)
	}

	if errResp := validateRole(r); errResp != nil {
		return errResp, nil
	}

	entry, err := logical.StorageEntryJSON(rolesStoragePrefix+name, r)
	if err != nil {
		return nil, fmt.Errorf("encoding role: %w", err)
	}
	if err := req.Storage.Put(ctx, entry); err != nil {
		return nil, fmt.Errorf("storing role: %w", err)
	}
	return nil, nil
}

func (b *harborBackend) pathRoleDelete(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	if err := req.Storage.Delete(ctx, rolesStoragePrefix+d.Get("name").(string)); err != nil {
		return nil, fmt.Errorf("deleting role: %w", err)
	}
	return nil, nil
}

// validateRole enforces the role rules from the spec and returns an error
// response naming the offending field, or nil.
func validateRole(r *role) *logical.Response {
	if r.Level != levelSystem && r.Level != levelProject {
		return logical.ErrorResponse("invalid level %q: must be %q or %q", r.Level, levelSystem, levelProject)
	}
	if len(r.Permissions) == 0 {
		return logical.ErrorResponse("permissions must contain at least one entry")
	}
	if r.Level == levelProject {
		ns := ""
		for i, p := range r.Permissions {
			if p.Kind != "project" {
				return logical.ErrorResponse("permissions[%d].kind is %q but a project-level role may only contain kind=project permissions", i, p.Kind)
			}
			if ns == "" {
				ns = p.Namespace
			} else if p.Namespace != ns {
				return logical.ErrorResponse("permissions[%d].namespace %q differs from %q: a project-level role must target exactly one project", i, p.Namespace, ns)
			}
		}
	}
	for i, p := range r.Permissions {
		if p.Kind != "project" && p.Kind != "system" {
			return logical.ErrorResponse("permissions[%d].kind must be %q or %q, got %q", i, "project", "system", p.Kind)
		}
		if p.Namespace == "" {
			return logical.ErrorResponse("permissions[%d].namespace is required (project name, or \"/\" for kind=system)", i)
		}
		if len(p.Access) == 0 {
			return logical.ErrorResponse("permissions[%d].access must contain at least one entry", i)
		}
		for j, a := range p.Access {
			if a.Resource == "" {
				return logical.ErrorResponse("permissions[%d].access[%d].resource is required", i, j)
			}
			if a.Action == "" {
				return logical.ErrorResponse("permissions[%d].access[%d].action is required", i, j)
			}
			if a.Effect != "" && a.Effect != "allow" && a.Effect != "deny" {
				return logical.ErrorResponse("permissions[%d].access[%d].effect must be \"allow\" or \"deny\", got %q", i, j, a.Effect)
			}
		}
	}
	if r.TTL < 0 || r.MaxTTL < 0 {
		return logical.ErrorResponse("ttl and max_ttl must not be negative")
	}
	if r.TTL > 0 && r.MaxTTL > 0 && r.TTL > r.MaxTTL {
		return logical.ErrorResponse("ttl (%s) must not exceed max_ttl (%s)", r.TTL, r.MaxTTL)
	}
	return nil
}

// parsePermissions accepts either a JSON string (possibly wrapped by TypeSlice
// as a single-element list) or a native list of maps.
func parsePermissions(v any) ([]harbor.RobotPermission, error) {
	var raw []byte
	switch t := v.(type) {
	case string:
		raw = []byte(t)
	case []any:
		if len(t) == 1 {
			if s, ok := t[0].(string); ok {
				raw = []byte(s)
				break
			}
		}
		b, err := json.Marshal(t)
		if err != nil {
			return nil, err
		}
		raw = b
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty value")
	}
	var perms []harbor.RobotPermission
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&perms); err != nil {
		// Accept {"permissions": [...]} for convenience.
		var wrapped struct {
			Permissions []harbor.RobotPermission `json:"permissions"`
		}
		dec2 := json.NewDecoder(strings.NewReader(string(raw)))
		dec2.DisallowUnknownFields()
		if err2 := dec2.Decode(&wrapped); err2 != nil || wrapped.Permissions == nil {
			return nil, fmt.Errorf("expected a JSON array of robot permissions: %w", err)
		}
		perms = wrapped.Permissions
	}
	return perms, nil
}

func permissionsToData(perms []harbor.RobotPermission) ([]any, error) {
	b, err := json.Marshal(perms)
	if err != nil {
		return nil, err
	}
	var out []any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

const pathRolesHelp = `
A role describes the robot accounts issued at creds/<name>: their level,
Harbor permissions and lease TTLs.

Project-level roles hold exactly one kind=project permission for one project.
System-level roles may hold any mix of kind=system and kind=project
permissions across projects (Harbor >= 2.2).

Example permissions for a pull-only CI role on project "library":
  [{"kind":"project","namespace":"library",
    "access":[{"resource":"repository","action":"pull"}]}]
`

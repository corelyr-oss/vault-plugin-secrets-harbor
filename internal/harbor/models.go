package harbor

// Models mirror the subset of the Harbor v2.0 REST API (swagger 2.15) that the
// plugin uses. Field names and JSON tags are copied verbatim from the API.

// Access is a single resource/action rule inside a robot permission.
type Access struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
	Effect   string `json:"effect,omitempty"`
}

// RobotPermission scopes a set of Access rules to a project or the system.
type RobotPermission struct {
	Kind      string   `json:"kind"`      // "project" | "system"
	Namespace string   `json:"namespace"` // project name, or "/" for system
	Access    []Access `json:"access"`
}

// RobotCreate is the request body for POST /robots.
type RobotCreate struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Secret      string            `json:"secret,omitempty"`
	Level       string            `json:"level"` // "project" | "system"
	Disable     bool              `json:"disable"`
	Duration    int64             `json:"duration"` // days; -1 = never
	Permissions []RobotPermission `json:"permissions"`
}

// RobotCreated is the response of POST /robots.
type RobotCreated struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Secret       string `json:"secret"`
	CreationTime string `json:"creation_time"`
	ExpiresAt    int64  `json:"expires_at"`
}

// Robot is the resource returned by GET /robots/{id} and used by PUT /robots/{id}.
type Robot struct {
	ID           int64             `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Secret       string            `json:"secret,omitempty"`
	Level        string            `json:"level"`
	Duration     int64             `json:"duration"`
	Editable     bool              `json:"editable"`
	Disable      bool              `json:"disable"`
	ExpiresAt    int64             `json:"expires_at"`
	Permissions  []RobotPermission `json:"permissions"`
	CreatorType  string            `json:"creator_type,omitempty"`
	CreatorRef   int64             `json:"creator_ref,omitempty"`
	CreationTime string            `json:"creation_time"`
	UpdateTime   string            `json:"update_time"`
}

// RobotSec is the request/response body of PATCH /robots/{id}.
type RobotSec struct {
	Secret string `json:"secret"`
}

// Project is the (subset of the) response of GET /projects/{name_or_id}.
type Project struct {
	ProjectID int64  `json:"project_id"`
	Name      string `json:"name"`
	Public    bool   `json:"-"`
}

// UserResp is the (subset of the) response of GET /users/current.
type UserResp struct {
	UserID       int64  `json:"user_id"`
	Username     string `json:"username"`
	Email        string `json:"email"`
	Realname     string `json:"realname"`
	SysadminFlag bool   `json:"sysadmin_flag"`
}

// PasswordReq is the request body of PUT /users/{id}/password.
type PasswordReq struct {
	OldPassword string `json:"old_password,omitempty"`
	NewPassword string `json:"new_password"`
}

// errorsEnvelope is Harbor's standard error response.
type errorsEnvelope struct {
	Errors []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// Package harbortest provides an in-memory fake of the Harbor robot/user API
// surface used by the plugin. It enforces the same validation rules as Harbor
// (robot name regex, secret policy, duration semantics, robot prefix, project
// "+" naming, and creator-scope checks for robots created by robots) so unit
// tests catch spec violations without Docker.
package harbortest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/harbor"
)

var (
	robotNameRe = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	hasLower    = regexp.MustCompile(`[a-z]`)
	hasUpper    = regexp.MustCompile(`[A-Z]`)
	hasNumber   = regexp.MustCompile(`\d`)
)

// robotActions is the set of actions Harbor accepts for the "robot" resource
// (identical for project and system scope, Harbor 2.15). Note: no "update".
var robotActions = map[string]bool{"create": true, "read": true, "list": true, "delete": true}

// IsValidSecret mirrors Harbor's robot secret / password policy.
func IsValidSecret(s string) bool {
	return len(s) >= 8 && len(s) <= 128 && hasLower.MatchString(s) && hasUpper.MatchString(s) && hasNumber.MatchString(s)
}

// User is a Harbor local user known to the fake.
type User struct {
	ID           int64
	Username     string
	Password     string
	Sysadmin     bool
	ProjectAdmin []string // projects the user administers (non-sysadmin)
}

// StoredRobot is a robot as kept by the fake, including its secret and creator.
type StoredRobot struct {
	harbor.Robot
	Secret      string
	CreatorType string // "local" | "robot"
	CreatorRef  int64
	Created     time.Time
}

// Server is a fake Harbor.
type Server struct {
	*httptest.Server

	// RobotPrefix is Harbor's configurable robot name prefix. Default "robot$".
	RobotPrefix string
	// Now lets tests control time. Defaults to time.Now.
	Now func() time.Time
	// FailNext, if set, makes the next matching request fail with the given
	// status and message. Key is "METHOD /path-prefix", e.g. "POST /robots".
	FailNext map[string]Failure

	mu       sync.Mutex
	users    map[string]*User
	robots   map[int64]*StoredRobot
	projects map[string]int64 // name -> id
	nextID   int64
	requests []string
}

// Failure describes an injected failure.
type Failure struct {
	Status  int
	Code    string
	Message string
}

// New starts a fake Harbor with an admin user (admin / Harbor12345).
func New() *Server {
	s := &Server{
		RobotPrefix: "robot$",
		Now:         time.Now,
		FailNext:    map[string]Failure{},
		users:       map[string]*User{},
		robots:      map[int64]*StoredRobot{},
		projects:    map[string]int64{"library": 1},
		nextID:      1,
	}
	s.AddUser(&User{Username: "admin", Password: "Harbor12345", Sysadmin: true})
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// NewTLS is like New but serves TLS with httptest's self-signed certificate.
func NewTLS() *Server {
	s := New()
	s.Close()
	s.Server = httptest.NewTLSServer(http.HandlerFunc(s.handle))
	return s
}

// AddUser registers a user; the ID is assigned if zero.
func (s *Server) AddUser(u *User) *User {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u.ID == 0 {
		u.ID = s.nextID
		s.nextID++
	}
	s.users[u.Username] = u
	return u
}

// AddProject registers a project and returns its id.
func (s *Server) AddProject(name string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.projects[name]; ok {
		return id
	}
	id := int64(len(s.projects) + 1)
	s.projects[name] = id
	return id
}

// AddRobot registers a pre-existing robot (e.g. an issuer robot for robot-mode
// tests). name is the short name; the full name is derived. Returns the robot.
func (s *Server) AddRobot(name, secret, level string, perms []harbor.RobotPermission) *StoredRobot {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.newRobotLocked(name, secret, level, "", -1, perms, "local", 1)
	return r
}

// Robots returns a snapshot of all robots.
func (s *Server) Robots() []StoredRobot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]StoredRobot, 0, len(s.robots))
	for _, r := range s.robots {
		out = append(out, *r)
	}
	return out
}

// Robot returns a robot by id, or nil.
func (s *Server) Robot(id int64) *StoredRobot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.robots[id]; ok {
		cp := *r
		return &cp
	}
	return nil
}

// User returns a user by name, or nil.
func (s *Server) User(name string) *User {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.users[name]; ok {
		cp := *u
		return &cp
	}
	return nil
}

// Requests returns "METHOD /path" for every request served so far.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// principal is the authenticated caller.
type principal struct {
	user  *User
	robot *StoredRobot
}

func (s *Server) newRobotLocked(name, secret, level, desc string, duration int64, perms []harbor.RobotPermission, creatorType string, creatorRef int64) *StoredRobot {
	full := s.RobotPrefix + name
	if level == "project" && len(perms) > 0 {
		full = s.RobotPrefix + perms[0].Namespace + "+" + name
	}
	now := s.Now()
	exp := int64(-1)
	if duration > 0 {
		exp = now.AddDate(0, 0, int(duration)).Unix()
	}
	r := &StoredRobot{
		Robot: harbor.Robot{
			ID:           s.nextID,
			Name:         full,
			Description:  desc,
			Level:        level,
			Duration:     duration,
			Editable:     true,
			ExpiresAt:    exp,
			Permissions:  perms,
			CreatorType:  creatorType,
			CreatorRef:   creatorRef,
			CreationTime: now.UTC().Format(time.RFC3339Nano),
			UpdateTime:   now.UTC().Format(time.RFC3339Nano),
		},
		Secret:      secret,
		CreatorType: creatorType,
		CreatorRef:  creatorRef,
		Created:     now,
	}
	s.nextID++
	s.robots[r.ID] = r
	return r
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	for key, f := range s.FailNext {
		parts := strings.SplitN(key, " ", 2)
		if len(parts) == 2 && parts[0] == r.Method && strings.HasPrefix(r.URL.Path, "/api/v2.0"+parts[1]) {
			delete(s.FailNext, key)
			s.mu.Unlock()
			writeErr(w, f.Status, f.Code, f.Message)
			return
		}
	}
	s.mu.Unlock()

	path := strings.TrimPrefix(r.URL.Path, "/api/v2.0")
	if path == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	switch {
	case path == "/ping" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("Pong"))
	case path == "/users/current" && r.Method == http.MethodGet:
		s.usersCurrent(w, r)
	case strings.HasPrefix(path, "/users/") && strings.HasSuffix(path, "/password") && r.Method == http.MethodPut:
		s.usersPassword(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/users/"), "/password"))
	case strings.HasPrefix(path, "/projects/") && r.Method == http.MethodGet:
		s.projectGet(w, r, strings.TrimPrefix(path, "/projects/"))
	case path == "/robots" && r.Method == http.MethodGet:
		s.robotsList(w, r)
	case path == "/robots" && r.Method == http.MethodPost:
		s.robotsCreate(w, r)
	case strings.HasPrefix(path, "/robots/"):
		s.robotByID(w, r, strings.TrimPrefix(path, "/robots/"))
	default:
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "route not found: "+path)
	}
}

func (s *Server) authenticate(r *http.Request) (*principal, bool) {
	name, pass, ok := r.BasicAuth()
	if !ok {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.HasPrefix(name, s.RobotPrefix) {
		for _, rb := range s.robots {
			if rb.Name == name && rb.Secret == pass && !rb.Disable && (rb.ExpiresAt == -1 || rb.ExpiresAt > s.Now().Unix()) {
				return &principal{robot: rb}, true
			}
		}
		return nil, false
	}
	if u, ok := s.users[name]; ok && u.Password == pass {
		return &principal{user: u}, true
	}
	return nil, false
}

func (s *Server) usersCurrent(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(r)
	if !ok || p.user == nil {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, harbor.UserResp{UserID: p.user.ID, Username: p.user.Username, SysadminFlag: p.user.Sysadmin})
}

func (s *Server) usersPassword(w http.ResponseWriter, r *http.Request, idStr string) {
	p, ok := s.authenticate(r)
	if !ok || p.user == nil {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	id, _ := strconv.ParseInt(idStr, 10, 64)
	var req harbor.PasswordReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "invalid body")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var target *User
	for _, u := range s.users {
		if u.ID == id {
			target = u
		}
	}
	if target == nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "user not found")
		return
	}
	if target.ID != p.user.ID && !p.user.Sysadmin {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "forbidden")
		return
	}
	if !p.user.Sysadmin || target.ID == p.user.ID {
		if req.OldPassword != target.Password {
			writeErr(w, http.StatusForbidden, "FORBIDDEN", "old password is incorrect")
			return
		}
	}
	if !IsValidSecret(req.NewPassword) {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "password does not meet requirement")
		return
	}
	target.Password = req.NewPassword
	w.WriteHeader(http.StatusOK)
}

func (s *Server) projectGet(w http.ResponseWriter, r *http.Request, name string) {
	if _, ok := s.authenticate(r); !ok && r.Header.Get("Authorization") != "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.projects[name]
	if !ok {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "project "+name+" not found")
		return
	}
	writeJSON(w, http.StatusOK, harbor.Project{ProjectID: id, Name: name})
}

// robotsList mirrors Harbor's GET /robots: without a Level=project filter only
// system-level robots are returned; robot principals need the project filter
// (and robot:list there) or get 403; q supports name=<exact> and name=~<fuzzy>.
func (s *Server) robotsList(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	var (
		levelProject bool
		projectID    int64
		nameExact    string
		nameFuzzy    string
	)
	for _, kv := range strings.Split(r.URL.Query().Get("q"), ",") {
		k, v, found := strings.Cut(kv, "=")
		if !found {
			continue
		}
		switch k {
		case "Level":
			levelProject = v == "project"
		case "ProjectID":
			projectID, _ = strconv.ParseInt(v, 10, 64)
		case "name":
			if strings.HasPrefix(v, "~") {
				nameFuzzy = strings.TrimPrefix(v, "~")
			} else {
				nameExact = v
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var projectName string
	if levelProject {
		for n, id := range s.projects {
			if id == projectID {
				projectName = n
			}
		}
		if projectName == "" {
			writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "project not found")
			return
		}
	}
	if p.robot != nil {
		var target harbor.RobotPermission
		if levelProject {
			target = harbor.RobotPermission{Kind: "project", Namespace: projectName}
		} else {
			target = harbor.RobotPermission{Kind: "system", Namespace: "/"}
		}
		if !holds(p.robot, target, "robot", "list") {
			writeErr(w, http.StatusForbidden, "FORBIDDEN", p.robot.Name)
			return
		}
	}
	out := []harbor.Robot{}
	for _, rb := range s.robots {
		if levelProject != (rb.Level == "project") {
			continue
		}
		if levelProject && (len(rb.Permissions) == 0 || rb.Permissions[0].Namespace != projectName) {
			continue
		}
		dbName := harbor.ShortName(rb.Name)
		if rb.Level == "project" {
			dbName = rb.Permissions[0].Namespace + "+" + dbName
		}
		if nameExact != "" && dbName != nameExact {
			continue
		}
		if nameFuzzy != "" && !strings.Contains(dbName, nameFuzzy) {
			continue
		}
		cp := rb.Robot
		cp.Secret = ""
		out = append(out, cp)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) robotsCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	var req harbor.RobotCreate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "invalid body")
		return
	}
	if !robotNameRe.MatchString(req.Name) {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "robot name should contain lower case alphanumeric characters, and can be separated by '.', '_' or '-'")
		return
	}
	if req.Level != "system" && req.Level != "project" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "bad request permission level "+req.Level)
		return
	}
	if req.Duration != -1 && req.Duration <= 0 {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "duration must be either -1 or a positive integer")
		return
	}
	if len(req.Permissions) == 0 {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "permissions must not be empty")
		return
	}
	if req.Level == "project" {
		if len(req.Permissions) != 1 || req.Permissions[0].Kind != "project" {
			writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "bad request permission: project level robot must have exactly one project permission")
			return
		}
	}
	for _, perm := range req.Permissions {
		if perm.Kind != "project" && perm.Kind != "system" {
			writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "bad request permission kind "+perm.Kind)
			return
		}
		if perm.Kind == "project" {
			if _, ok := s.projectID(perm.Namespace); !ok {
				writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "project "+perm.Namespace+" not found")
				return
			}
		}
		for _, a := range perm.Access {
			if a.Resource == "" || a.Action == "" {
				writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "bad request permission: resource and action are required")
				return
			}
			if a.Resource == "robot" && !robotActions[a.Action] {
				writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "bad request permission: robot:"+a.Action)
				return
			}
		}
	}
	if req.Secret != "" && !IsValidSecret(req.Secret) {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "the secret must be longer than 8 chars with at least 1 uppercase letter, 1 lowercase letter and 1 number")
		return
	}

	// authorization
	creatorType, creatorRef := "local", int64(0)
	switch {
	case p.user != nil:
		creatorRef = p.user.ID
		if !p.user.Sysadmin {
			if req.Level == "system" {
				writeErr(w, http.StatusForbidden, "FORBIDDEN", "forbidden")
				return
			}
			if !contains(p.user.ProjectAdmin, req.Permissions[0].Namespace) {
				writeErr(w, http.StatusForbidden, "FORBIDDEN", "forbidden")
				return
			}
		}
	case p.robot != nil:
		creatorType, creatorRef = "robot", p.robot.ID
		if req.Level == "system" && !holds(p.robot, harbor.RobotPermission{Kind: "system", Namespace: "/"}, "robot", "create") {
			writeErr(w, http.StatusForbidden, "FORBIDDEN", p.robot.Name)
			return
		}
		if !hasAccess(p.robot, "robot", "create", req.Permissions) {
			writeErr(w, http.StatusForbidden, "FORBIDDEN", p.robot.Name)
			return
		}
		if !scopeCovers(p.robot, req.Permissions) {
			writeErr(w, http.StatusForbidden, "DENIED", "permission scope is invalid. It must be equal to or more restrictive than the creator robot's permissions: "+p.robot.Name)
			return
		}
	}

	secret := req.Secret
	if secret == "" {
		secret = "Aa1" + randomHex(20)
	}
	s.mu.Lock()
	for _, rb := range s.robots {
		if rb.Name == s.RobotPrefix+req.Name || (req.Level == "project" && rb.Name == s.RobotPrefix+req.Permissions[0].Namespace+"+"+req.Name) {
			s.mu.Unlock()
			writeErr(w, http.StatusConflict, "CONFLICT", "robot account "+req.Name+" already exists")
			return
		}
	}
	rb := s.newRobotLocked(req.Name, secret, req.Level, req.Description, req.Duration, req.Permissions, creatorType, creatorRef)
	rb.Disable = req.Disable
	s.mu.Unlock()

	w.Header().Set("Location", "/api/v2.0/robots/"+strconv.FormatInt(rb.ID, 10))
	writeJSON(w, http.StatusCreated, harbor.RobotCreated{
		ID: rb.ID, Name: rb.Name, Secret: secret, CreationTime: rb.CreationTime, ExpiresAt: rb.ExpiresAt,
	})
}

func (s *Server) robotByID(w http.ResponseWriter, r *http.Request, idStr string) {
	p, ok := s.authenticate(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "invalid robot id")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rb, ok := s.robots[id]
	if !ok {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("robot %d not found", id))
		return
	}
	if p.robot != nil {
		// Access to a system-level robot needs system-scope robot:<action>;
		// a project-level robot needs it in that project. No robot:update exists.
		target := harbor.RobotPermission{Kind: "system", Namespace: "/"}
		if rb.Level == "project" && len(rb.Permissions) > 0 {
			target = harbor.RobotPermission{Kind: "project", Namespace: rb.Permissions[0].Namespace}
		}
		action := actionFor(r.Method)
		if action == "update" || !holds(p.robot, target, "robot", action) {
			writeErr(w, http.StatusForbidden, "FORBIDDEN", "forbidden")
			return
		}
	}
	switch r.Method {
	case http.MethodGet:
		cp := rb.Robot
		cp.Secret = ""
		writeJSON(w, http.StatusOK, cp)
	case http.MethodPut:
		var req harbor.Robot
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "invalid body")
			return
		}
		if req.Level != rb.Level || req.Name != rb.Name {
			writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "cannot update the level or name of robot")
			return
		}
		if req.Duration != -1 && req.Duration <= 0 {
			writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "duration must be either -1 or a positive integer")
			return
		}
		if req.Duration != rb.Duration {
			rb.Duration = req.Duration
			if req.Duration == -1 {
				rb.ExpiresAt = -1
			} else {
				rb.ExpiresAt = rb.Created.AddDate(0, 0, int(req.Duration)).Unix()
			}
		}
		rb.Description = req.Description
		rb.Disable = req.Disable
		if len(req.Permissions) > 0 {
			rb.Permissions = req.Permissions
		}
		rb.UpdateTime = s.Now().UTC().Format(time.RFC3339Nano)
		w.WriteHeader(http.StatusOK)
	case http.MethodPatch:
		var req harbor.RobotSec
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "invalid body")
			return
		}
		secret := req.Secret
		if secret == "" {
			secret = "Aa1" + randomHex(20)
		} else if !IsValidSecret(secret) {
			writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "the secret must be longer than 8 chars with at least 1 uppercase letter, 1 lowercase letter and 1 number")
			return
		}
		rb.Secret = secret
		writeJSON(w, http.StatusOK, harbor.RobotSec{Secret: secret})
	case http.MethodDelete:
		delete(s.robots, id)
		w.WriteHeader(http.StatusOK)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (s *Server) projectID(name string) (int64, bool) {
	id, ok := s.projects[name]
	return id, ok
}

// holds reports whether the robot has resource:action in the given scope.
func holds(creator *StoredRobot, target harbor.RobotPermission, resource, action string) bool {
	for _, cp := range creator.Permissions {
		if !namespaceCovers(cp, target) {
			continue
		}
		for _, a := range cp.Access {
			if a.Resource == resource && a.Action == action && a.Effect != "deny" {
				return true
			}
		}
	}
	return false
}

func actionFor(method string) string {
	switch method {
	case http.MethodGet:
		return "read"
	case http.MethodPut, http.MethodPatch:
		return "update"
	case http.MethodDelete:
		return "delete"
	}
	return "create"
}

// hasAccess reports whether the robot holds resource:action for every
// namespace targeted by (level, perms).
func hasAccess(creator *StoredRobot, resource, action string, perms []harbor.RobotPermission) bool {
	for _, target := range perms {
		found := false
		for _, cp := range creator.Permissions {
			if !namespaceCovers(cp, target) {
				continue
			}
			for _, a := range cp.Access {
				if a.Resource == resource && a.Action == action && a.Effect != "deny" {
					found = true
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// scopeCovers mirrors Harbor's rule that a child robot's permissions must be a
// subset of the creator robot's permissions.
func scopeCovers(creator *StoredRobot, perms []harbor.RobotPermission) bool {
	for _, target := range perms {
		for _, a := range target.Access {
			ok := false
			for _, cp := range creator.Permissions {
				if !namespaceCovers(cp, target) {
					continue
				}
				for _, ca := range cp.Access {
					if ca.Resource == a.Resource && ca.Action == a.Action {
						ok = true
					}
				}
			}
			if !ok {
				return false
			}
		}
	}
	return true
}

func namespaceCovers(creator, target harbor.RobotPermission) bool {
	if creator.Kind == "system" {
		return target.Kind == "system"
	}
	if creator.Kind != "project" || target.Kind != "project" {
		return false
	}
	return creator.Namespace == "*" || creator.Namespace == target.Namespace
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"errors": []map[string]string{{"code": code, "message": msg}}})
}

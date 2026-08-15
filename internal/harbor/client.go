// Package harbor is a minimal, dependency-free client for the subset of the
// Harbor v2.0 REST API needed to manage robot accounts.
package harbor

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	apiPrefix      = "/api/v2.0"
	defaultTimeout = 30 * time.Second
	maxErrorBody   = 4 << 10
)

// UserAgent is sent on every request; the plugin overrides it with its version.
var UserAgent = "vault-plugin-secrets-harbor"

// Config configures a Client.
type Config struct {
	URL                string        // Harbor base URL, e.g. https://harbor.example.com
	Username           string        // Harbor user name or full robot name (robot$...)
	Password           string        // user password or robot secret
	CACertPEM          string        // optional PEM bundle for the Harbor TLS certificate
	InsecureSkipVerify bool          // skip TLS verification (not recommended)
	Timeout            time.Duration // per-request timeout; default 30s
}

// Client talks to one Harbor instance with one credential.
type Client struct {
	baseURL  *url.URL
	username string
	password string
	http     *http.Client
}

// New builds a Client. It does not contact Harbor.
func New(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, errors.New("harbor: url is required")
	}
	u, err := url.Parse(strings.TrimRight(cfg.URL, "/"))
	if err != nil {
		return nil, fmt.Errorf("harbor: invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("harbor: url scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("harbor: url must include a host")
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.InsecureSkipVerify {
		tlsCfg.InsecureSkipVerify = true // #nosec G402 -- explicit operator opt-in
	}
	if cfg.CACertPEM != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cfg.CACertPEM)) {
			return nil, errors.New("harbor: ca_cert does not contain any valid PEM certificates")
		}
		tlsCfg.RootCAs = pool
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsCfg
	transport.Proxy = http.ProxyFromEnvironment

	return &Client{
		baseURL:  u,
		username: cfg.Username,
		password: cfg.Password,
		http:     &http.Client{Transport: transport, Timeout: timeout},
	}, nil
}

// Ping calls GET /ping (unauthenticated liveness).
func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/ping", nil, nil, nil)
}

// CurrentUser calls GET /users/current. Only valid for user credentials.
func (c *Client) CurrentUser(ctx context.Context) (*UserResp, error) {
	var out UserResp
	if err := c.do(ctx, http.MethodGet, "/users/current", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ChangeUserPassword calls PUT /users/{id}/password. oldPassword may be empty
// when the caller is a Harbor system administrator.
func (c *Client) ChangeUserPassword(ctx context.Context, userID int64, oldPassword, newPassword string) error {
	body := PasswordReq{OldPassword: oldPassword, NewPassword: newPassword}
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/users/%d/password", userID), nil, body, nil)
}

// CreateRobot calls POST /robots.
func (c *Client) CreateRobot(ctx context.Context, req RobotCreate) (*RobotCreated, error) {
	var out RobotCreated
	if err := c.do(ctx, http.MethodPost, "/robots", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetRobot calls GET /robots/{id}.
func (c *Client) GetRobot(ctx context.Context, id int64) (*Robot, error) {
	var out Robot
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/robots/%d", id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateRobot calls PUT /robots/{id}. Harbor requires the full robot
// (level, name, permissions) in the body; pass a Robot obtained from GetRobot
// with the desired fields (duration, description, disable) modified.
func (c *Client) UpdateRobot(ctx context.Context, robot *Robot) error {
	if robot == nil {
		return errors.New("harbor: robot is nil")
	}
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/robots/%d", robot.ID), nil, robot, nil)
}

// RefreshRobotSecret calls PATCH /robots/{id}. If secret is empty Harbor
// generates one; the effective secret is returned.
func (c *Client) RefreshRobotSecret(ctx context.Context, id int64, secret string) (string, error) {
	var out RobotSec
	if err := c.do(ctx, http.MethodPatch, fmt.Sprintf("/robots/%d", id), nil, RobotSec{Secret: secret}, &out); err != nil {
		return "", err
	}
	if out.Secret == "" {
		out.Secret = secret
	}
	return out.Secret, nil
}

// DeleteRobot calls DELETE /robots/{id}.
func (c *Client) DeleteRobot(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/robots/%d", id), nil, nil, nil)
}

// GetProject calls GET /projects/{name}.
func (c *Client) GetProject(ctx context.Context, name string) (*Project, error) {
	var out Project
	if err := c.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(name), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListRobotsOptions filters GET /robots. Harbor semantics (2.15):
//   - without Level=project the listing contains system-level robots only;
//   - project-level robots require Level=project plus ProjectID;
//   - robot principals may only list with a project filter they hold
//     robot:list on (an unfiltered listing yields 403);
//   - NameFuzzy is a substring match (q=name=~...).
type ListRobotsOptions struct {
	ProjectID int64  // >0 selects project-level robots of that project
	NameFuzzy string // substring match on the short name
	PageSize  int
}

// ListRobots calls GET /robots.
func (c *Client) ListRobots(ctx context.Context, opts ListRobotsOptions) ([]Robot, error) {
	q := url.Values{}
	var parts []string
	if opts.ProjectID > 0 {
		parts = append(parts, "Level=project", fmt.Sprintf("ProjectID=%d", opts.ProjectID))
	}
	if opts.NameFuzzy != "" {
		parts = append(parts, "name=~"+opts.NameFuzzy)
	}
	if len(parts) > 0 {
		q.Set("q", strings.Join(parts, ","))
	}
	if opts.PageSize > 0 {
		q.Set("page_size", fmt.Sprint(opts.PageSize))
	}
	var out []Robot
	if err := c.do(ctx, http.MethodGet, "/robots", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// FindRobotByShortName locates a robot by the short name given at creation
// (without Harbor's prefix or project qualifier). For project-level robots
// project must be the project name; for system-level robots it is ignored.
// Returns nil, nil if no robot matches exactly.
func (c *Client) FindRobotByShortName(ctx context.Context, level, project, short string) (*Robot, error) {
	opts := ListRobotsOptions{NameFuzzy: short, PageSize: 100}
	if level == "project" {
		p, err := c.GetProject(ctx, project)
		if err != nil {
			return nil, fmt.Errorf("resolving project %q: %w", project, err)
		}
		opts.ProjectID = p.ProjectID
	}
	robots, err := c.ListRobots(ctx, opts)
	if err != nil {
		return nil, err
	}
	for i := range robots {
		if ShortName(robots[i].Name) == short {
			return &robots[i], nil
		}
	}
	return nil, nil
}

// ShortName strips Harbor's robot prefix and project qualifier from a full
// robot name: "robot$library+ci-1a2b" -> "ci-1a2b", "robot$ci-1a2b" -> "ci-1a2b".
func ShortName(full string) string {
	if i := strings.LastIndexAny(full, "$+"); i >= 0 {
		return full[i+1:]
	}
	return full
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	u := *c.baseURL
	u.Path = strings.TrimRight(u.Path, "/") + apiPrefix + path
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}

	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("harbor: encode request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return fmt.Errorf("harbor: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("harbor: %s %s: %w", method, apiPrefix+path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.apiError(resp, method, apiPrefix+path)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("harbor: decode %s %s response: %w", method, apiPrefix+path, err)
	}
	return nil
}

func (c *Client) apiError(resp *http.Response, method, path string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	apiErr := &APIError{Status: resp.StatusCode, Method: method, Path: path}
	var env errorsEnvelope
	if json.Unmarshal(raw, &env) == nil && len(env.Errors) > 0 {
		apiErr.Code = env.Errors[0].Code
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, e.Message)
		}
		apiErr.Message = strings.Join(msgs, "; ")
	} else {
		apiErr.Message = strings.TrimSpace(string(raw))
		if apiErr.Message == "" {
			apiErr.Message = http.StatusText(resp.StatusCode)
		}
	}
	return apiErr
}

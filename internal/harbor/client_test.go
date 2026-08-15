package harbor_test

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/harbor"
	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/harbor/harbortest"
)

func adminClient(t *testing.T, s *harbortest.Server) *harbor.Client {
	t.Helper()
	c, err := harbor.New(harbor.Config{URL: s.URL, Username: "admin", Password: "Harbor12345"})
	require.NoError(t, err)
	return c
}

func TestNew_Validation(t *testing.T) {
	_, err := harbor.New(harbor.Config{})
	require.ErrorContains(t, err, "url is required")
	_, err = harbor.New(harbor.Config{URL: "ftp://x"})
	require.ErrorContains(t, err, "scheme")
	_, err = harbor.New(harbor.Config{URL: "https://"})
	require.ErrorContains(t, err, "host")
	_, err = harbor.New(harbor.Config{URL: "https://h", CACertPEM: "not pem"})
	require.ErrorContains(t, err, "ca_cert")
	c, err := harbor.New(harbor.Config{URL: "https://harbor.example.com/"})
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestPingAndCurrentUser(t *testing.T) {
	s := harbortest.New()
	defer s.Close()
	c := adminClient(t, s)
	ctx := context.Background()

	require.NoError(t, c.Ping(ctx))
	me, err := c.CurrentUser(ctx)
	require.NoError(t, err)
	require.Equal(t, "admin", me.Username)
	require.True(t, me.SysadminFlag)
}

func TestAuthFailure(t *testing.T) {
	s := harbortest.New()
	defer s.Close()
	c, err := harbor.New(harbor.Config{URL: s.URL, Username: "admin", Password: "wrong"})
	require.NoError(t, err)
	_, err = c.CurrentUser(context.Background())
	require.Error(t, err)
	require.True(t, harbor.IsUnauthorized(err), err)
	var apiErr *harbor.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, http.StatusUnauthorized, apiErr.Status)
	require.Equal(t, "UNAUTHORIZED", apiErr.Code)
	require.Contains(t, err.Error(), "GET /api/v2.0/users/current")
}

func TestRobotLifecycle(t *testing.T) {
	s := harbortest.New()
	defer s.Close()
	c := adminClient(t, s)
	ctx := context.Background()

	created, err := c.CreateRobot(ctx, harbor.RobotCreate{
		Name: "vault-ci-abcd1234", Level: "project", Duration: 3,
		Permissions: []harbor.RobotPermission{{Kind: "project", Namespace: "library",
			Access: []harbor.Access{{Resource: "repository", Action: "pull"}}}},
	})
	require.NoError(t, err)
	require.Equal(t, "robot$library+vault-ci-abcd1234", created.Name)
	require.NotEmpty(t, created.Secret)
	require.True(t, harbortest.IsValidSecret(created.Secret))
	require.Greater(t, created.ExpiresAt, time.Now().Unix())

	got, err := c.GetRobot(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, int64(3), got.Duration)
	require.Empty(t, got.Secret)

	got.Duration = 10
	require.NoError(t, c.UpdateRobot(ctx, got))
	got2, err := c.GetRobot(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, int64(10), got2.Duration)
	require.Greater(t, got2.ExpiresAt, got.ExpiresAt)

	newSecret, err := c.RefreshRobotSecret(ctx, created.ID, "NewSecret123")
	require.NoError(t, err)
	require.Equal(t, "NewSecret123", newSecret)
	require.Equal(t, "NewSecret123", s.Robot(created.ID).Secret)

	_, err = c.RefreshRobotSecret(ctx, created.ID, "weak")
	require.Error(t, err)

	found, err := c.FindRobotByShortName(ctx, "project", "library", "vault-ci-abcd1234")
	require.NoError(t, err)
	require.NotNil(t, found)
	require.Equal(t, created.ID, found.ID)
	none, err := c.FindRobotByShortName(ctx, "project", "library", "vault-ci-abcd12")
	require.NoError(t, err)
	require.Nil(t, none)
	_, err = c.FindRobotByShortName(ctx, "project", "nope", "vault-ci-abcd1234")
	require.ErrorContains(t, err, "resolving project")

	// Unfiltered listing shows system-level robots only.
	list, err := c.ListRobots(ctx, harbor.ListRobotsOptions{NameFuzzy: "vault-ci", PageSize: 10})
	require.NoError(t, err)
	require.Empty(t, list)
	proj, err := c.GetProject(ctx, "library")
	require.NoError(t, err)
	list, err = c.ListRobots(ctx, harbor.ListRobotsOptions{ProjectID: proj.ProjectID, NameFuzzy: "vault-ci", PageSize: 10})
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "vault-ci-abcd1234", harbor.ShortName(list[0].Name))

	require.NoError(t, c.DeleteRobot(ctx, created.ID))
	err = c.DeleteRobot(ctx, created.ID)
	require.True(t, harbor.IsNotFound(err), err)
	_, err = c.GetRobot(ctx, created.ID)
	require.True(t, harbor.IsNotFound(err), err)
}

func TestCreateRobot_HarborValidation(t *testing.T) {
	s := harbortest.New()
	defer s.Close()
	c := adminClient(t, s)
	ctx := context.Background()
	perm := []harbor.RobotPermission{{Kind: "system", Namespace: "/", Access: []harbor.Access{{Resource: "robot", Action: "list"}}}}

	_, err := c.CreateRobot(ctx, harbor.RobotCreate{Name: "Bad_Name", Level: "system", Duration: 1, Permissions: perm})
	require.ErrorContains(t, err, "robot name")
	_, err = c.CreateRobot(ctx, harbor.RobotCreate{Name: "ok", Level: "system", Duration: 0, Permissions: perm})
	require.ErrorContains(t, err, "duration")
	_, err = c.CreateRobot(ctx, harbor.RobotCreate{Name: "ok", Level: "system", Duration: 1, Permissions: perm, Secret: "short"})
	require.ErrorContains(t, err, "secret")
	_, err = c.CreateRobot(ctx, harbor.RobotCreate{Name: "ok", Level: "system", Duration: 1, Permissions: []harbor.RobotPermission{
		{Kind: "project", Namespace: "library", Access: []harbor.Access{{Resource: "robot", Action: "update"}}}}})
	require.ErrorContains(t, err, "robot:update")
	_, err = c.CreateRobot(ctx, harbor.RobotCreate{Name: "ok", Level: "system", Duration: -1, Permissions: perm})
	require.NoError(t, err)
	_, err = c.CreateRobot(ctx, harbor.RobotCreate{Name: "ok", Level: "system", Duration: -1, Permissions: perm})
	require.Error(t, err)
	var apiErr *harbor.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, http.StatusConflict, apiErr.Status)
}

func TestChangeUserPassword(t *testing.T) {
	s := harbortest.New()
	defer s.Close()
	c := adminClient(t, s)
	ctx := context.Background()
	me, err := c.CurrentUser(ctx)
	require.NoError(t, err)

	require.Error(t, c.ChangeUserPassword(ctx, me.UserID, "wrongOld1", "NewPassword1"))
	require.Error(t, c.ChangeUserPassword(ctx, me.UserID, "Harbor12345", "weak"))
	require.NoError(t, c.ChangeUserPassword(ctx, me.UserID, "Harbor12345", "NewPassword1"))
	require.Equal(t, "NewPassword1", s.User("admin").Password)
	// old credential no longer works
	_, err = c.CurrentUser(ctx)
	require.True(t, harbor.IsUnauthorized(err))
}

func TestTLS_CustomCA(t *testing.T) {
	s := harbortest.NewTLS()
	defer s.Close()
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}))
	ctx := context.Background()

	// Without CA: fails.
	c, err := harbor.New(harbor.Config{URL: s.URL, Username: "admin", Password: "Harbor12345"})
	require.NoError(t, err)
	require.Error(t, c.Ping(ctx))

	// With CA: works.
	c, err = harbor.New(harbor.Config{URL: s.URL, Username: "admin", Password: "Harbor12345", CACertPEM: caPEM})
	require.NoError(t, err)
	require.NoError(t, c.Ping(ctx))

	// Insecure: works.
	c, err = harbor.New(harbor.Config{URL: s.URL, Username: "admin", Password: "Harbor12345", InsecureSkipVerify: true})
	require.NoError(t, err)
	require.NoError(t, c.Ping(ctx))
}

func TestTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()
	c, err := harbor.New(harbor.Config{URL: slow.URL, Timeout: 100 * time.Millisecond})
	require.NoError(t, err)
	start := time.Now()
	err = c.Ping(context.Background())
	require.Error(t, err)
	require.Less(t, time.Since(start), time.Second)
}

func TestErrorMapping_NonJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	}))
	defer srv.Close()
	c, err := harbor.New(harbor.Config{URL: srv.URL})
	require.NoError(t, err)
	err = c.Ping(context.Background())
	var apiErr *harbor.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, http.StatusBadGateway, apiErr.Status)
	require.Empty(t, apiErr.Code)
	require.Contains(t, apiErr.Message, "bad gateway")
}

func TestRobotModeScopeCheck(t *testing.T) {
	s := harbortest.New()
	defer s.Close()
	issuer := s.AddRobot("vault-issuer", "IssuerSecret1", "system", []harbor.RobotPermission{
		{Kind: "project", Namespace: "library", Access: []harbor.Access{
			{Resource: "robot", Action: "create"}, {Resource: "robot", Action: "read"},
			{Resource: "robot", Action: "delete"}, {Resource: "robot", Action: "list"},
			{Resource: "repository", Action: "pull"},
		}},
	})
	c, err := harbor.New(harbor.Config{URL: s.URL, Username: issuer.Name, Password: "IssuerSecret1"})
	require.NoError(t, err)
	ctx := context.Background()

	// /users/current is not for robots
	_, err = c.CurrentUser(ctx)
	require.True(t, harbor.IsUnauthorized(err))
	// unfiltered listing needs system robot:list -> 403 (credential is valid though)
	_, err = c.ListRobots(ctx, harbor.ListRobotsOptions{PageSize: 1})
	require.True(t, harbor.IsForbidden(err), err)
	// project-filtered listing works
	proj, err := c.GetProject(ctx, "library")
	require.NoError(t, err)
	_, err = c.ListRobots(ctx, harbor.ListRobotsOptions{ProjectID: proj.ProjectID, PageSize: 1})
	require.NoError(t, err)
	// robots cannot read/refresh themselves (no robot:update, self is system-level)
	_, err = c.GetRobot(ctx, issuer.ID)
	require.True(t, harbor.IsForbidden(err), err)
	_, err = c.RefreshRobotSecret(ctx, issuer.ID, "NewSecret123")
	require.True(t, harbor.IsForbidden(err), err)

	// within scope
	child, err := c.CreateRobot(ctx, harbor.RobotCreate{Name: "child-ok", Level: "project", Duration: 1,
		Permissions: []harbor.RobotPermission{{Kind: "project", Namespace: "library",
			Access: []harbor.Access{{Resource: "repository", Action: "pull"}}}}})
	require.NoError(t, err)
	got, err := c.GetRobot(ctx, child.ID)
	require.NoError(t, err)
	// robots cannot update robots
	got.Duration = 5
	err = c.UpdateRobot(ctx, got)
	require.True(t, harbor.IsForbidden(err), err)
	found, err := c.FindRobotByShortName(ctx, "project", "library", "child-ok")
	require.NoError(t, err)
	require.NotNil(t, found)
	require.NoError(t, c.DeleteRobot(ctx, child.ID))

	// broader than issuer: push not held
	_, err = c.CreateRobot(ctx, harbor.RobotCreate{Name: "child-bad", Level: "project", Duration: 1,
		Permissions: []harbor.RobotPermission{{Kind: "project", Namespace: "library",
			Access: []harbor.Access{{Resource: "repository", Action: "push"}}}}})
	require.ErrorContains(t, err, "permission scope is invalid")

	// system-level child: no system robot:create
	_, err = c.CreateRobot(ctx, harbor.RobotCreate{Name: "child-sys", Level: "system", Duration: 1,
		Permissions: []harbor.RobotPermission{{Kind: "project", Namespace: "library",
			Access: []harbor.Access{{Resource: "repository", Action: "pull"}}}}})
	require.True(t, harbor.IsForbidden(err), err)

	// other project: no robot:create there
	s.AddProject("other")
	_, err = c.CreateRobot(ctx, harbor.RobotCreate{Name: "child-other", Level: "project", Duration: 1,
		Permissions: []harbor.RobotPermission{{Kind: "project", Namespace: "other",
			Access: []harbor.Access{{Resource: "repository", Action: "pull"}}}}})
	require.True(t, harbor.IsForbidden(err), err)
}

//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Thin admin helpers for Harbor endpoints the plugin itself does not need
// (projects, users, members).

func adminDo(method, path string, in any, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, strings.TrimRight(harborURL, "/")+"/api/v2.0"+path, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(adminUser, adminPass)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s %s: %d: %s", method, path, resp.StatusCode, string(raw))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func adminPost(path string, in any, out any) error { return adminDo(http.MethodPost, path, in, out) }
func adminDelete(path string) error                { return adminDo(http.MethodDelete, path, nil, nil) }

// createUser creates a Harbor local user and returns its id.
func createUser(username, password string) (int64, error) {
	if err := adminPost("/users", map[string]any{
		"username": username, "password": password, "email": username + "@example.invalid", "realname": username,
	}, nil); err != nil {
		return 0, err
	}
	var users []struct {
		UserID int64 `json:"user_id"`
	}
	if err := adminDo(http.MethodGet, "/users?q=username%3D"+username, nil, &users); err != nil {
		return 0, err
	}
	if len(users) == 0 {
		return 0, fmt.Errorf("user %s not found after creation", username)
	}
	return users[0].UserID, nil
}

// addProjectAdmin makes the user a project administrator (role 1).
func addProjectAdmin(projectID, userID int64) error {
	return adminPost(fmt.Sprintf("/projects/%d/members", projectID), map[string]any{
		"role_id": 1, "member_user": map[string]any{"user_id": userID},
	}, nil)
}

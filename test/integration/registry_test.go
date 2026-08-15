//go:build integration

package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Native Docker Registry v2 client for Harbor: bearer token flow via
// /service/token, a minimal OCI image push, and manifest pulls. This keeps the
// integration suite independent of a docker CLI.

const (
	mediaTypeOCIManifest = "application/vnd.oci.image.manifest.v1+json"
	mediaTypeOCIConfig   = "application/vnd.oci.image.config.v1+json"
	mediaTypeOCILayer    = "application/vnd.oci.image.layer.v1.tar+gzip"
)

// registryToken obtains a bearer token for scope using basic credentials.
// Returns the HTTP status of the token request and the token (if any).
func registryToken(user, pass, scope string) (int, string, error) {
	q := url.Values{"service": {"harbor-registry"}, "scope": {scope}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, strings.TrimRight(harborURL, "/")+"/service/token?"+q.Encode(), nil)
	if err != nil {
		return 0, "", err
	}
	req.SetBasicAuth(user, pass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, "", nil
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, out.Token, nil
}

// registryRequest performs an authenticated registry call and returns status + body.
func registryRequest(token, method, path string, body []byte, contentType string) (int, http.Header, []byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	u := path
	if !strings.HasPrefix(path, "http") {
		u = strings.TrimRight(harborURL, "/") + path
	}
	req, err := http.NewRequestWithContext(context.Background(), method, u, rdr)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", mediaTypeOCIManifest)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, raw, nil
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// pushTinyImage pushes a one-layer OCI image to repo:tag using the given
// credentials (which need push+pull on the repository).
func pushTinyImage(t *testing.T, user, pass, repo, tag string) {
	t.Helper()
	status, token, err := registryToken(user, pass, "repository:"+repo+":pull,push")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "token for push")

	// layer: gzip(tar with one file)
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	content := []byte("hello from vault-plugin-secrets-harbor integration test\n")
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "hello.txt", Mode: 0o644, Size: int64(len(content))}))
	_, _ = tw.Write(content)
	require.NoError(t, tw.Close())
	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	_, _ = gz.Write(tarBuf.Bytes())
	require.NoError(t, gz.Close())
	layer := gzBuf.Bytes()

	config, _ := json.Marshal(map[string]any{
		"architecture": "amd64", "os": "linux",
		"rootfs": map[string]any{"type": "layers", "diff_ids": []string{digestOf(tarBuf.Bytes())}},
		"config": map[string]any{},
	})

	uploadBlob := func(blob []byte) {
		st, hdr, body, err := registryRequest(token, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", nil, "")
		require.NoError(t, err)
		require.Equal(t, http.StatusAccepted, st, "start upload: %s", body)
		loc := hdr.Get("Location")
		require.NotEmpty(t, loc)
		sep := "?"
		if strings.Contains(loc, "?") {
			sep = "&"
		}
		st, _, body, err = registryRequest(token, http.MethodPut, loc+sep+"digest="+digestOf(blob), blob, "application/octet-stream")
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, st, "finish upload: %s", body)
	}
	uploadBlob(config)
	uploadBlob(layer)

	manifest, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     mediaTypeOCIManifest,
		"config":        map[string]any{"mediaType": mediaTypeOCIConfig, "digest": digestOf(config), "size": len(config)},
		"layers":        []map[string]any{{"mediaType": mediaTypeOCILayer, "digest": digestOf(layer), "size": len(layer)}},
	})
	st, _, body, err := registryRequest(token, http.MethodPut, "/v2/"+repo+"/manifests/"+tag, manifest, mediaTypeOCIManifest)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, st, "push manifest: %s", body)
}

// pullManifestStatus returns the HTTP status of fetching repo:tag's manifest
// with the given credentials: 200 = pull works; 401/403 = denied.
func pullManifestStatus(t *testing.T, user, pass, repo, tag string) int {
	t.Helper()
	status, token, err := registryToken(user, pass, "repository:"+repo+":pull")
	require.NoError(t, err)
	if status != http.StatusOK {
		return status
	}
	st, _, _, err := registryRequest(token, http.MethodGet, "/v2/"+repo+"/manifests/"+tag, nil, "")
	require.NoError(t, err)
	return st
}

// canPush reports whether the credentials receive push access in the token
// (Harbor issues a token with the granted subset of the requested actions).
func canPush(t *testing.T, user, pass, repo string) bool {
	t.Helper()
	status, token, err := registryToken(user, pass, "repository:"+repo+":pull,push")
	require.NoError(t, err)
	if status != http.StatusOK {
		return false
	}
	// Try to start an upload; denied → 401/403.
	st, _, _, err := registryRequest(token, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", nil, "")
	require.NoError(t, err)
	return st == http.StatusAccepted
}

func fmtRepo(project, name string) string { return fmt.Sprintf("%s/%s", project, name) }

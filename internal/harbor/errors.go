package harbor

import (
	"errors"
	"fmt"
	"net/http"
)

// APIError is returned for any non-2xx Harbor response.
type APIError struct {
	Status  int    // HTTP status code
	Code    string // Harbor error code (e.g. NOT_FOUND, FORBIDDEN, BAD_REQUEST); may be empty
	Message string // Harbor error message, or the raw body if not JSON
	Method  string
	Path    string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("harbor: %s %s: %d %s: %s", e.Method, e.Path, e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("harbor: %s %s: %d: %s", e.Method, e.Path, e.Status, e.Message)
}

// IsNotFound reports whether err is a Harbor 404.
func IsNotFound(err error) bool { return statusIs(err, http.StatusNotFound) }

// IsUnauthorized reports whether err is a Harbor 401.
func IsUnauthorized(err error) bool { return statusIs(err, http.StatusUnauthorized) }

// IsForbidden reports whether err is a Harbor 403.
func IsForbidden(err error) bool { return statusIs(err, http.StatusForbidden) }

func statusIs(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == status
}

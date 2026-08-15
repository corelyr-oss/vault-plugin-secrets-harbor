package backend

import "errors"

var (
	errNotConfigured          = errors.New("harbor secrets engine is not configured; write to config first")
	errRobotRotateUnsupported = errors.New("rotate-root is not supported for auth_type=robot: Harbor does not allow a robot account to refresh its own secret; refresh the issuer robot's secret as a Harbor administrator and write it to config")
)

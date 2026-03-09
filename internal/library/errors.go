package library

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"syscall"
)

func isPermissionError(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, fs.ErrPermission) || errors.Is(err, os.ErrPermission) {
		return true
	}
	if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		return true
	}

	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "operation not permitted") ||
		strings.Contains(msg, "access is denied") ||
		strings.Contains(msg, "read-only file system") {
		return true
	}

	return false
}

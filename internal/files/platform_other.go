//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package files

import (
	"errors"
	"os"
)

func fileIdentity(info os.FileInfo) string { return info.Name() }
func freeBytes(file *os.File) (int64, error) {
	return 0, errors.New("free-space checks unsupported on this platform")
}

func lockState(file *os.File) error {
	return errors.New("file service requires a platform with advisory file locks")
}

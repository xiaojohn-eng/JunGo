//go:build linux

package files

import (
	"io/fs"
	"os"
	"path"
	"strings"

	"golang.org/x/sys/unix"
)

// publishExclusive moves a verified staging file into place without overwriting
// any existing destination. Android's untrusted_app SELinux domain forbids
// hardlinks even within private app storage; renameat2 is allowed there.
//
// The only path traversal is performed by os.Root. Both syscall names are single
// leaf components relative to that pinned directory FD. A symlink destination
// is treated as occupied by RENAME_NOREPLACE, never followed. We deliberately do
// not fall back to a racy existence-check plus ordinary rename on old kernels.
func publishExclusive(root *os.Root, stage, destination string) error {
	stageName := path.Base(stage)
	if path.Dir(stage) != path.Dir(destination) || !validPath(destination, false) || !strings.HasPrefix(stageName, stagingPrefix) || !validUploadID(strings.TrimPrefix(stageName, stagingPrefix)) {
		return &os.LinkError{Op: "renameat2", Old: stage, New: destination, Err: fs.ErrInvalid}
	}
	directory, err := root.Open(path.Dir(stage))
	if err != nil {
		return err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return &os.LinkError{Op: "renameat2", Old: stage, New: destination, Err: unix.ENOTDIR}
	}
	err = unix.Renameat2(int(directory.Fd()), path.Base(stage), int(directory.Fd()), path.Base(destination), unix.RENAME_NOREPLACE)
	if err != nil {
		return &os.LinkError{Op: "renameat2", Old: stage, New: destination, Err: err}
	}
	return nil
}

//go:build linux

package files

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxExclusivePublicationMovesStageAndContainsPaths(t *testing.T) {
	f := setup(t, false)
	root := f.s.shares["docs"].root
	stage := stagingPrefix + strings.Repeat("b", 32)
	f.putFile(stage, []byte("verified"))
	for _, names := range [][2]string{{"x", "received"}, {"/", "received"}, {stage, "../outside"}, {stage, "/outside"}, {stage, "child/received"}} {
		if err := publishExclusive(root, names[0], names[1]); err == nil {
			t.Fatalf("invalid publication names accepted: %q", names)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(f.dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := publishExclusive(root, "escape/"+stage, "escape/received"); err == nil {
		t.Fatal("symlink parent escaped the root")
	}
	if err := publishExclusive(root, stage, "received"); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Linux publication did not consume the staging name", err)
	}
}

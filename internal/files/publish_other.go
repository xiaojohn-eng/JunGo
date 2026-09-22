//go:build !linux

package files

import "os"

func publishExclusive(root *os.Root, stage, destination string) error {
	return root.Link(stage, destination)
}

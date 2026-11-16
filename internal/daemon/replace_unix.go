//go:build !windows

package daemon

import "os"

func replaceStateFile(from, to string) error { return os.Rename(from, to) }

//go:build windows

package daemon

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// Windows readers (including doctor) can briefly deny replacement by opening
// without FILE_SHARE_DELETE. Retry only transient sharing/access errors, with
// a fixed bound; never remove the original file to force a replacement.
func replaceStateFile(from, to string) error {
	var err error
	for attempt := 0; attempt < 11; attempt++ {
		err = os.Rename(from, to)
		if err == nil || (!errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION)) {
			return err
		}
		if attempt < 10 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	return err
}

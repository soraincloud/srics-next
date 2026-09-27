//go:build darwin

package atomicfile

import (
	"os"

	"golang.org/x/sys/unix"
)

// Sync flushes a regular file through the drive cache on macOS. Fail closed
// when the filesystem cannot provide this guarantee; fsync alone is weaker.
func Sync(f *os.File) error {
	if err := f.Sync(); err != nil {
		return err
	}
	_, err := unix.FcntlInt(f.Fd(), unix.F_FULLFSYNC, 0)
	return err
}

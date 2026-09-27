//go:build !darwin

package atomicfile

import "os"

func Sync(f *os.File) error { return f.Sync() }

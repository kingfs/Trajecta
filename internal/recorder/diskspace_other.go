//go:build !linux

package recorder

import "errors"

func availableDiskBytes(string) (int64, error) {
	return 0, errors.New("filesystem free-space checks are supported only on Linux")
}

//go:build linux

package recorder

import (
	"fmt"
	"strings"
	"syscall"
)

func availableDiskBytes(path string) (int64, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "."
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 {
		return 0, fmt.Errorf("filesystem returned invalid block size %d", stat.Bsize)
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

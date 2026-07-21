//go:build unix

package main

import (
	"io/fs"
	"syscall"
)

// fileOwnerUID reads the owning user id from a stat result. It is
// platform-specific: ownership is the whole question the hardiness check asks, and
// it has no portable answer.
func fileOwnerUID(info fs.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Uid), true
}

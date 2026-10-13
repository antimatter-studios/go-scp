package scp

import (
	"io/fs"
	"time"
)

// AccessTime returns info's access time, where the platform's stat records one
// that this package can read, and its modification time otherwise, as "scp -p"
// sends for a file whose access time cannot be had.
func AccessTime(info fs.FileInfo) time.Time {
	if atime, ok := statAccessTime(info.Sys()); ok {
		return atime
	}
	return info.ModTime()
}

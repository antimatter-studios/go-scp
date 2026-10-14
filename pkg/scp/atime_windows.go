package scp

import (
	"syscall"
	"time"
)

func statAccessTime(sys any) (time.Time, bool) {
	attrs, ok := sys.(*syscall.Win32FileAttributeData)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(0, attrs.LastAccessTime.Nanoseconds()), true
}

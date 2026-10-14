//go:build !linux && !darwin && !windows

package scp

import "time"

// statAccessTime reads no access time here: the platform's stat is not one this
// package knows, so AccessTime falls back to the modification time.
func statAccessTime(any) (time.Time, bool) { return time.Time{}, false }

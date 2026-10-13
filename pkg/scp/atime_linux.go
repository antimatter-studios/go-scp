package scp

import (
	"syscall"
	"time"
)

func statAccessTime(sys any) (time.Time, bool) {
	st, ok := sys.(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(st.Atim.Unix()), true
}

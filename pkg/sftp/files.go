package sftp

import "sync"

// parallel readies the copy for files that copy at once, when the client's
// Parallel allows more than one: a slot for each that may be in flight, and a
// Progress that is never called from two of them at once.
func (cp *copying) parallel() {
	if cp.Parallel <= 1 {
		return
	}
	cp.files = make(chan struct{}, cp.Parallel)
	if report := cp.progress; report != nil {
		var mu sync.Mutex
		cp.progress = func(name string, done, size int64) {
			mu.Lock()
			defer mu.Unlock()
			report(name, done, size)
		}
	}
}

// file runs copyFile, the copy of one regular file: at once when files copy one at
// a time, and otherwise on a goroutine of its own, counted in inFlight, as soon as
// a slot is free. Only files take slots, so a directory waiting for its files never
// holds one they need.
func (cp *copying) file(inFlight *sync.WaitGroup, copyFile func()) {
	if cp.files == nil {
		copyFile()
		return
	}
	cp.files <- struct{}{}
	inFlight.Add(1)
	go func() {
		defer inFlight.Done()
		defer func() { <-cp.files }()
		copyFile()
	}()
}

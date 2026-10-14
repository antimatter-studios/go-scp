package scp

import "io"

// Progress is told how a transfer moves: once as each file starts, with done 0,
// and again as its bytes move, with done the bytes moved so far. name is the
// file's path in the transfer and size the bytes it holds. It is called on the
// goroutine doing the transfer, so a slow callback slows the transfer down.
type Progress func(name string, done, size int64)

// track returns r, reporting what is read from it as the body of name, size bytes
// long. With no Progress it returns r itself.
func (p Progress) track(name string, size int64, r io.Reader) io.Reader {
	if p == nil {
		return r
	}
	p(name, 0, size)
	return &progressReader{r: r, name: name, size: size, progress: p}
}

type progressReader struct {
	r        io.Reader
	name     string
	done     int64
	size     int64
	progress Progress
}

func (pr *progressReader) Read(b []byte) (int, error) {
	n, err := pr.r.Read(b)
	if n > 0 {
		pr.done += int64(n)
		pr.progress(pr.name, pr.done, pr.size)
	}
	return n, err
}

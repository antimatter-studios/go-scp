module github.com/antimatter-studios/go-scp/pkg/sftp

go 1.25.0

// The classic client and DirSink come from this repository, at the commit being
// built; a release of this module requires a released version of them instead.
replace github.com/antimatter-studios/go-scp => ../..

require (
	github.com/antimatter-studios/go-scp v0.5.0
	github.com/pkg/sftp v1.13.11
	golang.org/x/crypto v0.54.0
)

require (
	github.com/kr/fs v0.1.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

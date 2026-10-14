module github.com/antimatter-studios/go-scp/pkg/sftp

go 1.25.0

// Built in this repository, the classic client and DirSink come from the same
// commit. A program that requires this module gets the version required below:
// replace directives apply only to the module being built.
replace github.com/antimatter-studios/go-scp/pkg/scp => ../scp

require (
	github.com/antimatter-studios/go-scp/pkg/scp v0.7.0
	github.com/pkg/sftp v1.13.11
	golang.org/x/crypto v0.54.0
)

require (
	github.com/kr/fs v0.1.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

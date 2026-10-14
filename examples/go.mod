module github.com/antimatter-studios/go-scp/examples

go 1.25.0

// Built in this repository, the modules the examples show come from the same
// commit.
replace (
	github.com/antimatter-studios/go-scp/pkg/scp => ../pkg/scp
	github.com/antimatter-studios/go-scp/pkg/sftp => ../pkg/sftp
)

require (
	github.com/antimatter-studios/go-scp/pkg/scp v0.8.0
	github.com/antimatter-studios/go-scp/pkg/sftp v0.8.0
	golang.org/x/crypto v0.54.0
)

require (
	github.com/kr/fs v0.1.0 // indirect
	github.com/pkg/sftp v1.13.11 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

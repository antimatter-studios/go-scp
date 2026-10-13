module github.com/antimatter-studios/go-scp/examples/sftp

go 1.25.0

// The library and the SFTP module from this repository, at the commit being built.
replace (
	github.com/antimatter-studios/go-scp => ../..
	github.com/antimatter-studios/go-scp/pkg/sftp => ../../pkg/sftp
)

require (
	github.com/antimatter-studios/go-scp v0.6.0
	github.com/antimatter-studios/go-scp/pkg/sftp v0.1.0
)

require (
	github.com/kr/fs v0.1.0 // indirect
	github.com/pkg/sftp v1.13.11 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

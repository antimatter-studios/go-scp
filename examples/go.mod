module github.com/antimatter-studios/go-scp/examples

go 1.25.0

// Built in this repository, the modules the examples show come from the same
// commit.
replace github.com/antimatter-studios/go-scp/pkg/scp => ../pkg/scp

require (
	github.com/antimatter-studios/go-scp/pkg/scp v0.5.0
	golang.org/x/crypto v0.52.0
)

require golang.org/x/sys v0.45.0 // indirect

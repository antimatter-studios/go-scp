module github.com/antimatter-studios/go-scp

go 1.25.0

require golang.org/x/crypto v0.52.0

// v0.1.0 had the package at the module root; it lives in pkg/scp from v0.2.0.
retract v0.1.0

require golang.org/x/sys v0.45.0 // indirect

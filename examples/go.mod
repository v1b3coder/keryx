// Showcase consumers of the publisher SDK. Each subdirectory is a small,
// standalone program that imports github.com/v1b3coder/keryx/sdk exactly the way
// an external project would.
module github.com/v1b3coder/keryx/examples

go 1.26.0

require github.com/v1b3coder/keryx/sdk v0.0.0

require (
	github.com/google/go-containerregistry v0.22.1 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/secure-systems-lab/go-securesystemslib v0.11.0 // indirect
	github.com/sigstore/protobuf-specs v0.5.2 // indirect
	github.com/sigstore/sigstore v1.10.6 // indirect
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e // indirect
	github.com/theupdateframework/go-tuf/v2 v2.4.2 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/term v0.46.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260727163830-6c54dddc4772 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/v1b3coder/keryx/sdk => ../sdk

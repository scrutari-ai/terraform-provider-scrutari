//go:build tools

// Package tools pins the versions of every command-line tool the
// provider build pipeline depends on. None of these symbols are
// referenced at runtime — the file's only job is to keep the tools
// in `go.mod` / `go.sum` so they're frozen with the rest of the
// dependency graph, surviving `go mod tidy` cycles.
//
// The `//go:build tools` constraint at the top of the file means
// the Go compiler skips this package during a normal `go build`
// (it doesn't try to link in `oapi-codegen` or `tfplugindocs` as
// runtime dependencies of the provider binary). The Makefile's
// `make tools` target installs each command into `./bin/` for the
// generate / docs targets to invoke.
//
// # Why pin tool versions in tools.go and ALSO in the Makefile
//
// The Makefile passes an explicit version constant (e.g.
// `OAPI_CODEGEN_VERSION ?= v2.5.0`) to `go install ...@$(VERSION)`,
// which is what actually installs the bin into ./bin. The version
// in tools.go is what `go mod tidy` keeps in `go.mod`'s
// require block. They MUST match — `make generate-check` will
// surface drift in CI by regenerating against the Makefile pin and
// then failing if the resulting code differs from what a developer
// generated against the same Makefile pin. Easy way to drive both
// from one source: bump the Makefile constant, then `go get
// github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@<new>`
// to update go.mod in lockstep.

package tools

import (
	// oapi-codegen — generates the wire DTOs in `internal/client/`
	// from the committed `openapi.json`. Invoked by `make generate`.
	_ "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen"

	// tfplugindocs — generates the registry-published documentation
	// in `docs/` from the provider's schema MarkdownDescriptions and
	// the `examples/` HCL fixtures. Invoked by `make docs`.
	_ "github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs"
)

HOSTNAME=registry.terraform.io
NAMESPACE=scrutari
NAME=scrutari
VERSION=0.2.0
OS_ARCH=$(shell go env GOOS)_$(shell go env GOARCH)

# Codegen knobs — `make fetch-spec` pulls a fresh openapi.json from
# GATEWAY_URL; everything downstream reads the committed file at
# SPEC_PATH. The default GATEWAY_URL points at the production
# control-plane DNS, but `make fetch-spec GATEWAY_URL=https://localhost:9095`
# is the typical dev-loop override.
#
# COMPAT_SPEC_PATH is a build artifact (.gitignored). The gateway
# emits OpenAPI 3.1 and oapi-codegen v2.5.0 doesn't yet handle 3.1's
# `"type": ["string", "null"]` nullable-array syntax. We pre-process
# the canonical 3.1 spec into a 3.0-compatible form via jq before
# feeding it to codegen. When oapi-codegen ships full 3.1 support
# (their issue #373), this whole compat step becomes a `cp`.
GATEWAY_URL      ?= https://api.edge.scrutari.ai
SPEC_PATH        := internal/client/openapi.json
COMPAT_SPEC_PATH := internal/client/openapi.compat.json
CLIENT_PATH      := internal/client/client.gen.go
TOOLS_BIN        := $(CURDIR)/bin

.PHONY: build install test acceptance docs docs-check tidy tools fetch-spec compat-spec generate generate-check

# Build the provider binary into the project root.
build:
	go build -o terraform-provider-${NAME} -ldflags "-X main.version=${VERSION}-dev"

# Install into Terraform's plugin path so `terraform init` can discover
# this build without a registry round-trip. Use the dev_overrides
# block in ~/.terraformrc instead if you're iterating quickly.
install: build
	mkdir -p ~/.terraform.d/plugins/${HOSTNAME}/${NAMESPACE}/${NAME}/${VERSION}/${OS_ARCH}
	cp terraform-provider-${NAME} ~/.terraform.d/plugins/${HOSTNAME}/${NAMESPACE}/${NAME}/${VERSION}/${OS_ARCH}/

# Unit tests only — fast, no network.
test:
	go test -v ./...

# Acceptance tests hit a real Scrutari gateway. Requires
# SCRUTARI_API_KEY + SCRUTARI_HOST in env.
acceptance:
	TF_ACC=1 go test -v ./internal/provider/ -timeout 30m

# Pinned tfplugindocs version. Same posture as OAPI_CODEGEN_VERSION
# below — keeps every developer's docs generation deterministic
# regardless of when they first ran `make docs`. Bumping requires a
# Makefile PR (and the regenerated docs/ tree it produces should be
# the same diff every committer sees).
TFPLUGINDOCS_VERSION ?= v0.20.1

# Install tfplugindocs into $(TOOLS_BIN). Mirrors the `tools` target
# pattern for oapi-codegen — `go install` parks the bin in
# $GOPATH/bin by default, which isn't reliably in PATH. Installing
# to $(TOOLS_BIN) and invoking it via the absolute path side-steps
# the PATH problem on every developer's machine and in CI.
$(TOOLS_BIN)/tfplugindocs:
	@mkdir -p $(TOOLS_BIN)
	@echo "→ Installing tfplugindocs $(TFPLUGINDOCS_VERSION) into $(TOOLS_BIN)"
	GOBIN=$(TOOLS_BIN) go install github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@$(TFPLUGINDOCS_VERSION)

# Generate registry docs from schema MarkdownDescriptions and
# examples/. The dependency on $(TOOLS_BIN)/tfplugindocs makes this
# idempotent — first run installs, subsequent runs reuse the
# cached binary.
docs: $(TOOLS_BIN)/tfplugindocs
	$(TOOLS_BIN)/tfplugindocs generate --provider-name ${NAME}

# CI guard: fail if committed docs don't match what the schema would
# generate. Catches "docs got stale because someone forgot to run
# `make docs`" in PRs.
docs-check: $(TOOLS_BIN)/tfplugindocs
	$(TOOLS_BIN)/tfplugindocs generate --provider-name ${NAME}
	@git diff --exit-code docs/ || (echo "::error::docs are stale. Run 'make docs' and commit." && exit 1)

# Pull the latest deps. Run after editing imports.
tidy:
	go mod tidy

# ─── Codegen pipeline ──────────────────────────────────────────────
#
# Pull a fresh openapi.json from the live gateway and regenerate the
# wire DTOs into internal/client/client.gen.go. The hand-rolled
# Client struct in internal/provider/client.go remains the public
# surface (it owns idempotency keys, error wrapping, the
# scru_live_ / scru_test_ prefix validation, and the User-Agent);
# the generated types are imported strictly as DTO definitions, so
# field drift between gateway and provider surfaces as a Go
# compile error rather than a JSON-decode error at runtime.
#
# # Usage
#
#   make fetch-spec           # pull openapi.json from GATEWAY_URL
#   make generate             # regenerate client.gen.go from the
#                             # committed spec (no network)
#   make generate-check       # CI guard: regen + fail if diff
#
# `make tools` installs oapi-codegen into ./bin under the repo so
# CI environments don't depend on the developer's $GOPATH/bin
# state. Subsequent calls reuse the cached binary.

# Pin oapi-codegen here, not in tools/tools.go, so a `go mod tidy`
# that drops the tool import (it's a build-time-only dep) doesn't
# silently rotate the version we generate against.
OAPI_CODEGEN_VERSION ?= v2.5.0

tools:
	@mkdir -p $(TOOLS_BIN)
	@if [ ! -x $(TOOLS_BIN)/oapi-codegen ] || \
	    [ "$$($(TOOLS_BIN)/oapi-codegen --version 2>/dev/null)" != "$(OAPI_CODEGEN_VERSION)" ]; then \
	    echo "→ Installing oapi-codegen $(OAPI_CODEGEN_VERSION) into $(TOOLS_BIN)"; \
	    GOBIN=$(TOOLS_BIN) go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION); \
	fi

# Pull a fresh openapi.json from the running gateway and overwrite
# the committed copy. Run this whenever the gateway's spec lands a
# change that the provider needs to track. The CI gate
# (`generate-check`) catches the case where someone forgets to run
# `fetch-spec` + `generate` after a gateway-side schema bump.
fetch-spec:
	@echo "→ Fetching $(GATEWAY_URL)/v1/openapi.json"
	curl -sSf -o $(SPEC_PATH) $(GATEWAY_URL)/v1/openapi.json
	@echo "✓ Spec written to $(SPEC_PATH)"

# OpenAPI 3.1 → 3.0 nullable downcast. oapi-codegen v2.5.0 doesn't
# yet understand 3.1's `"type": ["X", "null"]` nullable-array form
# (their issue #373). Every list response carries it on
# `next_cursor`, several response shapes carry it on optional
# fields (`last_used_at`, `verified_at`, `last_error`, etc.), so we
# can't simply exclude a tag. The jq filter below rewrites every
# such two-element nullable-array into the 3.0 form
# (`"type": "X", "nullable": true`) before handing the spec to
# codegen. Three-element-or-more arrays (union types — utoipa
# doesn't generate them today, but defensive) are left alone;
# they'd need a different fix and would fail loudly at codegen
# time if they ever showed up.
#
# The canonical $(SPEC_PATH) stays 3.1 (matches the gateway's wire
# contract); $(COMPAT_SPEC_PATH) is a transient build artifact,
# regenerated on every codegen run and .gitignored.
# The jq filter intentionally lives on a single line. Multi-line
# filters with backslash-continuation get parsed by Make in ways
# that break jq's lexer (the backslashes survive into the filter
# source). One-line is uglier to read but provably works. Reading
# guide — walk every node and apply TWO rewrites:
#
#   1. An object whose `.type` is exactly a two-element array
#      containing "null" becomes the 3.0 form
#      `{ type: "<non-null>", nullable: true }`.
#      Three-or-more-element type arrays (union types — utoipa
#      doesn't emit them today) are left untouched and would fail
#      at codegen time if they ever appeared, the correct loud
#      failure.
#   2. An object whose `.oneOf` contains a `{"type": "null"}`
#      branch (utoipa's 3.1 encoding for `Option<T>` where T is a
#      $ref — refs can't carry inline nullability, so the null
#      rides as a oneOf sibling; first seen on
#      `MessagesRequest.system`) gets the null branch(es) removed
#      and `nullable: true` set. When exactly ONE branch remains it
#      collapses to the canonical 3.0 nullable-ref idiom
#      `{ nullable: true, allOf: [<branch>] }` (a bare $ref with a
#      `nullable` sibling would be IGNORED per the 3.0 spec — the
#      allOf wrapper is what makes the nullability stick, and
#      oapi-codegen renders it as a pointer field). With two or
#      more remaining branches the oneOf stays a oneOf, just
#      without the null arm.
compat-spec: $(SPEC_PATH)
	@command -v jq >/dev/null || ( echo "✗ jq not found — install via 'brew install jq' (macOS) or 'apt install jq' (Debian/Ubuntu)"; exit 1 )
	@echo "→ Downcasting OpenAPI 3.1 nullable forms in $(SPEC_PATH) → $(COMPAT_SPEC_PATH)"
	@jq '.openapi = "3.0.3" | walk(if type == "object" and (.type | type) == "array" and (.type | length) == 2 and (.type | index("null")) != null then (.type | map(select(. != "null"))) as $$nn | .type = $$nn[0] | .nullable = true elif type == "object" and (.oneOf | type) == "array" and ([.oneOf[] | select(type == "object" and (.type == "null" or .type == ["null"]))] | length) > 0 then ([.oneOf[] | select((type == "object" and (.type == "null" or .type == ["null"])) | not)]) as $$nn | (if ($$nn | length) == 1 then (del(.oneOf) | .allOf = $$nn | .nullable = true) else (.oneOf = $$nn | .nullable = true) end) else . end)' $(SPEC_PATH) > $(COMPAT_SPEC_PATH)
	@echo "✓ 3.0-compat spec written to $(COMPAT_SPEC_PATH)"

# Regenerate the client from the COMPAT spec (the 3.0-downcast
# form). The compat step is fast (jq processing ~80 KB of JSON)
# so we re-run it on every `make generate` rather than caching —
# avoids stale-compat bugs.
generate: tools compat-spec
	@echo "→ Regenerating $(CLIENT_PATH) from $(COMPAT_SPEC_PATH)"
	$(TOOLS_BIN)/oapi-codegen -config internal/client/config.yaml $(COMPAT_SPEC_PATH)
	@echo "✓ Client regenerated"

# CI guard: regenerate, then fail if git sees any diff. Catches both
# "spec was edited but client wasn't regenerated" AND "client was
# hand-edited" — both are bugs. Mirrors the gateway-side drift gate
# (#222) so spec drift never crosses a repo boundary silently.
generate-check: generate
	@git diff --exit-code $(CLIENT_PATH) $(SPEC_PATH) || ( \
	    echo "✗ Generated client or spec is out of sync. Run 'make fetch-spec generate' and commit."; \
	    exit 1 )

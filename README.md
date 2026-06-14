# Terraform Provider for Scrutari

Manage your [Scrutari](https://app.edge.scrutari.ai) post-quantum AI gateway as
code. This provider lets platform teams declare gateway routes, custom
domains, delegated DNS zones, transport-security policy, PII guardrails,
and API keys in Terraform, so an entire tenant's gateway configuration
is version-controlled, reviewable, and reproducible.

Scrutari is a post-quantum AI gateway for regulated industries: hybrid
PQ-TLS and CNSA 2.0 on the wire, signed and auditable evidence, and
sovereign controls. This provider is the control plane for that gateway.

## Requirements

- Terraform >= 1.0
- A Scrutari tenant and an API key with the scopes for the resources you
  manage (see [Authentication](#authentication))

## Using the provider

```hcl
terraform {
  required_providers {
    scrutari = {
      source  = "scrutari-ai/scrutari"
      version = "~> 0.1"
    }
  }
}

provider "scrutari" {
  # host defaults to https://api.edge.scrutari.ai
  # api_key is read from the SCRUTARI_API_KEY env var when omitted
}
```

Set credentials through the environment so the key never lands in
state files or version control:

```sh
export SCRUTARI_API_KEY="scru_live_..."
export SCRUTARI_HOST="https://api.edge.scrutari.ai"   # optional
```

## Example

Delegate a DNS zone once, then manage hostnames, routing, and policy
entirely in Terraform:

```hcl
# Delegate *.ai.example.com to Scrutari (one NS delegation).
resource "scrutari_delegated_zone" "main" {
  zone = "ai.example.com"
}

# A hostname under the delegated zone, no per-domain DNS challenge.
resource "scrutari_domain" "api" {
  domain  = "api.ai.example.com"
  zone_id = scrutari_delegated_zone.main.id
}

# Route traffic for that host to an upstream.
resource "scrutari_route" "api" {
  host     = scrutari_domain.api.domain
  upstream = "https://backend.internal.example.com"
}

# Require at least hybrid post-quantum TLS, in monitor mode first.
resource "scrutari_transport_policy" "floor" {
  min_posture = "hybrid"
  mode        = "monitor"
}

# Redact PII categories on the way to model providers.
resource "scrutari_pii_policy" "guardrails" {
  rule {
    category = "credit_card"
    mode     = "block"
  }
  rule {
    category = "mrn"
    mode     = "redact"
  }
}
```

## Resources and data sources

Resources:

- `scrutari_route` — host-to-upstream routing
- `scrutari_domain` — custom hostnames (classic verification or
  zone-backed)
- `scrutari_delegated_zone` — delegated DNS zones for wildcard issuance
- `scrutari_transport_policy` — the post-quantum transport floor and
  monitor/enforce mode (singleton per tenant)
- `scrutari_pii_policy` — PII detection and redaction guardrails
  (singleton per tenant)
- `scrutari_api_key` — scoped API keys for automation

Data sources:

- `scrutari_tenant` — the calling tenant's metadata
- `scrutari_domain` — look up an existing domain
- `scrutari_member` — look up a tenant member

Full schema and per-attribute documentation is in the
[`docs/`](./docs) directory and on the Terraform Registry page.

## Authentication

The provider authenticates with a Scrutari API key, passed via the
`api_key` provider attribute or the `SCRUTARI_API_KEY` environment
variable. Grant each automation key the least privilege it needs. The
scopes map to the resources you manage:

| Resource | Scopes |
|---|---|
| `scrutari_route` | `routes:read`, `routes:write` |
| `scrutari_domain` | `domains:read`, `domains:write` |
| `scrutari_delegated_zone` | `zones:read`, `zones:write` |
| `scrutari_transport_policy` | `transport_policy:read`, `transport_policy:write` |
| `scrutari_pii_policy` | `pii_policy:read`, `pii_policy:write` |
| `scrutari_api_key` | `api_keys:read`, `api_keys:write` |

Mint scoped keys from the Scrutari dashboard or the gateway admin tools.
Keys are tenant-scoped; the provider acts only within the key's tenant.

## Importing existing state

Every resource supports `terraform import`. The singleton resources
(`scrutari_transport_policy`, `scrutari_pii_policy`) import with the
fixed id `tenant`; the others import by their server id. See each
resource's `import.sh` under [`examples/`](./examples).

## Development

```sh
make generate     # regenerate the API client from the OpenAPI spec
go build ./...    # build the provider
make docs         # regenerate docs/ via tfplugindocs
```

Acceptance tests run against a live gateway and create then destroy real
resources, so they are gated behind `TF_ACC=1` and a dedicated test
tenant whose key carries the `scru_test_` prefix:

```sh
TF_ACC=1 \
  SCRUTARI_HOST="https://api.edge.scrutari.ai" \
  SCRUTARI_API_KEY="scru_test_..." \
  go test ./internal/provider -v -timeout 30m
```

In CI, acceptance runs manually via the Actions "Run workflow" button
(`workflow_dispatch`), never automatically, because it mutates live
gateway state.

## Releasing

Releases are cut by GoReleaser on a `v*` tag and published to the
Terraform Registry. The release is GPG-signed (`SHA256SUMS.sig`) so the
Registry can verify provenance. See [`.goreleaser.yml`](./.goreleaser.yml)
and [`PUBLISHING.md`](./PUBLISHING.md) for the full flow.

## License

See [LICENSE](./LICENSE).

terraform {
  required_providers {
    scrutari = {
      source = "scrutari-ai/scrutari"
    }
  }
}

provider "scrutari" {
  # api_key from SCRUTARI_API_KEY env var (recommended)
  # host from SCRUTARI_HOST env var (default: https://api.edge.scrutari.ai)
}

# ── Classic mode: TXT-challenge verification ─────────────────────────
#
# The create returns a one-time TXT challenge. Publish it from the
# same configuration via your DNS provider; the gateway's verifier
# flips the row to `verified` once the record resolves.

resource "scrutari_domain" "api" {
  domain = "api.acme.example"
}

# Example: publishing the challenge with the Azure DNS provider.
# (Any DNS provider works — the two outputs below are the contract.)
#
# resource "azurerm_dns_txt_record" "scrutari_challenge" {
#   name                = trimsuffix(scrutari_domain.api.challenge_host, ".acme.example")
#   zone_name           = "acme.example"
#   resource_group_name = "rg-dns"
#   ttl                 = 300
#   record {
#     value = scrutari_domain.api.expected_value
#   }
# }

# ── Zone-backed mode: hostnames under a delegated zone ───────────────
#
# Once a scrutari_delegated_zone is `active`, hostnames under it are
# born verified — no challenge, no DNS work, served by the zone's
# wildcard certificate. The hostname must sit exactly one label under
# the zone.

resource "scrutari_domain" "checkout" {
  domain  = "checkout.ai.acme.example"
  zone_id = scrutari_delegated_zone.ai.id
}

resource "scrutari_delegated_zone" "ai" {
  zone = "ai.acme.example"
}

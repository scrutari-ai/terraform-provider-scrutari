terraform {
  required_providers {
    scrutari = {
      source = "scrutari/scrutari"
    }
  }
}

provider "scrutari" {
  # Use a key holding the zones:write scope ONLY in the configuration
  # that manages zones. Zones change yearly; hostnames change daily —
  # keep day-to-day pipelines on domains:write + routes:write keys.
}

resource "scrutari_delegated_zone" "ai" {
  zone = "ai.acme.example"
}

# Activation is a two-step DNS handshake in the PARENT zone:
#
#   1. Publish the one-time TXT proof (available right after apply):
output "zone_challenge_host" {
  value = scrutari_delegated_zone.ai.challenge_host
}
output "zone_challenge_txt" {
  value     = scrutari_delegated_zone.ai.expected_txt
  sensitive = true
}

#   2. After the proof verifies, re-run `terraform refresh` and
#      publish these NS records; the zone flips `active` once the
#      delegation resolves:
output "zone_name_servers" {
  value = scrutari_delegated_zone.ai.name_servers
}

# Hostnames under the active zone are then pure Terraform — see the
# scrutari_domain example's zone-backed mode.

terraform {
  required_providers {
    scrutari = {
      source = "scrutari/scrutari"
    }
  }
}

provider "scrutari" {
  # Use a key holding transport_policy:write only in the
  # configuration that owns the cryptographic posture. Floors change
  # on compliance cadence, not deploy cadence.
}

# The recommended adoption path: monitor first, watch the
# transport_policy.violation events and the
# scrutari_transport_policy_verdicts_total metric, then flip to
# enforce once the fleet conforms.

resource "scrutari_transport_policy" "this" {
  min_posture = "cnsa-2.0"
  mode        = "monitor"
}

# Enforce mode refuses non-conforming connections with HTTP 426 and
# only applies where the gateway deployment can PROVE the floor per
# connection — `attestable_here` tells you whether this endpoint
# can. Applying enforce with an unattestable floor fails the plan
# with a self-explaining error rather than silently not enforcing.
#
# resource "scrutari_transport_policy" "this" {
#   min_posture = "cnsa-2.0"
#   mode        = "enforce"
# }

output "floor_provable_on_this_endpoint" {
  value = scrutari_transport_policy.this.attestable_here
}

terraform {
  required_providers {
    scrutari = {
      source = "scrutari-ai/scrutari"
    }
  }
}

provider "scrutari" {
  # Use a key holding pii_policy:write only in the configuration that
  # owns data-loss-prevention posture. Redaction policy changes on
  # compliance cadence, not deploy cadence.
}

# The PII redaction policy for AI traffic. Before a prompt reaches an
# upstream AI provider, the gateway scans it for standard PII and acts
# per this policy. Non-AI passthrough traffic is never scanned, so this
# control adds no latency to the bulk proxy path.
#
# REPLACE semantics: the `rule` blocks are the COMPLETE set of
# non-default rules. Any of the eight categories you do not declare
# stays at the safe `audit` default (detect and log, forward
# untouched). `audit` is the absence of a rule, never `mode = "audit"`.
#
# Categories: ssn, us_phone, email, credit_card, mrn, npi, icd10, dob.
# Modes:      redact (replace the matched span), block (refuse with 422).

resource "scrutari_pii_policy" "this" {
  # Hard-stop the highest-sensitivity identifiers before they ever
  # leave the tenant boundary.
  rule {
    category = "ssn"
    mode     = "block"
  }
  rule {
    category = "credit_card"
    mode     = "block"
  }

  # Healthcare identifiers: redact in place so the prompt still reaches
  # the model, minus the PHI.
  rule {
    category = "mrn"
    mode     = "redact"
  }
  rule {
    category = "icd10"
    mode     = "redact"
  }

  # email, us_phone, npi, and dob are not declared here, so they remain
  # at the audit default: detected and logged, forwarded untouched.
}

# updated_at is the RFC 3339 timestamp of the last write, or null while
# the document is the all-audit default.
output "pii_policy_last_written" {
  value = scrutari_pii_policy.this.updated_at
}

# The PII policy is a per-tenant singleton (the tenant comes from the
# API key), so the import ID is the literal `tenant`.
terraform import scrutari_pii_policy.this tenant

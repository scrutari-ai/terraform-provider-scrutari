# The transport policy is a per-tenant singleton (the tenant comes
# from the API key), so the import ID is the literal `tenant`.
terraform import scrutari_transport_policy.this tenant

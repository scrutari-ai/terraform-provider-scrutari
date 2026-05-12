resource "scrutari_api_key" "ci_terraform" {
  name        = "terraform-ci"
  scopes      = ["routes:read", "routes:write"]
  environment = "live"
}

# Plaintext is returned ONCE at creation. Pipe it to a secret manager
# (Vault / AWS Secrets Manager / GCP Secret Manager) — never log it.
output "api_key_plaintext" {
  value     = scrutari_api_key.ci_terraform.plaintext
  sensitive = true
}

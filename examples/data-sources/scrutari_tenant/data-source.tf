data "scrutari_tenant" "self" {}

output "current_plan" {
  value = data.scrutari_tenant.self.plan
}

output "rate_limit_rps" {
  value = data.scrutari_tenant.self.rate_limit_rps
}

output "feature_flags" {
  # `features` is a JSON-encoded string. Decode to read individual flags.
  value = jsondecode(data.scrutari_tenant.self.features)
}

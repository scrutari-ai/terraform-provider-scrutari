terraform {
  required_providers {
    scrutari = {
      source = "scrutari/scrutari"
    }
  }
}

provider "scrutari" {
  # api_key from SCRUTARI_API_KEY env var (recommended)
  # host from SCRUTARI_HOST env var (default: https://api.edge.scrutari.ai)
}

resource "scrutari_route" "billing" {
  host                 = "api.acme-corp.scrutari.ai"
  path_prefix          = "/billing"
  upstream_url         = "https://stripe-proxy.acme-corp.internal"
  strip_path_prefix    = true
  preserve_host_header = false
  auth_mode            = "required"
}

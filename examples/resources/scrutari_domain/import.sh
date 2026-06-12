# Import by the numeric verification id from `GET /v1/domains` (or
# the dashboard's Domains page). Two create-only fields cannot be
# recovered by import: `expected_value` (the one-time TXT secret) and
# `zone_id` (not exposed by the read API yet).
terraform import scrutari_domain.api 17

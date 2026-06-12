# Import by the zone UUID from `GET /v1/zones`. The one-time
# parent-zone TXT proof (`expected_txt`) cannot be recovered by
# import — it is delivered exactly once, on the original create.
terraform import scrutari_delegated_zone.ai 3f8a3a1e-9d2b-4c1f-8a55-0123456789ab

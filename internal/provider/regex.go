package provider

import "regexp"

// uuidRegex matches the canonical hyphenated UUID form. Used by
// member_data_source.go's user_id validator. Defined once in its own
// file so additional resource/data-source files can reuse it without
// re-declaring.
var uuidRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// fqdnRegex is a cheap plan-time syntax gate for hostnames: dotted
// labels of [a-z0-9-], no leading/trailing hyphen per label, at
// least one dot. Deliberately LOOSER than the gateway's
// `is_valid_fqdn` (which also enforces length caps) — the gateway
// stays authoritative; this only catches obvious junk (schemes,
// spaces, paths) before an API round-trip.
var fqdnRegex = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// zoneRegex: a delegable zone needs at least THREE labels (apex
// delegation of a registrable domain is out of scope, RFC-010 §8) —
// so two dots minimum. Same looser-than-gateway posture as
// fqdnRegex; the admission gate is authoritative.
var zoneRegex = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.){2,}[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

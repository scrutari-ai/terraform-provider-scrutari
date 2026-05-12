package provider

import "regexp"

// uuidRegex matches the canonical hyphenated UUID form. Used by
// member_data_source.go's user_id validator. Defined once in its own
// file so additional resource/data-source files can reuse it without
// re-declaring.
var uuidRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

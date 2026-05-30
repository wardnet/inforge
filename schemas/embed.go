// Package schemas embeds the JSON Schemas that resource files are validated
// against. The compiled schemas are consumed by internal/validate.
package schemas

import "embed"

// FS holds the embedded JSON Schema documents, one per resource type
// (network.json, compute.json, dns.json, database.json, secrets.json,
// service.json). Each file's "$id" matches its filename.
//
//go:embed *.json
var FS embed.FS

// Package api embeds the OpenAPI document of the /v1 HTTP API.
package api

import _ "embed"

// OpenAPI is api/openapi.json, the machine-readable contract (a test keeps
// it in sync with the routes and response fields).
//
//go:embed openapi.json
var OpenAPI []byte

// Package compat freezes the pre-migration public API and storage metadata.
package compat

import _ "embed"

//go:embed spec.json
var Spec []byte

//go:embed schema.sql
var Schema string

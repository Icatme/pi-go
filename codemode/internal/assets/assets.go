// Package assets owns the reviewed, immutable guest artifact.
package assets

import _ "embed"

const Version = "quickjs-wasi 3.6.2"
const SHA256 = "d4c9375f2b1ca4dc95f72c8aa2982a7a9951ac8011490d79c6582df732b4bbd9"

//go:embed quickjs.wasm
var WASM []byte

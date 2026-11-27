// Package capi carries the C SDK's interface of native modules
// (kario_native.h) for the kario command, which builds the Node.js side of
// it (sdk/ts/node/native.c) for apps with native code. A Go package cannot
// sit beside the SDK's C sources, hence its own directory.
package capi

import _ "embed"

//go:embed kario_native.h
var NativeHeader []byte

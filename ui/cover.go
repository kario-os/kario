//go:build kariocover

package ui

import "github.com/kario-os/kario/tools/covflush"

// A coverage build's programs that link this package write their counters
// while they run (tools/covflush).
func init() { covflush.Start() }

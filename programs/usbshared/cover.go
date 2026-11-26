//go:build kariocover

package usbshared

import "github.com/kario-os/kario/tools/covflush"

func init() { beforeConfine = covflush.Confine }

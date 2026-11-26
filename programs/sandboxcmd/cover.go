//go:build kariocover

package sandboxcmd

import "github.com/kario-os/kario/tools/covflush"

func init() { beforeExec = covflush.Flush }

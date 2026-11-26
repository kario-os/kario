//go:build !unix

package ime

import "os"

func mapFile(path string) ([]byte, error) { return os.ReadFile(path) }

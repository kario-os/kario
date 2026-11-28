// phone-factory-test is programs/factorytest as a program of its own: the
// station's (phone-factory-test --unit 10.71.0.1:7171, docs/40-testing.md
// §10.4) and for development; the image runs it as kario-system, called
// phone-factory-test.
package main

import "github.com/kario-os/kario/programs/factorytest"

func main() { factorytest.Main() }

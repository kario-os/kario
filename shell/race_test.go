//go:build race

package shell

// raceScale stretches the scenario's timeouts under the race detector, which
// runs the Shell several times slower (2 to 20 times, Go's documentation
// says), as --timeout-scale does under QEMU's emulation.
const raceScale = 4

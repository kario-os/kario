// Package fingerprint is the fingerprint sensor's HAL: a sensor reads a
// finger (Capture), an enrolment turns several readings of one finger into
// a template, and matching compares a reading with a template. All of it
// runs inside phone-keystored, which stands in for the TEE (R15): readings
// and templates never leave it; the shell only learns "matched" through an
// authentication token.
//
// Backends. The developer machine and QEMU use Sim, touched by tests with
// named fingers. The sensors of the phones this system targets (FPC,
// Goodix: SPI devices) have no mainline driver: their vendors drive them
// from a trusted application in the TEE, which also enrols and matches, so
// a backend for one is a client of that TA and depends on the vendor's
// TEE (not implemented; the test phone, a Redmi Note 4X, has an FPC sensor).
// A USB sensor that libfprint (LGPL) supports could be a backend only by
// talking to libfprint in a separate process, never linked in.
package fingerprint

import (
	"context"
	"errors"
)

// Sample is one reading of a finger: the features the matcher compares.
type Sample struct {
	Features []byte
}

// Template is an enrolled finger, opaque outside its backend.
type Template []byte

// Progress is how far an enrolment is: Done of Steps touches counted.
type Progress struct {
	Done, Steps int
	// Rejected says why the last touch did not count ("" if it did).
	Rejected string
}

// Why a touch did not count during an enrolment.
const (
	RejectOtherFinger = "other_finger" // not the finger the enrolment started with
	RejectEnrolled    = "enrolled"     // that finger is enrolled already
)

// Sensor is a fingerprint sensor and its matcher.
type Sensor interface {
	// Name says which backend it is ("simulated").
	Name() string
	// Capture waits for a finger on the sensor and reads it, until ctx
	// ends.
	Capture(ctx context.Context) (Sample, error)
	// Enroll starts the enrolment of one finger.
	Enroll() Enrollment
	// Match reports whether the sample is the template's finger.
	Match(t Template, s Sample) bool
}

// Enrollment collects the touches of one finger.
type Enrollment interface {
	// Steps is how many touches it takes.
	Steps() int
	Add(s Sample) Progress
	// Template is the finger once Progress.Done reached Steps.
	Template() Template
}

// ErrNoDriver: the sensor named has no backend on this system.
var ErrNoDriver = errors.New("no driver for this fingerprint sensor")

// Open returns the backend a board names: "sim" (or "") for the simulated
// sensor, "none" for a phone without a usable one (nil, nil).
func Open(name string) (Sensor, error) {
	switch name {
	case "", "sim":
		return NewSim(), nil
	case "none":
		return nil, nil
	}
	return nil, ErrNoDriver
}

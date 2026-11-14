package keystore

// phone-keystored's methods for the lock-screen PIN and the fingerprints
// (svc names its others). The shell is the only client: it carries PINs
// from its system screens and tokens between the methods, nothing more.
const (
	MethodLockStatus   = "lock.status"    // → LockStatus
	MethodLockSetPIN   = "lock.set_pin"   // {old, pin} → PINResult
	MethodLockClearPIN = "lock.clear_pin" // {old} → PINResult
	MethodLockVerify   = "lock.verify"    // {pin, challenge} → PINResult
	MethodFPChallenge  = "fp.challenge"   // → {challenge}
	MethodFPEnroll     = "fp.enroll"      // {token, name} → {steps}
	MethodFPEnrollNext = "fp.enroll_next" // → EnrollStep, after the next touch
	MethodFPAuth       = "fp.auth"        // {app?, key?} → AuthResult, after the next touch
	MethodFPCancel     = "fp.cancel"
	MethodFPRename     = "fp.rename"   // {id, name}
	MethodFPRemove     = "fp.remove"   // {token, id}
	MethodConfirmBio   = "confirm_bio" // {app, key, alg, prompt, payload, token} → ui.KeyProof
	// MethodFPSimTouch touches the simulated sensor with a named finger
	// (the shell's selftest "finger" command), as svc.TelSimRing rings the
	// simulated baseband; a real sensor has no such method.
	MethodFPSimTouch = "fp.sim_touch" // {finger}
)

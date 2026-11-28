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
	// Minor mode's parental PIN (parent.go): a right one gives no token.
	MethodParentStatus   = "parent.status"    // → ParentStatus
	MethodParentSetPIN   = "parent.set_pin"   // {old, pin} → PINResult
	MethodParentClearPIN = "parent.clear_pin" // {old} → PINResult
	MethodParentVerify   = "parent.verify"    // {pin} → PINResult
	MethodParentResetPIN = "parent.reset_pin" // {pin} → PINResult, after the parent's signed reset
	// The system's own secrets (secret.go): data is base64.
	MethodSecretSeal = "secret.seal" // {purpose, data} → {sealed}
	MethodSecretOpen = "secret.open" // {purpose, sealed} → {data}
	// MethodFPSimTouch touches the simulated sensor with a named finger
	// (the shell's selftest "finger" command), as svc.TelSimRing rings the
	// simulated baseband; a real sensor has no such method.
	MethodFPSimTouch = "fp.sim_touch" // {finger} → {read}
	// MethodFPSimPlace moves the simulated sensor (the selftest's
	// "fingerprint-at"): into the OK key (ui.FingerprintOKKey) or out of
	// it (""), as a keypad with one in its OK key would have it.
	MethodFPSimPlace = "fp.sim_place" // {name}
)

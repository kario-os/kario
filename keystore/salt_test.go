package keystore

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

// wipeKeeping empties the state dir as a factory reset does (package wipe):
// only the device keys and the factory's files stay, and a new data salt
// is written.
func wipeKeeping(t *testing.T, dir string) {
	t.Helper()
	keep := map[string][]byte{}
	for _, rel := range []string{"keystore/device.key", "keystore/device-sm2.key", "factory/root.pub", "factory/root.key", "factory/root-sm2.key"} {
		raw, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		keep[rel] = raw
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	for rel, raw := range keep {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o700)
		if err := os.WriteFile(filepath.Join(dir, rel), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := NewDataSalt(dir); err != nil {
		t.Fatal(err)
	}
}

// A factory reset keeps the device's identity (its ID, keys and
// certificate) but not what the data area held: a sealed secret copied from
// before the reset no longer opens, and the PINs are gone.
func TestDataSaltAfterAReset(t *testing.T) {
	s, dir := open(t)
	if _, err := os.Stat(DataSaltPath(dir)); err != nil {
		t.Fatalf("a new store has no data salt: %v", err)
	}
	sealed, err := s.SealSecret("carddav:a1", []byte("app password"))
	if err != nil {
		t.Fatal(err)
	}
	if r, err := s.SetLockPIN("", "2468"); err != nil || !r.OK {
		t.Fatalf("lock PIN: %+v %v", r, err)
	}
	if r, err := s.SetParentPIN("", "1357"); err != nil || !r.OK {
		t.Fatalf("parental PIN: %+v %v", r, err)
	}
	id, pub, _ := s.CSR()
	cert := s.devCert
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := again.OpenSecret("carddav:a1", sealed); err != nil || string(plain) != "app password" {
		t.Fatalf("the same data area: %q %v", plain, err)
	}

	wipeKeeping(t, dir)
	after, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id2, pub2, _ := after.CSR()
	if id2 != id || pub2 != pub || after.devCert != cert {
		t.Fatalf("identity %s %s, was %s %s", id2, pub2, id, pub)
	}
	if _, err := after.OpenSecret("carddav:a1", sealed); err == nil {
		t.Fatal("a secret sealed before the reset opened after it")
	}
	if after.LockStatus().PIN || after.ParentStatus().PIN {
		t.Fatalf("PINs after the reset: lock %v, parental %v", after.LockStatus().PIN, after.ParentStatus().PIN)
	}
}

// A store from before data salts (its device key there, no salt) keeps its
// derivation: what it sealed still opens, and no salt is made for it.
func TestStoreWithoutADataSalt(t *testing.T) {
	dir := t.TempDir()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	if err := os.MkdirAll(filepath.Join(dir, "keystore"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keystore", "device.key"), []byte(b64.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(DataSaltPath(dir)); err == nil {
		t.Fatal("a salt was made for an existing store")
	}
	sealed, err := s.SealSecret("p", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := again.OpenSecret("p", sealed); err != nil || string(plain) != "x" {
		t.Fatalf("%q %v", plain, err)
	}
	if DeviceIDFor(priv.Public().(ed25519.PublicKey)) != s.deviceID {
		t.Fatalf("DeviceIDFor %s, the store's %s", DeviceIDFor(priv.Public().(ed25519.PublicKey)), s.deviceID)
	}
	if err := os.WriteFile(DataSaltPath(dir), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("a corrupt salt was taken")
	}
}

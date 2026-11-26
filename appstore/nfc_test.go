package appstore

import "testing"

// The store checks a submission's nfc cards as the phone's installer does.
func TestNFCCardsChecked(t *testing.T) {
	_, ts := newServer(t)
	dev := developer(t, ts, "Carol", "91110108MA01ABCD2H")
	card := map[string]any{"id": "metro", "category": "other", "description": "Metro card", "aids": []string{"F04651545241"}, "express": true}
	app := func(perms []string, cards ...map[string]any) map[string]any {
		return map[string]any{"id": "org.example.metro", "name": "Metro", "version": "1.0", "exec": "phone-app-x", "permissions": perms, "nfc": cards}
	}
	if code, _ := submit(t, ts, build(t, dev, app(nil, card))); code != 422 {
		t.Fatalf("cards without the nfc permission: %d", code)
	}
	bad := map[string]any{"id": "metro", "category": "other", "description": "Metro card", "aids": []string{"325041592E5359532E4444463031"}}
	if code, _ := submit(t, ts, build(t, dev, app([]string{"nfc"}, bad))); code != 422 {
		t.Fatalf("the PPSE in a transit card: %d", code)
	}
	if code, st := submit(t, ts, build(t, dev, app([]string{"nfc"}, card))); code != 200 || st != "pending_review" {
		t.Fatalf("a transit card: %d %s", code, st)
	}
}

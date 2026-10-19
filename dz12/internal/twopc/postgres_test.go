package twopc

import "testing"

func TestGIDRejectsUntrustedAndNoncanonicalIDs(t *testing.T) {
	for _, id := range []string{"", "x'; COMMIT; --", "a3f010901592485da3475d320ae77db9", "A3F01090-1592-485D-A347-5D320AE77DB9"} {
		if _, err := GID("catalog", id); err == nil {
			t.Errorf("accepted unsafe ID %q", id)
		}
	}
	gid, err := GID("catalog", "a3f01090-1592-485d-a347-5d320ae77db9")
	if err != nil || gid != "marketplace:catalog:a3f01090-1592-485d-a347-5d320ae77db9" {
		t.Fatalf("gid=%q error=%v", gid, err)
	}
	if _, err := GID("untrusted'", "a3f01090-1592-485d-a347-5d320ae77db9"); err == nil {
		t.Fatal("untrusted domain accepted")
	}
}

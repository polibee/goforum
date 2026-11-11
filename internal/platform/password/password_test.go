package password

import "testing"

func TestHashVerify(t *testing.T) {
	hash, err := Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !Verify(hash, "correct horse battery staple") {
		t.Fatal("expected password to verify")
	}
	if Verify(hash, "wrong password") {
		t.Fatal("wrong password must not verify")
	}
	if NeedsRehash(hash) {
		t.Fatal("fresh hash should not need rehash")
	}
	m, it, p, err := ParseParams(hash)
	if err != nil {
		t.Fatalf("parse params: %v", err)
	}
	if m != memory || it != iterations || p != parallelism {
		t.Fatalf("unexpected params m=%d t=%d p=%d", m, it, p)
	}
}

func TestVerifyMalformed(t *testing.T) {
	for _, encoded := range []string{"", "plain", "$argon2id$v=19$bad$salt$hash"} {
		if Verify(encoded, "x") {
			t.Fatalf("malformed hash accepted: %q", encoded)
		}
	}
}

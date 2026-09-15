package wakeup

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func keypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// keyid is the SHA-256 hex of the canonical key object; tests only need a
	// stable distinct id, so use the raw public key hex.
	return pub, priv, hex.EncodeToString(pub)
}

func sign(t *testing.T, priv ed25519.PrivateKey, v int64, topic string, seq int64, keyid string) Sig {
	t.Helper()
	msg, err := SignedBytes(v, topic, seq)
	if err != nil {
		t.Fatal(err)
	}
	return Sig{KeyID: keyid, Sig: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, msg))}
}

func TestSignedBytesDomainSeparatedOLPC(t *testing.T) {
	msg, err := SignedBytes(1, "abc", 7)
	if err != nil {
		t.Fatal(err)
	}
	// OLPC sorts object keys: seq, t, v.
	want := `keryx/wakeup/v1|{"seq":7,"t":"abc","v":1}`
	if string(msg) != want {
		t.Fatalf("signed bytes = %q, want %q", msg, want)
	}
}

func TestVerifyThreshold(t *testing.T) {
	pub1, priv1, id1 := keypair(t)
	pub2, priv2, id2 := keypair(t)
	_, _, id3 := keypair(t)
	keys := map[string]ed25519.PublicKey{id1: pub1, id2: pub2}

	// Threshold 1: one valid signature suffices.
	sigs := []Sig{sign(t, priv1, 1, "t", 5, id1)}
	if err := Verify(1, "t", 5, sigs, keys, 1); err != nil {
		t.Fatalf("threshold 1 rejected: %v", err)
	}
	// Extra unknown-key signatures neither count nor invalidate.
	sigs = append(sigs, sign(t, priv2, 1, "t", 5, id3))
	if err := Verify(1, "t", 5, sigs, keys, 1); err != nil {
		t.Fatalf("extra unknown signature rejected: %v", err)
	}
	// Duplicate keyids do not count twice.
	dup := []Sig{sign(t, priv1, 1, "t", 5, id1), sign(t, priv1, 1, "t", 5, id1)}
	if err := Verify(1, "t", 5, dup, keys, 2); err == nil {
		t.Fatal("duplicate keyids satisfied threshold 2")
	}
	// Threshold 2 with two distinct keys verifies.
	two := []Sig{sign(t, priv1, 1, "t", 5, id1), sign(t, priv2, 1, "t", 5, id2)}
	if err := Verify(1, "t", 5, two, keys, 2); err != nil {
		t.Fatalf("threshold 2 rejected: %v", err)
	}
	// A known keyid with an invalid signature rejects the wake-up.
	bad := []Sig{{KeyID: id1, Sig: base64.RawURLEncoding.EncodeToString(make([]byte, 64))}}
	if err := Verify(1, "t", 5, bad, keys, 1); err == nil {
		t.Fatal("accepted invalid signature")
	}
	// A different topic or seq invalidates the signature.
	if err := Verify(1, "other", 5, sigs[:1], keys, 1); err == nil {
		t.Fatal("accepted signature over a different topic")
	}
	if err := Verify(1, "t", 6, sigs[:1], keys, 1); err == nil {
		t.Fatal("accepted signature over a different seq")
	}
}

func TestParseStrict(t *testing.T) {
	pub, priv, id := keypair(t)
	s := sign(t, priv, 1, "abc", 7, id)
	valid := `{"v":1,"t":"abc","seq":7,"sig":[{"keyid":"` + id + `","sig":"` + s.Sig + `"}]}`
	w, err := Parse([]byte(valid))
	if err != nil {
		t.Fatalf("rejected valid envelope: %v", err)
	}
	if w.V != 1 || w.T != "abc" || w.Seq != 7 {
		t.Fatalf("parsed = %+v", w)
	}
	if err := Verify(w.V, w.T, w.Seq, w.Sig, map[string]ed25519.PublicKey{id: pub}, 1); err != nil {
		t.Fatalf("parsed envelope does not verify: %v", err)
	}

	bad := []string{
		`{"v":2,"t":"abc","seq":7,"sig":[{"keyid":"x","sig":"y"}]}`,                // unknown version
		`{"v":1,"t":"abc","seq":7,"sig":[{"keyid":"x","sig":"y"}],"n":3}`,          // unknown field
		`{"v":1,"t":"abc","seq":7,"seq":8,"sig":[{"keyid":"x","sig":"y"}]}`,        // duplicate member
		`{"v":1,"seq":7,"sig":[{"keyid":"x","sig":"y"}]}`,                          // missing t
		`{"v":1,"t":"abc","seq":7}`,                                                // missing sig
		`{"v":1,"t":"abc","seq":0,"sig":[{"keyid":"x","sig":"y"}]}`,                // seq < 1
		`{"v":1,"t":"abc","seq":1.5,"sig":[{"keyid":"x","sig":"y"}]}`,              // non-integer
		`{"v":1,"t":"abc","seq":9007199254740992,"sig":[{"keyid":"x","sig":"y"}]}`, // seq over 2^53-1
		`{"v":1,"t":"abc","seq":7,"sig":[]}`,                                       // empty sig
		`{"v":1,"t":"abc","seq":7,"sig":[{"keyid":"x","sig":"y"}]} trailing`,       // trailing data
	}
	for _, b := range bad {
		if _, err := Parse([]byte(b)); err == nil {
			t.Fatalf("accepted invalid envelope: %s", b)
		}
	}
	_ = pub
}

func TestSortKeyIDs(t *testing.T) {
	got := SortKeyIDs([]Sig{{KeyID: "b"}, {KeyID: "a"}})
	if strings.Join(got, ",") != "a,b" {
		t.Fatalf("got %v", got)
	}
}

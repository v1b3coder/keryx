package notify

import "testing"

// The app derives the same topic for the demo company's security channel
// (app/src/lib/relay.test.ts fixture): company 127.0.0.1, channel security.
const (
	appCompany = "127.0.0.1"
	appChannel = "security"
	appScopeID = "eea1254e05830616fa0e1a5a1db21cf4a0684aed4498cba74dda77f12342a9f8"
	appH       = "3aa3eb44c7889aa11dded2e4c9d3da1b5d26a72f929fe20c275132524765d48e"
	appTopic   = "c9U4I55zbVzbbAXny43KvSDFLMpbqPGk3ij_Ds_v7g4"
)

func TestDerivationMatchesTheApp(t *testing.T) {
	scopeID, err := PublicScopeID(appChannel)
	if err != nil {
		t.Fatal(err)
	}
	if scopeID != appScopeID {
		t.Fatalf("scope_id = %q, want %q", scopeID, appScopeID)
	}
	h := SourceHash(appCompany + "|" + appChannel)
	if h != appH {
		t.Fatalf("h = %q, want %q", h, appH)
	}
	topic, err := Derive(appCompany, scopeID, h)
	if err != nil {
		t.Fatal(err)
	}
	if topic != appTopic {
		t.Fatalf("topic = %q, want %q", topic, appTopic)
	}
}

func TestSignedBytesDomainSeparatedOLPC(t *testing.T) {
	msg, err := SignedBytes(1, "abc", 7)
	if err != nil {
		t.Fatal(err)
	}
	// OLPC sorts object keys: seq, t, v.
	if string(msg) != `keryx/wakeup/v1|{"seq":7,"t":"abc","v":1}` {
		t.Fatalf("signed bytes = %q", msg)
	}
}

package client

import (
	"encoding/json"
	"reflect"
	"testing"

	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
)

// Acquire's published closed outcome vocabulary stays unchanged when a new
// method is added to leases@1 (VISION 2026-10-01, service compatibility).
func TestAcquirePublishedOutcomeRefusesHeld(t *testing.T) {
	want := []AcquireOutcome{AcquireOutcomeAcquired, AcquireOutcomeInsufficient,
		AcquireOutcomeHoldersRefused, AcquireOutcomeNotPermitted,
		AcquireOutcomeUnavailable, AcquireOutcomeInvalid, AcquireOutcomeForbidden}
	if got := AcquireOutcomeValues(); !reflect.DeepEqual(got, want) {
		t.Fatalf("published AcquireOutcome changed: got %v, want %v", got, want)
	}
	if _, known := ParseAcquireOutcome("held"); known {
		t.Error("published AcquireOutcome accepts held")
	}
	var outcome AcquireOutcome
	if err := json.Unmarshal([]byte(`"held"`), &outcome); err == nil {
		t.Error("published AcquireOutcome codec accepts held")
	}
	var result AcquireResult
	if err := json.Unmarshal([]byte(`{"outcome":"held","asked":[]}`), &result); err == nil {
		t.Error("published AcquireResult codec accepts held")
	}
	if err := json.Unmarshal([]byte(`{"outcome":"holders_refused","asked":[]}`), &result); err != nil || result.Outcome != AcquireOutcomeHoldersRefused {
		t.Fatalf("published AcquireResult refuses its holders_refused outcome: %+v %v", result, err)
	}
}

type acquireReplyTransport struct{}

func (acquireReplyTransport) ExchangeFrame(frame []byte) ([]byte, error) {
	var request struct{ Method string }
	if err := json.Unmarshal(frame, &request); err != nil {
		return nil, err
	}
	outcome := "holders_refused"
	if request.Method == "AcquireFree" {
		outcome = "held"
	}
	return []byte(`{"version":1,"service":"abstraction.resource/leases@1","method":"` + request.Method + `","ok":true,"payload":{"value":{"outcome":"` + outcome + `","asked":[]}}}`), nil
}

func TestAcquireAndAcquireFreeDecodeTheirOwnOutcomes(t *testing.T) {
	client := wire.NewLeasesClient(acquireReplyTransport{})
	plain, err := client.Acquire("card:0", 4096, 0)
	if err != nil || plain.Outcome != AcquireOutcomeHoldersRefused || plain.Lease != nil || len(plain.Asked) != 0 {
		t.Fatalf("Acquire changed its published result: %+v %v", plain, err)
	}
	free, err := client.AcquireFree("card:0", 4096)
	if err != nil || free.Outcome != AcquireFreeOutcomeHeld || free.Lease != nil || len(free.Asked) != 0 {
		t.Fatalf("AcquireFree lost its own held result: %+v %v", free, err)
	}
	if _, known := ParseAcquireFreeOutcome("holders_refused"); known {
		t.Fatal("AcquireFree declares an outcome that requires asking holders")
	}
}

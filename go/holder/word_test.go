package holder

import (
	"errors"
	"fmt"
	"testing"

	res "github.com/openabstractions/abstraction-resource/go/client"
)

// The lease refusal words have one owner: each acquire outcome a provider
// tells its caller, and the lease ended twice while loading, read one word;
// any other stage or outcome reads none.
func TestFailureWordIsTheLeaseRefusal(t *testing.T) {
	for _, tc := range []struct {
		failure *Failure
		want    string
	}{
		{&Failure{Stage: StageAcquire, Result: res.AcquireResult{Outcome: res.AcquireOutcomeNotPermitted}}, WordNotPermitted},
		{&Failure{Stage: StageAcquire, Result: res.AcquireResult{Outcome: res.AcquireOutcomeInsufficient}}, WordInsufficient},
		{&Failure{Stage: StageAcquire, Result: res.AcquireResult{Outcome: res.AcquireOutcomeHoldersRefused}}, WordHoldersRefused},
		{&Failure{Stage: StageAcquire, FreeResult: &res.AcquireFreeResult{Outcome: res.AcquireFreeOutcomeHeld}}, WordHeld},
		{&Failure{Stage: StageAcquire, FreeResult: &res.AcquireFreeResult{Outcome: res.AcquireFreeOutcomeNotPermitted}}, WordNotPermitted},
		{&Failure{Stage: StageAcquire, FreeResult: &res.AcquireFreeResult{Outcome: res.AcquireFreeOutcomeInsufficient}}, WordInsufficient},
		{&Failure{Stage: StageAcquire, FreeResult: &res.AcquireFreeResult{Outcome: res.AcquireFreeOutcomeUnavailable}}, ""},
		{&Failure{Stage: StageAcquire, Err: fmt.Errorf("load: %w", ErrLeaseEnded)}, WordEnded},
		{&Failure{Stage: StageAcquire, Result: res.AcquireResult{Outcome: res.AcquireOutcomeUnavailable}}, ""},
		{&Failure{Stage: StageAcquire, Err: errors.New("no lease service")}, ""},
		{&Failure{Stage: StageStart, Err: ErrLeaseEnded}, ""},
		{nil, ""},
	} {
		if got := tc.failure.Word(); got != tc.want {
			t.Fatalf("%+v reads %q, want %q", tc.failure, got, tc.want)
		}
	}
}

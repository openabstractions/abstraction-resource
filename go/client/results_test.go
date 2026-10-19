package client

import (
	"errors"
	"testing"

	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
)

func TestCallResultPreservesTypedRefusal(t *testing.T) {
	tests := []struct {
		name    string
		outcome wire.ResourceCallOutcome
		code    wire.ServiceErrorCode
	}{
		{"ok", wire.ResourceCallOutcomeOk, ""},
		{"forbidden", wire.ResourceCallOutcomeForbidden, wire.ServiceErrorCodeForbidden},
		{"unavailable", wire.ResourceCallOutcomeUnavailable, wire.ServiceErrorCodePolicyUnavailable},
		{"invalid", wire.ResourceCallOutcomeInvalid, wire.ServiceErrorCodeInvalidRequest},
		{"unknown", wire.ResourceCallOutcomeUnknown, wire.ServiceErrorCode("unknown")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := callResultError("Holders", tt.outcome)
			if tt.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var refusal *wire.ServiceError
			if !errors.As(err, &refusal) || refusal.Code != tt.code {
				t.Fatalf("got %v, want structured code %s", err, tt.code)
			}
		})
	}
}

package client

import (
	"errors"
	"testing"

	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
)

func TestCallResultPreservesTypedRefusalAndPayloadInvariant(t *testing.T) {
	tests := []struct {
		name    string
		outcome wire.ResourceCallOutcome
		payload bool
		code    wire.ServiceErrorCode
	}{
		{"ok", wire.ResourceCallOutcomeOk, true, ""},
		{"forbidden", wire.ResourceCallOutcomeForbidden, false, wire.ServiceErrorCodeForbidden},
		{"unavailable", wire.ResourceCallOutcomeUnavailable, false, wire.ServiceErrorCodePolicyUnavailable},
		{"invalid", wire.ResourceCallOutcomeInvalid, false, wire.ServiceErrorCodeInvalidRequest},
		{"unknown", wire.ResourceCallOutcomeUnknown, false, wire.ServiceErrorCode("unknown")},
		{"missing ready payload", wire.ResourceCallOutcomeOk, false, wire.ServiceErrorCodeInvalidResult},
		{"refusal with payload", wire.ResourceCallOutcomeForbidden, true, wire.ServiceErrorCodeInvalidResult},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := callResultError("Holders", tt.outcome, tt.payload)
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

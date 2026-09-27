package service

import (
	"testing"

	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
)

type yieldPageHandler struct {
	wire.Leases
	page wire.YieldPage
}

func (h yieldPageHandler) Observe(string, int64) (wire.ObserveResult, error) {
	return wire.ObserveResult{Outcome: wire.ResourceCallOutcomeOk, Page: &h.page}, nil
}

type yieldPageTransport struct{ handler yieldPageHandler }

func (t yieldPageTransport) ExchangeFrame(frame []byte) ([]byte, error) {
	return wire.ServeEndpoint(frame, "test", "", &wire.LeasesDispatcher{Handler: t.handler})
}

func TestGeneratedObserveCarriesExactLease(t *testing.T) {
	page := wire.YieldPage{Requests: []wire.YieldRequest{{ID: "ask-1", Resource: "card:0",
		Amount: 4096, Reason: "asker", At: "2026-09-26T00:00:00.000000Z", Lease: "lease-42"}}, Next: "1"}
	client := wire.NewLeasesClient(yieldPageTransport{handler: yieldPageHandler{page: page}})
	got, err := client.Observe("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != wire.ResourceCallOutcomeOk || got.Page == nil || len(got.Page.Requests) != 1 || got.Page.Requests[0].Lease != "lease-42" {
		t.Fatalf("generated observe lost the target lease: %+v", got)
	}
}

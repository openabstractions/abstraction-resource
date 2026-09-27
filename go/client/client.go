// Package client reads abstraction.resource/table@1 through the framed
// service. It measures nothing itself.
package client

import (
	"context"
	"os"
	"time"

	"github.com/openabstractions/abstraction-identity/listen"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
)

// EnvEndpoint names the endpoint for a caller that resolves nothing.
const EnvEndpoint = "ABSTRACTION_RESOURCE_ENDPOINT"

type Client struct{ transport listen.FrameClient }

func DefaultEndpoint() string {
	if s := os.Getenv(EnvEndpoint); s != "" {
		return s
	}
	return listen.Endpoint("resource-table-v1")
}

func Discover() *Client { return New(DefaultEndpoint()) }

func New(endpoint string) *Client {
	return NewWithTransport(listen.FrameClient{Endpoint: endpoint})
}

// NewWithTransport retains the caller's endpoint, server trust and waiting limits.
func NewWithTransport(transport listen.FrameClient) *Client {
	return &Client{transport: transport.WithDefaults(10*time.Second, 1<<20)}
}

// Resources are the resources this machine's table can report.
func (c *Client) Resources() (ResourceList, error) {
	return c.ResourcesContext(context.Background())
}

// ResourcesContext uses ctx only for this operation.
func (c *Client) ResourcesContext(ctx context.Context) (ResourceList, error) {
	if err := ctx.Err(); err != nil {
		return ResourceList{}, err
	}
	reply, err := wire.NewTableClient(c.transport.WithContext(ctx)).Resources()
	if err != nil {
		return ResourceList{}, err
	}
	if err := callResultError("Resources", reply.Outcome, reply.List != nil); err != nil {
		return ResourceList{}, err
	}
	return *reply.List, nil
}

// Holders is who holds resource. fresh asks the service to read its
// instrument again rather than stand on the sample it holds.
func (c *Client) Holders(resource string, fresh bool) (ResourceState, error) {
	return c.HoldersContext(context.Background(), resource, fresh)
}

// HoldersContext uses ctx only for this operation.
func (c *Client) HoldersContext(ctx context.Context, resource string, fresh bool) (ResourceState, error) {
	if err := ctx.Err(); err != nil {
		return ResourceState{}, err
	}
	reply, err := wire.NewTableClient(c.transport.WithContext(ctx)).Holders(resource, fresh)
	if err != nil {
		return ResourceState{}, err
	}
	if err := callResultError("Holders", reply.Outcome, reply.State != nil); err != nil {
		return ResourceState{}, err
	}
	return *reply.State, nil
}

// callResultError preserves typed non-success replies through the compact Go
// client API, and refuses a reply that violates the outcome/payload invariant.
func callResultError(operation string, outcome wire.ResourceCallOutcome, payload bool) error {
	if outcome == wire.ResourceCallOutcomeOk && payload {
		return nil
	}
	if outcome == wire.ResourceCallOutcomeOk || payload || !outcome.Known() {
		return &wire.ServiceError{Code: wire.ServiceErrorCodeInvalidResult, Message: operation + " outcome/payload mismatch"}
	}
	code := wire.ServiceErrorCode(outcome.String())
	switch outcome {
	case wire.ResourceCallOutcomeUnavailable:
		code = wire.ServiceErrorCodePolicyUnavailable
	case wire.ResourceCallOutcomeInvalid:
		code = wire.ServiceErrorCodeInvalidRequest
	}
	return &wire.ServiceError{Code: code, Message: operation + " outcome " + outcome.String()}
}

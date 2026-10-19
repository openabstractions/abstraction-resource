// Package client reads abstraction.resource/table@1 through the framed
// service. It measures nothing itself.
package client

import (
	"context"
	"fmt"
	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	"os"
	"time"

	"github.com/openabstractions/abstraction-identity/listen"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
)

// EnvEndpoint names the endpoint for a caller that resolves nothing.
const EnvEndpoint = "ABSTRACTION_RESOURCE_ENDPOINT"

type Client struct {
	transport listen.FrameClient
	initErr   error
}

// DefaultEndpoint returns empty when the account namespace cannot be resolved.
func DefaultEndpoint() string { endpoint, _ := checkedDefaultEndpoint(); return endpoint }
func checkedDefaultEndpoint() (string, error) {
	if endpoint := os.Getenv(EnvEndpoint); endpoint != "" {
		return endpoint, nil
	}
	return bootstrap.InstalledEndpoint("resource-table-v1")
}
func emptyEndpointError() error {
	_, err := checkedDefaultEndpoint()
	if err != nil {
		return err
	}
	return fmt.Errorf("resource: empty endpoint")
}

func Discover() *Client {
	endpoint, err := checkedDefaultEndpoint()
	c := New(endpoint)
	c.initErr = err
	return c
}

func New(endpoint string) *Client {
	c := NewWithTransport(listen.FrameClient{Endpoint: endpoint})
	if endpoint == "" {
		c.initErr = emptyEndpointError()
	}
	return c
}

// NewWithTransport retains the caller's endpoint, server trust and waiting limits.
func NewWithTransport(transport listen.FrameClient) *Client {
	c := &Client{transport: transport.WithDefaults(10*time.Second, 1<<20)}
	if transport.Endpoint == "" && transport.Dialer == nil {
		c.initErr = emptyEndpointError()
	}
	return c
}

// Resources are the resources this machine's table can report.
func (c *Client) Resources() (ResourceList, error) {
	return c.ResourcesContext(context.Background())
}

// ResourcesContext uses ctx only for this operation.
func (c *Client) ResourcesContext(ctx context.Context) (ResourceList, error) {
	if c.initErr != nil {
		return ResourceList{}, c.initErr
	}
	if err := ctx.Err(); err != nil {
		return ResourceList{}, err
	}
	reply, err := wire.NewTableClient(c.transport.WithContext(ctx)).Resources()
	if err != nil {
		return ResourceList{}, err
	}
	if err := callResultError("Resources", reply.Outcome); err != nil {
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
	if c.initErr != nil {
		return ResourceState{}, c.initErr
	}
	if err := ctx.Err(); err != nil {
		return ResourceState{}, err
	}
	reply, err := wire.NewTableClient(c.transport.WithContext(ctx)).Holders(resource, fresh)
	if err != nil {
		return ResourceState{}, err
	}
	if err := callResultError("Holders", reply.Outcome); err != nil {
		return ResourceState{}, err
	}
	return *reply.State, nil
}

// callResultError maps a generated validated reply into the compact API.
// Outcome/payload postconditions belong to the generated service client.
func callResultError(operation string, outcome wire.ResourceCallOutcome) error {
	if outcome == wire.ResourceCallOutcomeOk {
		return nil
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

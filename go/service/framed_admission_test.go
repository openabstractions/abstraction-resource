package service

import (
	"context"
	"errors"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type admissionFixtureListener struct {
	connections chan listen.Conn
	closed      chan struct{}
	once        sync.Once
}

func (l *admissionFixtureListener) Accept() (listen.Conn, error) {
	select {
	case c := <-l.connections:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *admissionFixtureListener) Close() error { l.once.Do(func() { close(l.closed) }); return nil }

type admissionFixtureConnection struct {
	reading, closed     chan struct{}
	readOnce, closeOnce sync.Once
}

func newAdmissionFixtureConnection() *admissionFixtureConnection {
	return &admissionFixtureConnection{reading: make(chan struct{}), closed: make(chan struct{})}
}
func (c *admissionFixtureConnection) Read([]byte) (int, error) {
	c.readOnce.Do(func() { close(c.reading) })
	<-c.closed
	return 0, io.ErrClosedPipe
}
func (*admissionFixtureConnection) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (c *admissionFixtureConnection) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}
func (*admissionFixtureConnection) Bind() (*identity.Binding, error) {
	return nil, errors.New("fixture handshake not complete")
}
func (*admissionFixtureConnection) SetDeadline(time.Time) error { return nil }

func TestHostFramedAdmissionSaturationFailedHandshakeAndReuse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	l := &admissionFixtureListener{connections: make(chan listen.Conn), closed: make(chan struct{})}
	errorEntered := make(chan struct{}, 1)
	releaseError := make(chan struct{})
	h := &Host{listener: l, ctx: ctx, cancel: cancel, OnError: func(error) { errorEntered <- struct{}{}; <-releaseError }}
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		close(releaseError)
		h.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("host failed to join")
		}
	})
	wait := func(ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal(what)
		}
	}
	offer := func() *admissionFixtureConnection {
		t.Helper()
		c := newAdmissionFixtureConnection()
		select {
		case l.connections <- c:
		case <-time.After(5 * time.Second):
			t.Fatal("host stopped accepting")
		}
		return c
	}
	var held []*admissionFixtureConnection
	for range 64 {
		c := offer()
		wait(c.reading, "admitted connection never reached handshake")
		held = append(held, c)
	}
	excess := offer()
	wait(excess.closed, "excess unauthenticated connection was not refused")
	held[0].Close()
	wait(errorEntered, "failed handshake never reached its handler error path")
	excess = offer()
	wait(excess.closed, "failed handshake released admission before handler returned")
	releaseError <- struct{}{}
	// The handler may still be returning: keep offering until its slot is reused.
	deadline := time.After(5 * time.Second)
	for {
		c := offer()
		select {
		case <-c.reading:
			return
		case <-c.closed:
			select {
			case <-deadline:
				t.Fatal("returned handler slot not reusable")
			default:
			}
		case <-deadline:
			t.Fatal("replacement connection was neither admitted nor refused")
		}
	}
}

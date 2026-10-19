package service

import (
	"context"
	"errors"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
)

// leaseReceiver answers abstraction.resource/leases@1 for one call. Every
// method names the caller from the binding the host rechecked; a caller whose
// program cannot be named holds nothing, because a lease is a grant to a
// program and a grant to nobody is not a grant.
type leaseReceiver struct {
	book     *Book
	call     *listen.FramedCall
	ctx      context.Context
	owner    string
	enforcer func(context.Context, *identity.Peer, string, string) bool
}

func (r *leaseReceiver) recheck() error {
	if r.call == nil {
		return &wire.ServiceError{Code: wire.ServiceErrorCodeCallerRefused, Message: "caller is not bound"}
	}
	if err := r.call.Recheck(); err != nil {
		return &wire.ServiceError{Code: wire.ServiceErrorCodeCallerRefused, Message: "caller binding no longer valid"}
	}
	return nil
}

func (r *leaseReceiver) caller() (Subject, *identity.Peer, error) {
	peer, err := r.call.Peer()
	if err != nil || peer == nil {
		return Subject{}, nil, &wire.ServiceError{Code: wire.ServiceErrorCodeCallerRefused, Message: "caller identity could not be rechecked"}
	}
	who, err := SubjectFromPeer(peer)
	if err != nil {
		return Subject{}, nil, &wire.ServiceError{Code: wire.ServiceErrorCodeCallerRefused, Message: "the caller's program could not be named"}
	}
	return who, peer, nil
}

// SubjectFromPeer names the caller as rights names a subject: an absolute
// image path or a package family, and a Windows SID or a POSIX uid. It is
// identity.SubjectOf at Bound.
func SubjectFromPeer(peer *identity.Peer) (Subject, error) {
	s, err := identity.SubjectOf(peer, Bound)
	if err != nil {
		return Subject{}, err
	}
	return Subject{Program: s.Program, Account: s.Account}, nil
}

func (r *leaseReceiver) context() context.Context {
	if r.ctx == nil {
		return context.Background()
	}
	return r.ctx
}

func (r *leaseReceiver) Acquire(resource string, amount, waitMs int64) (wire.AcquireResult, error) {
	if err := r.recheck(); err != nil {
		return wire.AcquireResult{}, err
	}
	who, peer, err := r.caller()
	if err != nil {
		return wire.AcquireResult{}, err
	}
	ctx := r.context()
	if r.call != nil {
		// Acquire waits for holders to yield. Tie that work to the caller's
		// connection so a cancelled wait does not continue to grant.
		ctx = r.call.WaitContext()
	}
	return r.book.Acquire(ctx, who, peer, resource, amount, waitMs), nil
}

// AcquireFree is the no-yield acquire (RES-L6). It asks nobody, so it
// waits for nobody either.
func (r *leaseReceiver) AcquireFree(resource string, amount int64) (wire.AcquireFreeResult, error) {
	if err := r.recheck(); err != nil {
		return wire.AcquireFreeResult{}, err
	}
	who, peer, err := r.caller()
	if err != nil {
		return wire.AcquireFreeResult{}, err
	}
	return r.book.AcquireFree(r.context(), who, peer, resource, amount), nil
}

func (r *leaseReceiver) Renew(lease string) (wire.LeaseChangeResult, error) {
	if err := r.recheck(); err != nil {
		return wire.LeaseChangeResult{}, err
	}
	who, peer, err := r.caller()
	if err != nil {
		return wire.LeaseChangeResult{}, err
	}
	change := r.book.Renew(r.maintenanceSubject(who, peer, lease), lease)
	return wire.LeaseChangeResult{Outcome: wire.ResourceCallOutcomeOk, Change: &change}, nil
}

func (r *leaseReceiver) Release(lease string) (wire.LeaseChangeResult, error) {
	if err := r.recheck(); err != nil {
		return wire.LeaseChangeResult{}, err
	}
	who, peer, err := r.caller()
	if err != nil {
		return wire.LeaseChangeResult{}, err
	}
	change := r.book.Release(r.maintenanceSubject(who, peer, lease), lease)
	return wire.LeaseChangeResult{Outcome: wire.ResourceCallOutcomeOk, Change: &change}, nil
}

func (r *leaseReceiver) BindProcess(lease string, pid int64) (wire.ProcessBindResult, error) {
	if err := r.recheck(); err != nil {
		return wire.ProcessBindResult{}, err
	}
	who, peer, err := r.caller()
	if err != nil {
		return wire.ProcessBindResult{}, err
	}
	process, err := peer.Process.AtLeast(Bound.Process)
	if err != nil {
		return wire.ProcessBindResult{Outcome: wire.ProcessBindOutcomeUnverifiable}, nil
	}
	return r.book.bindProcess(who, process, lease, pid, r.recheck), nil
}

func (r *leaseReceiver) Observe(cursor string, waitMs int64) (wire.ObserveResult, error) {
	if err := r.recheck(); err != nil {
		return wire.ObserveResult{}, err
	}
	who, _, err := r.caller()
	if err != nil {
		return wire.ObserveResult{}, err
	}
	// An observation waits on the caller's connection, not on the call's own
	// deadline: a holder that watches for thirty seconds is the contract's
	// wait, and the frame deadline is shorter than that.
	ctx := r.context()
	if r.call != nil {
		ctx = r.call.WaitContext()
	}
	page, err := r.book.observeContext(ctx, who, cursor, waitMs, r.recheck)
	if err != nil {
		if errors.Is(err, errObserveBusy) {
			return wire.ObserveResult{Outcome: wire.ResourceCallOutcomeUnavailable}, nil
		}
		if refusal, ok := err.(*wire.ServiceError); ok && refusal.Code == wire.ServiceErrorCodeInvalidRequest {
			return wire.ObserveResult{Outcome: wire.ResourceCallOutcomeInvalid}, nil
		}
		return wire.ObserveResult{}, err
	}
	return wire.ObserveResult{Outcome: wire.ResourceCallOutcomeOk, Page: &page}, nil
}

func (r *leaseReceiver) Answer(request string, answer wire.YieldAnswer, reason string) (wire.AnswerCallResult, error) {
	if err := r.recheck(); err != nil {
		return wire.AnswerCallResult{}, err
	}
	if !answer.Known() {
		return wire.AnswerCallResult{Outcome: wire.ResourceCallOutcomeInvalid}, nil
	}
	who, _, err := r.caller()
	if err != nil {
		return wire.AnswerCallResult{}, err
	}
	recorded := r.book.Answer(who, request, answer, reason)
	return wire.AnswerCallResult{Outcome: wire.ResourceCallOutcomeOk, Answer: &recorded}, nil
}

// Label names what the caller's own lease holds (Book.Label). It is
// display only and rides on the caller owning the lease, as Renew does.
func (r *leaseReceiver) Label(lease, text string) (wire.LeaseChangeResult, error) {
	if err := r.recheck(); err != nil {
		return wire.LeaseChangeResult{}, err
	}
	who, peer, err := r.caller()
	if err != nil {
		return wire.LeaseChangeResult{}, err
	}
	change := r.book.Label(r.maintenanceSubject(who, peer, lease), lease, text)
	return wire.LeaseChangeResult{Outcome: wire.ResourceCallOutcomeOk, Change: &change}, nil
}

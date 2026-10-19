package service

import (
	"context"
	"path/filepath"

	identity "github.com/openabstractions/abstraction-identity"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func (b *Book) acquireFor(ctx context.Context, who, controller Subject, process identity.Process, resource string, amount, waitMs int64) wire.AcquireResult {
	result := b.acquire(ctx, who, resource, amount, waitMs, func() error {
		if b.standing == nil {
			return ErrPolicyUnavailable
		}
		return b.standing(ctx, who, ActionHold, resource)
	})
	if result.Outcome == wire.AcquireOutcomeAcquired && result.Lease != nil {
		b.mu.Lock()
		if live := b.leases[result.Lease.ID]; live != nil {
			live.controller = &leaseController{who: controller, process: process}
		}
		b.mu.Unlock()
	}
	return result
}

func (b *Book) controlledSubject(who Subject, process identity.Process, lease string) (Subject, string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	live := b.leases[lease]
	if live == nil || live.controller == nil {
		return Subject{}, "", false
	}
	c := live.controller
	if !c.who.same(who) || c.process.PID != process.PID || !c.process.StartTime.Equal(process.StartTime) || c.process.Generation != process.Generation || c.process.Recycled || process.Recycled {
		return Subject{}, "", false
	}
	return live.subject(), live.Resource, true
}

func (r *leaseReceiver) AcquireFor(subject rights.Subject, resource string, amount, waitMs int64) (wire.AcquireResult, error) {
	refused := wire.AcquireResult{Outcome: wire.AcquireOutcomeNotPermitted, Asked: []wire.YieldRecord{}}
	if err := r.recheck(); err != nil {
		return wire.AcquireResult{}, err
	}
	caller, peer, err := r.caller()
	if err != nil {
		return wire.AcquireResult{}, err
	}
	if r.enforcer == nil || r.owner == "" || caller.Account != r.owner || subject.Account != r.owner || !r.enforcer(r.context(), peer, ActionHold, resource) {
		return refused, nil
	}
	if !identity.ValidSubjectProgram(subject.Program) {
		refused.Outcome = wire.AcquireOutcomeInvalid
		return refused, nil
	}
	if filepath.IsAbs(subject.Program) {
		subject.Program = identity.CanonicalProgramPath(filepath.Clean(subject.Program))
	}
	process, err := peer.Process.AtLeast(Bound.Process)
	if err != nil || process.Recycled {
		return refused, nil
	}
	if err := r.recheck(); err != nil {
		return wire.AcquireResult{}, err
	}
	ctx := r.call.WaitContext()
	return r.book.acquireFor(ctx, Subject{Program: subject.Program, Account: subject.Account}, caller, process, resource, amount, waitMs), nil
}

func (r *leaseReceiver) maintenanceSubject(who Subject, peer *identity.Peer, lease string) Subject {
	if r.enforcer == nil || who.Account != r.owner {
		return who
	}
	process, err := peer.Process.AtLeast(Bound.Process)
	if err != nil {
		return who
	}
	original, resource, ok := r.book.controlledSubject(who, process, lease)
	if ok && r.enforcer(r.context(), peer, ActionHold, resource) {
		return original
	}
	return who
}

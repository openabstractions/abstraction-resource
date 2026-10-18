package service

import (
	"context"
	"errors"
)

// A lease lives only while its rule does (CONTRACT.md RES-L7). Acquire
// decides ActionHold once; Standing decides it again for a lease that
// already lives, by the subject the lease names, with no call in flight and
// asking nobody. Renew decides again before it extends a lease, and Recheck
// decides again for every lease the book holds, so a revoked, blocked or
// expired rule ends the leases it kept: card leases and wake leases alike
// (RES-A1 for the platform wake request).

// Recheck decides ActionHold again for the holder of every acquired lease, on
// that lease's resource, and ends each lease whose holder no rule permits any
// more: a revoked, blocked or expired grant (RES-L7). Ending the last wake
// lease that kept the platform wake request releases that request (RES-A1),
// and the holder's next acquire is decided, and asked, as a first one is. A
// decision that could not be obtained ends nothing here; Renew does not move
// renew_by without one, so the lease ends within one lifetime all the same.
// A connection-owned hold is ended by the service that owns its connection,
// which revokes on its own policy.
//
// The host calls it on its expiry clock, which covers a grant that expires
// and an edit made by any writer; a composition also calls it when the policy
// changes (abstraction-rights RIGHTS-R23), so a revoke takes effect before its
// reply.
func (b *Book) Recheck(ctx context.Context) {
	if b.standing == nil {
		return
	}
	type holding struct {
		who      Subject
		resource string
	}
	b.mu.Lock()
	held := map[holding][]string{}
	for id, live := range b.leases {
		if !live.held {
			key := holding{who: live.subject(), resource: live.Resource}
			held[key] = append(held[key], id)
		}
	}
	b.mu.Unlock()
	for key, ids := range held {
		err := b.standing(ctx, key.who, ActionHold, key.resource)
		if err == nil || errors.Is(err, ErrPolicyUnavailable) {
			continue
		}
		for _, id := range ids {
			b.release(id, "revoked")
		}
	}
}

// standingOf decides ActionHold again on id's resource for who before Renew
// extends id, when id is a lease who acquired and the book has a Standing
// policy; a connection-owned hold, a lease of another holder or no lease at
// all needs no decision and reads nil. It decides outside the book's lock, as
// every rights decision here is made.
func (b *Book) standingOf(who Subject, id string) error {
	if b.standing == nil {
		return nil
	}
	b.mu.Lock()
	live, ok := b.leases[id]
	acquired := ok && !live.held && live.subject().same(who)
	resource := ""
	if acquired {
		resource = live.Resource
	}
	b.mu.Unlock()
	if !acquired {
		return nil
	}
	return b.standing(context.Background(), who, ActionHold, resource)
}

package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-rights/go/authorization/gate"
)

// ActionTableRead gates reading other programs' rows; ResourceAccount is the
// resource it is decided on (CONTRACT.md RES-T4).
const (
	ActionTableRead = "abstraction.resource/table.read"
	ResourceAccount = "account"
)

// actionNamespace is the namespace every resource action belongs to; the
// table and the lease book designate each Policy they install for it
// (RIGHTS-A7).
const actionNamespace = "abstraction.resource"

// tableCallBudget is how long one table call has to answer, and the margin a
// lease call keeps beyond the wait it was given. expiryInterval is how often
// the host asks the book to end the leases nobody renewed.
const (
	tableCallBudget = 10 * time.Second
	expiryInterval  = 5 * time.Second
)

// Bound is what this service requires of a caller before it will answer.
// Path is only ProofBound anywhere: Windows cannot verify the code a running
// process is executing, and Linux has no answer at all below 6.5. The table
// needs the caller's program for the own-rows rule, so a caller whose path is
// unknown is refused rather than answered with somebody else's rows.
var Bound = identity.Need{
	User:    identity.ProofKernel,
	Process: identity.ProofKernel,
	Path:    identity.ProofBound,
}

// Policy authorizes one table or lease operation for the rechecked bound
// caller. It is the rights gate: it must honor ctx and be safe for concurrent
// calls. Wrap ErrPolicyUnavailable when the decision cannot be obtained; every
// other error is a refusal, and a refused table read narrows the answer to the
// caller's own rows instead of ending it.
type Policy = gate.Gate

// ErrPolicyUnavailable distinguishes a failed decision lookup from refusal. A
// lookup that cannot answer is never a permit and never a narrowing: the
// caller is told the decision point is unavailable. A missing policy is such
// a lookup.
var ErrPolicyUnavailable = gate.ErrUnavailable

// A Host serves abstraction.resource/table@1, and the lease book beside it as
// abstraction.resource/leases@1, on one endpoint. The two are one endpoint
// because they are one answer seen twice: the table says who holds the
// resource, and the leases say who was granted it and who was asked to give it
// back.
type Host struct {
	listener  listen.Listener
	table     *Table
	ctx       context.Context
	cancel    context.CancelFunc
	once      sync.Once
	workers   sync.WaitGroup
	lifecycle sync.Mutex
	serving   bool
	policy    Policy
	book      *Book
	owner     string
	enforcer  func(context.Context, *identity.Peer, string, string) bool
	OnError   func(error)
	// OnStopped is called when admission stops. Assign it before Serve.
	OnStopped func()
}

// Listen binds the endpoint. Configure a policy before Serve; without one
// every read of the holders reads unavailable (RES-T4).
func Listen(endpoint string, table *Table) (*Host, error) {
	if table == nil {
		return nil, errors.New("resource table service: nil table")
	}
	if err := listen.CanEver(endpoint, Bound); err != nil {
		return nil, fmt.Errorf("resource table service cannot bind callers: %w", err)
	}
	l, err := listen.ListenFramed(endpoint, Bound)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Host{listener: listen.Sessions(l, listen.SessionOptions{}), table: table, ctx: ctx, cancel: cancel}, nil
}

// EnablePolicy narrows other programs' rows to callers the policy authorizes.
// Configure it before Serve. The installed policy is designated for the
// resource namespace and refuses every action outside it (RIGHTS-A7).
func (h *Host) EnablePolicy(policy Policy) error {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	if h.serving || h.ctx.Err() != nil {
		return errors.New("resource table service: configure policy before Serve")
	}
	if policy == nil {
		return errors.New("resource table service: explicit policy required")
	}
	designated, err := gate.For(actionNamespace, policy)
	if err != nil {
		return fmt.Errorf("resource table service: %w", err)
	}
	h.policy = designated
	return nil
}

// EnableLeases serves the lease book on this endpoint beside the table.
// Configure it before Serve. Without one the endpoint answers table@1 alone,
// and a caller that names leases@1 reads unknown_service.
func (h *Host) EnableLeases(book *Book) error {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	if h.serving || h.ctx.Err() != nil {
		return errors.New("resource table service: configure leases before Serve")
	}
	if book == nil {
		return errors.New("resource table service: explicit lease book required")
	}
	h.book = book
	return nil
}

// EnableEnforcer designates a native process for AcquireFor. Nil is never an
// implicit designation. Configure it before Serve, as with Rights DecideFor.
func (h *Host) EnableEnforcer(enforcer func(context.Context, *identity.Peer, string, string) bool) error {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	if h.serving || h.ctx.Err() != nil || enforcer == nil {
		return errors.New("resource: configure explicit enforcer before Serve")
	}
	me, err := identity.Self()
	if err != nil {
		return err
	}
	owner, err := me.Account()
	if err != nil {
		return err
	}
	h.owner, h.enforcer = owner, enforcer
	return nil
}

// Book is the lease book this host serves, or nil.
func (h *Host) Book() *Book {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	return h.book
}

func (h *Host) Close() error {
	var err error
	h.once.Do(func() { h.cancel(); err = h.listener.Close() })
	return err
}

func (h *Host) Serve(ctx context.Context) error {
	h.lifecycle.Lock()
	if h.serving {
		h.lifecycle.Unlock()
		return errors.New("resource table service: host already served")
	}
	h.serving = true
	policy, book, owner, enforcer := h.policy, h.book, h.owner, h.enforcer
	h.lifecycle.Unlock()
	//unchecked: Close is idempotent (sync.Once); this async cancellation callback has no caller to report the error to
	stop := context.AfterFunc(ctx, func() { _ = h.Close() })
	defer stop()
	// A lease nobody renews ends (CONTRACT.md RES-L4). The host is what
	// notices: the book is asked on a clock so an ended lease stops standing
	// between the next asker and the bytes even while nobody calls. The same
	// clock ends a lease whose grant was revoked or expired (RES-L7).
	if book != nil {
		h.workers.Add(1)
		go func() {
			defer h.workers.Done()
			ticker := time.NewTicker(expiryInterval)
			defer ticker.Stop()
			for {
				select {
				case <-h.ctx.Done():
					return
				case <-ticker.C:
					book.Expire()
					book.Recheck(h.ctx)
				}
			}
		}()
	}
	defer h.workers.Wait()
	defer func() {
		if h.OnStopped != nil {
			h.OnStopped()
		}
	}()
	defer h.Close()
	slots := listen.NewCallAdmission(64)
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			if h.ctx.Err() != nil || ctx.Err() != nil {
				return nil
			}
			return err
		}
		release, admitted := slots.Take(conn)
		if !admitted {
			continue
		}
		h.workers.Add(1)
		go func() {
			defer h.workers.Done()
			defer release()
			defer conn.Close()
			budget := tableCallBudget
			if book != nil {
				// Acquire may wait up to wait_ms for holders to yield and
				// Observe up to its own bound; a call deadline shorter than
				// the wait the contract allows would end the arbitration it
				// was waiting for.
				budget = MaxAcquireWait + tableCallBudget
			}
			requestCtx, cancel := context.WithTimeout(h.ctx, budget)
			defer cancel()
			call, err := listen.ReceiveFramed(requestCtx, conn, Bound, 1<<20)
			if call != nil {
				defer call.Close()
			}
			if err == nil {
				services := []wire.ServedService{&wire.TableDispatcher{Handler: &receiver{table: h.table, call: call, policy: policy, ctx: requestCtx}}}
				if book != nil {
					services = append(services, &wire.LeasesDispatcher{Handler: &leaseReceiver{book: book, call: call, ctx: requestCtx, owner: owner, enforcer: enforcer}})
				}
				var response []byte
				response, err = wire.ServeEndpoint(call.Frame, "openabstractions", "", services...)
				if err == nil {
					err = call.Reply(response)
				}
			}
			if err != nil && h.OnError != nil && h.ctx.Err() == nil {
				h.OnError(err)
			}
		}()
	}
}

// receiver is one call. It rechecks the binding, decides, and narrows.
type receiver struct {
	table  *Table
	call   *listen.FramedCall
	policy Policy
	ctx    context.Context
}

func (r *receiver) Resources() (wire.ResourcesResult, error) {
	if err := r.recheck(); err != nil {
		return wire.ResourcesResult{}, err
	}
	list, err := r.table.Resources()
	if err != nil {
		return wire.ResourcesResult{}, err
	}
	return wire.ResourcesResult{Outcome: wire.ResourceCallOutcomeOk, List: &list}, nil
}

// Holders answers with every row the caller may read. A caller the policy
// permits reads the machine's table; a caller it refuses reads the rows of its
// own program and nothing else, which is the rule the table exists to keep
// (CONTRACT.md RES-T4). capacity, held and observed describe the machine and
// are the same for both: they name no program.
func (r *receiver) Holders(resource string, fresh bool) (wire.HoldersResult, error) {
	if err := r.recheck(); err != nil {
		return wire.HoldersResult{}, err
	}
	if resource == "" {
		return wire.HoldersResult{Outcome: wire.ResourceCallOutcomeInvalid}, nil
	}
	narrow := false
	if err := r.authorize(ActionTableRead, ResourceAccount); err != nil {
		var refusal *wire.ServiceError
		if errors.As(err, &refusal) && refusal.Code != wire.ServiceErrorCodeForbidden {
			if refusal.Code == wire.ServiceErrorCodePolicyUnavailable {
				return wire.HoldersResult{Outcome: wire.ResourceCallOutcomeUnavailable}, nil
			}
			return wire.HoldersResult{}, err
		}
		narrow = true
	}
	state, err := r.table.Holders(resource, fresh)
	if err != nil {
		return wire.HoldersResult{}, &wire.ServiceError{Code: wire.ServiceErrorCodeInternal, Message: err.Error()}
	}
	if !narrow {
		return wire.HoldersResult{Outcome: wire.ResourceCallOutcomeOk, State: &state}, nil
	}
	program, image, err := r.programs()
	if err != nil {
		return wire.HoldersResult{}, err
	}
	state.Holders = own(state.Holders, program, image)
	return wire.HoldersResult{Outcome: wire.ResourceCallOutcomeOk, State: &state}, nil
}

// own keeps claims named for the caller's subject and instrument rows named
// for its bound image. Windows may report a short DOS path for that image.
func own(rows []wire.Holder, program, image string) []wire.Holder {
	out := []wire.Holder{}
	for _, row := range rows {
		if identity.SameSubjectProgram(row.Program, program) || (filepath.IsAbs(row.Program) &&
			identity.SameSubjectProgram(row.Program, image)) {
			out = append(out, row)
		}
	}
	return out
}

// programs returns the caller's rights subject and its bound image path. A
// packaged caller uses its package family as the subject, while an instrument
// still names its process by image path.
func (r *receiver) programs() (string, string, error) {
	peer, err := r.peer()
	if err != nil {
		return "", "", err
	}
	program, err := identity.SubjectProgram(peer, identity.ProofBound)
	if err != nil {
		return "", "", &wire.ServiceError{Code: wire.ServiceErrorCodeCallerRefused, Message: "the caller's program could not be named"}
	}
	path, err := peer.Path.AtLeast(Bound.Path)
	if err != nil {
		return "", "", &wire.ServiceError{Code: wire.ServiceErrorCodeCallerRefused, Message: "the caller's image could not be named"}
	}
	return program, identity.CanonicalProgramPath(filepath.Clean(path)), nil
}

func (r *receiver) peer() (*identity.Peer, error) {
	if r.call == nil {
		return nil, &wire.ServiceError{Code: wire.ServiceErrorCodeCallerRefused, Message: "caller is not bound"}
	}
	peer, err := r.call.Peer()
	if err != nil {
		return nil, &wire.ServiceError{Code: wire.ServiceErrorCodeCallerRefused, Message: "caller identity could not be rechecked"}
	}
	return peer, nil
}

func (r *receiver) recheck() error {
	if r.call == nil {
		return &wire.ServiceError{Code: wire.ServiceErrorCodeCallerRefused, Message: "caller is not bound"}
	}
	if err := r.call.Recheck(); err != nil {
		return &wire.ServiceError{Code: wire.ServiceErrorCodeCallerRefused, Message: "caller binding no longer valid"}
	}
	return nil
}

// authorize applies the configured policy to one operation. Without a policy
// the decision is unavailable, never a permit.
func (r *receiver) authorize(action, resource string) error {
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	peer, err := r.peer()
	if err != nil {
		return err
	}
	err = r.policy.Check(ctx, peer, action, resource)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrPolicyUnavailable) {
		return &wire.ServiceError{Code: wire.ServiceErrorCodePolicyUnavailable, Message: "resource table policy decision unavailable"}
	}
	return &wire.ServiceError{Code: wire.ServiceErrorCodeForbidden, Message: "reading other programs' rows is not permitted"}
}

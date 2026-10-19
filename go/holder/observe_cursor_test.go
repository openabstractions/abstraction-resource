package holder

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	res "github.com/openabstractions/abstraction-resource/go/client"
)

type cursorLeases struct {
	Leases         // Any lease mutation is unexpected in observation recovery.
	cursors        []string
	repeated       bool
	initialInvalid bool
	cancel         context.CancelFunc
}

func (f *cursorLeases) Observe(ctx context.Context, cursor string, _ time.Duration) (res.YieldPage, error) {
	f.cursors = append(f.cursors, cursor)
	switch len(f.cursors) {
	case 1:
		if !f.initialInvalid {
			return res.YieldPage{Next: "old-epoch:3"}, nil
		}
	case 2:
	case 3:
		if !f.repeated {
			return res.YieldPage{Next: "new-epoch:2"}, nil
		}
	default:
		f.cancel()
		return res.YieldPage{}, ctx.Err()
	}
	return res.YieldPage{}, &res.ServiceError{Code: res.ServiceErrorCodeInvalidRequest, Message: "expired cursor"}
}

func TestObserveResetsExpiredCursorOnceAndResumes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &cursorLeases{cancel: cancel}
	h := &Holder{opts: Options{Leases: f, OnError: func(err error) { t.Error(err) }}, closed: make(chan struct{})}
	h.observe(ctx)
	if want := []string{"", "old-epoch:3", "", "new-epoch:2"}; !reflect.DeepEqual(f.cursors, want) {
		t.Fatalf("observation cursors %q, want %q", f.cursors, want)
	}
}

func TestObserveStopsWhenEmptyCursorIsInvalid(t *testing.T) {
	for _, initial := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		f := &cursorLeases{cancel: cancel, repeated: true, initialInvalid: initial}
		var reported []error
		h := &Holder{opts: Options{Leases: f, OnError: func(err error) { reported = append(reported, err) }}, closed: make(chan struct{})}
		h.observe(ctx)
		cancel()
		want := []string{"", "old-epoch:3", ""}
		if initial {
			want = []string{""}
		}
		if !reflect.DeepEqual(f.cursors, want) {
			t.Fatalf("observation cursors %q, want %q", f.cursors, want)
		}
		var refusal *res.ServiceError
		if len(reported) != 1 || !errors.As(reported[0], &refusal) || refusal.Code != res.ServiceErrorCodeInvalidRequest {
			t.Fatalf("invalid observation was not surfaced: %v", reported)
		}
	}
}

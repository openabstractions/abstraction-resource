// Command resourcedriver is the conformance driver for OA's resource service
// (conformance/DRIVER.md, layer `lease`). It applies a scenario to the lease
// book and the table over a Fixed instrument, so a 24 GiB discrete card or a
// 128 GiB shared-memory pool is a line in a scenario and never a GPU run.
//
//	resourcedriver --capabilities
//	resourcedriver <workdir> <scenario-file>
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	"github.com/openabstractions/abstraction-resource/go/service"
)

const gib = int64(1) << 30

// account is the one account every scenario subject runs under.
const account = "S-1-5-21-7-1001"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--capabilities" {
		fmt.Print("lease\n")
		return
	}
	if len(os.Args) == 2 && (os.Args[1] == "-h" || os.Args[1] == "--help") {
		fmt.Print("usage: resourcedriver --capabilities | resourcedriver <workdir> <scenario-file>\n")
		return
	}
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: resourcedriver --capabilities | resourcedriver <workdir> <scenario-file>")
		os.Exit(2)
	}
	file, err := os.Open(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer file.Close()
	d, err := newDriver()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := d.run(file, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type driver struct {
	in      *instrument.Fixed
	book    *service.Book
	table   *service.Table
	set     map[string]instrument.FixedResource
	claims  []service.Claim
	permits map[string]bool
	down    bool
	caller  string
}

func newDriver() (*driver, error) {
	d := &driver{in: instrument.NewFixed(), set: map[string]instrument.FixedResource{}, permits: map[string]bool{}}
	d.table = service.NewTable(d.in, func() []service.Claim { return append([]service.Claim(nil), d.claims...) }, time.Nanosecond)
	book, err := service.OpenBook(service.BookOptions{Table: d.table, Hold: d.hold,
		Yield:    func(context.Context, string) error { return nil },
		Standing: func(context.Context, service.Subject, string, string) error { return nil },
		Settle:   50 * time.Millisecond})
	if err != nil {
		return nil, err
	}
	d.book = book
	return d, nil
}

// hold is the rights decision the scenario sets with permit and outage: the
// caller of the operation in flight is the subject decided.
func (d *driver) hold(_ context.Context, _ *identity.Peer, _, _ string) error {
	if d.down {
		return service.ErrPolicyUnavailable
	}
	if !d.permits[d.caller] {
		return fmt.Errorf("no rule for %s", d.caller)
	}
	return nil
}

func (d *driver) run(r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	n := 0
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		n++
		if _, err := fmt.Fprintf(w, "%02d %s -> %s\n", n, line, d.apply(strings.Fields(trimmed))); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (d *driver) apply(f []string) string {
	switch f[0] {
	case "measure":
		return d.measure(f[1:])
	case "sample":
		return d.sample(f[1:])
	case "engine":
		if len(f) != 4 {
			return "invalid"
		}
		amount, ok := size(f[3])
		if !ok {
			return "invalid"
		}
		// engine RES PROGRAM SIZE: a server reports SIZE as the card's
		// capacity. Its claimed row carries the report in detail (RES-T3).
		d.claims = append(d.claims, service.Claim{Resource: f[1], Program: f[2],
			Detail: "capacity " + format(amount)})
		return "ok"
	case "permit":
		if len(f) != 2 {
			return "invalid"
		}
		d.permits[f[1]] = true
		return "ok"
	case "outage":
		d.down = true
		return "ok"
	case "restore":
		d.down = false
		return "ok"
	case "acquire":
		return d.acquire(f[1:])
	case "nowait":
		if len(f) != 4 {
			return "invalid"
		}
		amount, ok := size(f[3])
		if !ok {
			return "invalid"
		}
		d.caller = f[1]
		result := d.book.AcquireFree(context.Background(), subject(f[1]), &identity.Peer{}, f[2], amount)
		return result.Outcome.String()
	case "table":
		return d.read(f[1:])
	}
	return "unknown-op"
}

// measure RES [capacity=SIZE] [pool=RES]
func (d *driver) measure(f []string) string {
	if len(f) < 1 {
		return "invalid"
	}
	r := d.set[f[0]]
	for _, kv := range f[1:] {
		key, value, _ := strings.Cut(kv, "=")
		switch key {
		case "capacity":
			amount, ok := size(value)
			if !ok {
				return "invalid"
			}
			r.Capacity = amount
		case "pool":
			r.Pool = value
		default:
			return "invalid"
		}
	}
	d.set[f[0]] = r
	d.in.Set(f[0], r)
	return "ok"
}

// sample RES PROGRAM SIZE: the instrument sees PROGRAM hold SIZE. A program
// written image:<name> is a process the platform would not attribute.
func (d *driver) sample(f []string) string {
	if len(f) != 3 {
		return "invalid"
	}
	amount, ok := size(f[2])
	if !ok {
		return "invalid"
	}
	s := instrument.Sample{PID: 100 + len(d.set[f[0]].Samples), Amount: amount}
	if image, found := strings.CutPrefix(f[1], "image:"); found {
		s.Image = image
	} else {
		s.Program, s.Account = f[1], account
	}
	r := d.set[f[0]]
	r.Samples = append(r.Samples, s)
	d.set[f[0]] = r
	d.in.Set(f[0], r)
	return "ok"
}

// acquire PROGRAM RES SIZE [wait=MS]
func (d *driver) acquire(f []string) string {
	if len(f) < 3 || len(f) > 4 {
		return "invalid"
	}
	amount, ok := size(f[2])
	if !ok {
		return "invalid"
	}
	wait := int64(0)
	if len(f) == 4 {
		value, found := strings.CutPrefix(f[3], "wait=")
		n, err := strconv.ParseInt(value, 10, 64)
		if !found || err != nil {
			return "invalid"
		}
		wait = n
	}
	d.caller = f[0]
	result := d.book.Acquire(context.Background(), subject(f[0]), &identity.Peer{}, f[1], amount, wait)
	asked := []string{}
	for _, a := range result.Asked {
		answer, _, _ := strings.Cut(a.Answer, ":")
		asked = append(asked, a.Holder+":"+answer)
	}
	if len(asked) == 0 {
		asked = append(asked, "none")
	}
	return result.Outcome.String() + " asked=" + strings.Join(asked, ",")
}

// table RES: capacity, held and each row as program/account/evidence/amount,
// sorted.
func (d *driver) read(f []string) string {
	if len(f) != 1 {
		return "invalid"
	}
	state, err := d.table.Holders(f[0], true)
	if err != nil {
		return "unavailable"
	}
	rows := []string{}
	for _, h := range state.Holders {
		evidence := "claimed"
		if h.Evidence == wire.EvidenceVerified {
			evidence = "verified"
		}
		who := h.Account
		if who == "" {
			who = "-"
		}
		rows = append(rows, h.Program+"/"+who+"/"+evidence+"/"+format(h.Amount))
	}
	sort.Strings(rows)
	if len(rows) == 0 {
		rows = append(rows, "none")
	}
	return "ok capacity=" + format(state.Capacity) + " held=" + format(state.Held) + " rows=" + strings.Join(rows, ",")
}

func subject(program string) service.Subject {
	return service.Subject{Program: program, Account: account}
}

// size reads 24GiB, 512MiB or a byte count.
func size(s string) (int64, bool) {
	unit := int64(1)
	switch {
	case strings.HasSuffix(s, "GiB"):
		unit, s = gib, strings.TrimSuffix(s, "GiB")
	case strings.HasSuffix(s, "MiB"):
		unit, s = 1<<20, strings.TrimSuffix(s, "MiB")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || n > (1<<62)/unit {
		return 0, false
	}
	return n * unit, true
}

func format(n int64) string {
	switch {
	case n != 0 && n%gib == 0:
		return strconv.FormatInt(n/gib, 10) + "GiB"
	case n != 0 && n%(1<<20) == 0:
		return strconv.FormatInt(n>>20, 10) + "MiB"
	}
	return strconv.FormatInt(n, 10)
}

package gpufault

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"runtime"
)

// QuarantineFile is the quarantine's file in a provider's state folder.
const QuarantineFile = "gpu-quarantine.json"

// ProviderOptions is one local-engine provider's GPU fault handling: its
// program name, its state folder, its wake lease, its OA logging logger and
// its diagnostic stream.
type ProviderOptions struct {
	// Name is the provider's program name; it starts each stderr line
	// ("modelhostd: ...").
	Name string
	// State is the provider's state folder, which keeps QuarantineFile; ""
	// keeps the quarantine in memory.
	State string
	// Records is the platform's records; nil reads PlatformRecords on
	// Windows and none elsewhere.
	Records Records
	// Wake takes the wake lease while GPU work runs; nil takes none.
	Wake WakeFunc
	// Logger receives one warning record per fault and per fault-handling
	// error (RES-G6). The provider passes its OA logging logger, so the
	// resource kit carries no logging dependency. Nil writes no record.
	Logger *slog.Logger
	// Stderr receives each fault's Panel line; nil writes none.
	Stderr io.Writer
	// Report receives each fault-handling error; it may be nil.
	Report func(error)
}

// ForProvider is the Guard every local-engine provider composes (VISION
// 2026-10-01, RES-G1..RES-G7): the platform's records, the quarantine in the
// provider's state, one log record per fault and per handling error, and the
// fault's Panel line on stderr. The provider attaches its holders, asks the
// Guard before each load and runs it.
func ForProvider(o ProviderOptions) (*Guard, error) {
	source := o.Records
	if source == nil && runtime.GOOS == "windows" {
		source = PlatformRecords()
	}
	state := ""
	if o.State != "" {
		state = filepath.Join(o.State, QuarantineFile)
	}
	return New(Options{
		Records: source,
		State:   state,
		Wake:    o.Wake,
		// Each GPU fault handling error is one OA logging record too: a
		// damaged quarantine moved aside, a source gone blind (RES-G3).
		OnError: func(err error) {
			if o.Logger != nil {
				o.Logger.LogAttrs(context.Background(), slog.LevelWarn, "gpu fault handling", slog.String("error", err.Error()))
			}
			if o.Report != nil {
				o.Report(err)
			}
		},
		OnFault: func(f Fault) {
			f.Log(o.Logger)
			if f.Line != "" && o.Stderr != nil {
				//unchecked: a diagnostic line; the fault's record already went to the log
				fmt.Fprintf(o.Stderr, "%s: %s\n", o.Name, f.Line)
			}
		},
	})
}

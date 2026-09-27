package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// An auditRecord is one line of the lease audit: an ask, a yield, a refusal,
// or the lease that followed. Every ask carries who asked, who was asked, the
// resource, the bytes and the time it took (CONTRACT.md RES-L2).
//
// Amount is what the ask actually freed, which the instrument measured;
// Requested is what the holder had been granted or what the asker asked for.
// A record with an empty Holder is about the asker alone.
type auditRecord struct {
	At        string `json:"at"`
	Event     string `json:"event"`
	Asker     string `json:"asker,omitempty"`
	Holder    string `json:"holder,omitempty"`
	Lease     string `json:"lease,omitempty"`
	Resource  string `json:"resource,omitempty"`
	Amount    int64  `json:"amount"`
	Requested int64  `json:"requested,omitempty"`
	Answer    string `json:"answer,omitempty"`
	TookMS    int64  `json:"took_ms"`
}

// MaxAudit is the size at which the audit rolls over to one previous file. The
// record is evidence of arbitration, not a log of traffic: an ask is a rare
// event, and a megabyte of them is thousands.
const MaxAudit = 1 << 20

// An Audit is the append-only record of every ask, yield and refusal. It is a
// file of one JSON document per line, which a reader tails and a person reads.
type Audit struct {
	path string
	mu   sync.Mutex
	file *os.File
	size int64
	// OnError reports a write that did not land. An audit that cannot be
	// written is reported; it never ends the arbitration it was recording.
	OnError func(error)
}

// OpenAudit opens the record at path, creating its directory.
func OpenAudit(path string) (*Audit, error) {
	if path == "" {
		return nil, errors.New("resource audit: a path is required")
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("resource audit: absolute path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("resource audit: %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		//unchecked: best-effort cleanup on a path that already returns a definite error
		file.Close()
		return nil, err
	}
	return &Audit{path: path, file: file, size: info.Size()}, nil
}

func (a *Audit) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return nil
	}
	err := a.file.Close()
	a.file = nil
	return err
}

// Write appends one record.
func (a *Audit) Write(r auditRecord) {
	if a == nil {
		return
	}
	line, err := json.Marshal(r)
	if err != nil {
		a.report(err)
		return
	}
	line = append(line, '\n')
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return
	}
	if a.size+int64(len(line)) > MaxAudit {
		a.roll()
	}
	n, err := a.file.Write(line)
	a.size += int64(n)
	if err != nil {
		a.report(err)
	}
}

// roll moves the full record aside so the newest asks are always readable. The
// caller holds the lock.
func (a *Audit) roll() {
	if err := a.file.Close(); err != nil {
		a.report(err)
	}
	a.file = nil
	if err := os.Rename(a.path, a.path+".1"); err != nil {
		a.report(err)
	}
	file, err := os.OpenFile(a.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		a.report(err)
		return
	}
	a.file, a.size = file, 0
}

func (a *Audit) report(err error) {
	if a.OnError != nil {
		a.OnError(err)
	}
}

// ReadAudit is the last limit records of the audit at path, oldest first. A
// line that does not parse is left out rather than ending the read: the record
// is evidence, and one damaged line is not a reason to withhold the rest.
func ReadAudit(path string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 64
	}
	var lines []string
	for _, name := range []string{path + ".1", path} {
		data, err := os.ReadFile(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var check auditRecord
			if json.Unmarshal([]byte(line), &check) != nil {
				continue
			}
			lines = append(lines, line)
		}
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines, nil
}

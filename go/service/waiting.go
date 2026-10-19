package service

import (
	"strings"
	"unicode/utf8"

	"github.com/openabstractions/abstraction-identity"
)

// WaitingHeldBy begins the waiting word resource registers in job's
// waiting-word catalogue for an acquire that waits on a holder's yield: the
// family card, the word held_by, then the holder's program (JOB-A15, RES-L8).
// Resource owns the word; job prints it.
const WaitingHeldBy = "card:held_by:"

// maxWaitingWord is JOB-A15's bound on a waiting word.
const maxWaitingWord = 64

// publisherIDLength is the length of the publisher id that ends a package
// family name, <Name>_<PublisherId> (identity.PackageFamily).
const publisherIDLength = 13

// heldBy names the holder's program in the waiting word (RES-L8). The table
// names a program by its image path or its package family:
//
//   - an image path reads as its base name, without the extension only when
//     that is Windows' .exe, so /usr/bin/python3.12 stays python3.12;
//   - a package family, with or without identity's msix: prefix, reads as its
//     package name, without the publisher id.
//
// The name is lowercased, every character JOB-A15 does not allow, ':'
// included, is written '_', and it is cut so the word stays within 64 bytes.
func heldBy(program string) string {
	name := program
	switch {
	case strings.ContainsAny(name, `/\`):
		name = name[strings.LastIndexAny(name, `/\`)+1:]
		if len(name) > len(".exe") && strings.EqualFold(name[len(name)-len(".exe"):], ".exe") {
			name = name[:len(name)-len(".exe")]
		}
	default:
		name = strings.TrimPrefix(name, identity.PackagedProgramPrefix)
		if i := strings.LastIndexByte(name, '_'); i > 0 && len(name)-i-1 == publisherIDLength {
			name = name[:i]
		}
	}
	var b strings.Builder
	for len(name) > 0 {
		r, size := utf8.DecodeRuneInString(name)
		name = name[size:]
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	word := b.String()
	if word == "" {
		word = "unknown"
	}
	if room := maxWaitingWord - len(WaitingHeldBy); len(word) > room {
		word = word[:room]
	}
	return WaitingHeldBy + word
}

// A waitingWord is the word shared by every Acquire of one asker on one pool
// that is in its ask loop, and how many such Acquires there are: the word
// stays while any of them still waits.
type waitingWord struct {
	word    string
	waiters int
}

// waitingKey names one asker's wait on one pool.
func waitingKey(who Subject, pool string) string {
	return who.Program + "\x00" + who.Account + "\x00" + pool
}

// Waiting is the waiting word of who's acquire of resource while it waits on
// a holder's yield: card:held_by:<program> naming the holder asked last. It
// is empty when no acquire of who waits on that resource's pool. The word is
// set when a yield request goes out and removed when the last waiting
// acquire of who on that pool returns, so a release clears it with the grant
// that follows, with no timer. Only card resources carry the word (JOB-A15,
// RES-L8).
func (b *Book) Waiting(who Subject, resource string) string {
	key := waitingKey(who, b.table.poolOf(resource))
	b.mu.Lock()
	defer b.mu.Unlock()
	if w := b.words[key]; w != nil {
		return w.word
	}
	return ""
}

// enterWaitingLocked counts one more Acquire of who on resource's pool in
// its ask loop. The caller holds the lock.
func (b *Book) enterWaitingLocked(who Subject, resource string) {
	key := waitingKey(who, b.table.poolOf(resource))
	if b.words == nil {
		b.words = map[string]*waitingWord{}
	}
	w := b.words[key]
	if w == nil {
		w = &waitingWord{}
		b.words[key] = w
	}
	w.waiters++
}

// leaveWaitingLocked counts one Acquire of who on resource's pool out of its
// ask loop and removes the word with the last one. The caller holds the
// lock.
func (b *Book) leaveWaitingLocked(who Subject, resource string) {
	key := waitingKey(who, b.table.poolOf(resource))
	w := b.words[key]
	if w == nil {
		return
	}
	if w.waiters--; w.waiters <= 0 {
		delete(b.words, key)
	}
}

// noteWaiting records that who's acquire of resource now waits on program.
func (b *Book) noteWaiting(who Subject, resource, program string) {
	if !strings.HasPrefix(resource, cardPrefix) {
		return
	}
	word := heldBy(program)
	key := waitingKey(who, b.table.poolOf(resource))
	b.mu.Lock()
	if w := b.words[key]; w != nil {
		w.word = word
	}
	b.mu.Unlock()
}

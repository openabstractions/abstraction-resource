package service

import (
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
)

func TestSubjectFromPeerRequiresPeerAndProgramProof(t *testing.T) {
	for name, peer := range map[string]*identity.Peer{
		"nil peer": nil,
		"missing proof": {},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := SubjectFromPeer(peer); err == nil {
				t.Fatal("accepted peer without required subject evidence")
			}
		})
	}
}

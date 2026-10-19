package main

import (
	"github.com/openabstractions/abstraction-resource/go/internal/personfolders"
	"os"
	"strings"
	"testing"
)

// The transcript of the shared-pool acceptance: the second 100 GiB lease on a
// 128 GiB APU is refused after its holder was asked.
func TestSharedPoolTranscript(t *testing.T) {
	d, err := newDriver()
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	scenario := "measure memory capacity=128GiB\nmeasure card:0 pool=memory\npermit a\npermit b\n" +
		"acquire a card:0 100GiB\nacquire b memory 100GiB wait=100\nacquire b card:0 200GiB\n"
	if err := d.run(strings.NewReader(scenario), &out); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"05 acquire a card:0 100GiB -> acquired asked=none",
		"06 acquire b memory 100GiB wait=100 -> holders_refused asked=a:unanswered",
		"07 acquire b card:0 200GiB -> insufficient asked=none",
	}
	got := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")[4:]
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("transcript\n%s", out.String())
	}
}

// TestMain confines per-user folders to the canonical owned test fixture.
func TestMain(m *testing.M) { os.Exit(personfolders.Main(m)) }

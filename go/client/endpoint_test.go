package client

import (
	"context"
	"errors"
	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	"github.com/openabstractions/abstraction-resource/go/internal/personfolders"
	"net"
	"os"
	"runtime"
	"testing"
)

func TestDefaultEndpointRefusesAmbientAndRetainsExplicitChoice(t *testing.T) {
	t.Setenv("OA_TEST_ALLOW_INSTALLED_RUNTIME", "")
	t.Setenv(EnvEndpoint, "")
	if endpoint, err := checkedDefaultEndpoint(); endpoint != "" || !errors.Is(err, bootstrap.ErrNoTrustedInstallation) {
		t.Fatalf("ambient endpoint: %q %v", endpoint, err)
	}
	if got := DefaultEndpoint(); got != "" {
		t.Fatal("ambient address escaped refusal", got)
	}
	t.Setenv(EnvEndpoint, "explicit-owned-endpoint")
	if got := DefaultEndpoint(); got != "explicit-owned-endpoint" {
		t.Fatal(got)
	}
}
func TestDiscoverRetainsDefaultRefusalBeforeIO(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("Unix socket default refusal; Windows SID and installed Mac naming have separate controls")
	}
	t.Setenv(EnvEndpoint, "")
	base := t.TempDir()
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", base)
	if DefaultEndpoint() != "" {
		t.Fatal("refused namespace returned a fallback")
	}
	c := Discover()
	called := false
	c.transport.Dialer = func(context.Context, string) (net.Conn, error) {
		called = true
		return nil, errors.New("unexpected dial")
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := c.Resources()
	if err == nil || called {
		t.Fatalf("lost resolution error or dialed: %v %v", err, called)
	}
}

func TestLeaseClientRetainsDefaultRefusal(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("Unix namespace receiving")
	}
	t.Setenv(EnvEndpoint, "")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", dir)
	c := Discover()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AcquireFree("awake", 1); !errors.Is(err, bootstrap.ErrNoTrustedInstallation) {
		t.Fatal("lease client lost default refusal")
	}
}

// TestMain confines per-user folders to the canonical owned test fixture.
func TestMain(m *testing.M) { os.Exit(personfolders.Main(m)) }

package chatclient

import (
	"os"
	"testing"

	"github.com/kingfs/Trajecta/internal/testnet"
)

// TestMain installs the outbound network guard: this package's production code
// falls back to http.DefaultClient when no client is injected, so a test that
// forgets to inject one must fail instead of reaching a real provider.
func TestMain(m *testing.M) {
	testnet.InstallLoopbackGuard()
	os.Exit(m.Run())
}

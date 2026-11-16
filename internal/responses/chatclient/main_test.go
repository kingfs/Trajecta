package chatclient

import (
	"os"
	"testing"

	"github.com/kingfs/Trajecta/internal/testnet"
)

// TestMain installs the outbound network guard: this package only aggregates
// streams and formats errors today, but any future outbound call added here
// must fail in tests instead of reaching a real provider.
func TestMain(m *testing.M) {
	testnet.InstallLoopbackGuard()
	os.Exit(m.Run())
}

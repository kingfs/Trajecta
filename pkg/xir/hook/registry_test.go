package hook_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

func hasPanicMessage(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		recovered := recover()
		if recovered == nil {
			t.Fatalf("expected panic containing %q, got none", want)
		}
		if got := fmt.Sprint(recovered); !strings.Contains(got, want) {
			t.Fatalf("panic %q does not contain %q", got, want)
		}
	}()
	fn()
}

func TestRegistryLookupAndIDs(t *testing.T) {
	t.Parallel()
	reg := &hook.Registry{}
	reg.Register("b", func(hook.Config) (hook.Hook, error) { return nil, errors.New("unused") })
	reg.Register("a", func(hook.Config) (hook.Hook, error) { return nil, errors.New("unused") })

	if _, ok := reg.Lookup("a"); !ok {
		t.Fatal("Lookup(a) missed a registered factory")
	}
	if _, ok := reg.Lookup("missing"); ok {
		t.Fatal("Lookup(missing) found an unregistered factory")
	}
	if got, want := reg.IDs(), []string{"a", "b"}; !equalStrings(got, want) {
		t.Fatalf("IDs() = %v, want %v", got, want)
	}

	// IDs returns a fresh copy.
	ids := reg.IDs()
	ids[0] = "mutated"
	if got := reg.IDs(); got[0] != "a" {
		t.Fatalf("IDs() shares its backing slice: %v", got)
	}
}

func TestRegistryNilIsSafeForReads(t *testing.T) {
	t.Parallel()
	var reg *hook.Registry
	if _, ok := reg.Lookup("x"); ok {
		t.Fatal("nil Registry Lookup reported a factory")
	}
	if ids := reg.IDs(); ids != nil {
		t.Fatalf("nil Registry IDs() = %v, want nil", ids)
	}
}

func TestRegistryDuplicateRegistrationPanics(t *testing.T) {
	t.Parallel()
	reg := &hook.Registry{}
	factory := func(hook.Config) (hook.Hook, error) { return nil, errors.New("unused") }
	reg.Register("dup", factory)

	hasPanicMessage(t, "duplicate", func() { reg.Register("dup", factory) })
}

func TestRegistryInvalidRegistrationPanics(t *testing.T) {
	t.Parallel()
	t.Run("empty id", func(t *testing.T) {
		hasPanicMessage(t, "empty ID", func() {
			(&hook.Registry{}).Register("", func(hook.Config) (hook.Hook, error) { return nil, nil })
		})
	})
	t.Run("nil factory", func(t *testing.T) {
		hasPanicMessage(t, "nil factory", func() { (&hook.Registry{}).Register("x", nil) })
	})
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

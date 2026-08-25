package nodeidentity

import (
	"errors"
	"testing"
)

// isolateLocks redirects the lock root to a per-test temp dir: on darwin
// os.UserConfigDir() ignores XDG_CONFIG_HOME, so the env var alone would leak
// test locks into the real user directory (and fail under sandboxed test
// environments that cannot write it).
func isolateLocks(t *testing.T) {
	t.Helper()
	old := lockRootOverride
	lockRootOverride = t.TempDir()
	t.Cleanup(func() { lockRootOverride = old })
}

func TestAcquireRejectsDuplicateAndAllowsDifferentKind(t *testing.T) {
	isolateLocks(t)
	a, err := Acquire(KindPhysical, "host-a")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer a.Release()
	if _, err := Acquire(KindPhysical, "host-a"); err == nil {
		t.Fatal("expected duplicate lock rejection")
	} else if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("duplicate error = %v, want ErrAlreadyRunning", err)
	}
	b, err := Acquire(KindVM, "host-a")
	if err != nil {
		t.Fatalf("vm should coexist with physical: %v", err)
	}
	defer b.Release()
}

func TestAcquireRejectsUnknownKind(t *testing.T) {
	isolateLocks(t)
	if _, err := Acquire("container", "host-c"); err == nil {
		t.Fatal("expected unsupported kind rejection")
	}
}

func TestReleaseAllowsReacquire(t *testing.T) {
	isolateLocks(t)
	a, err := Acquire(KindPhysical, "host-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	b, err := Acquire(KindPhysical, "host-b")
	if err != nil {
		t.Fatal(err)
	}
	_ = b.Release()
}

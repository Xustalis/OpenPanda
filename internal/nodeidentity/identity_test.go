package nodeidentity

import (
	"errors"
	"testing"
)

func TestAcquireRejectsDuplicateAndAllowsDifferentKind(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
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
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := Acquire("container", "host-c"); err == nil {
		t.Fatal("expected unsupported kind rejection")
	}
}

func TestReleaseAllowsReacquire(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
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

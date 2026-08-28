package live

import (
	"testing"
	"time"
)

func TestSubscribeNotifyDoesNotLoseSignal(t *testing.T) {
	t.Parallel()

	reg := New()
	w := reg.Subscribe("alice")
	reg.Notify("alice")
	select {
	case <-w.C():
	case <-time.After(time.Second):
		t.Fatal("signal lost: Notify before receive must be delivered")
	}
}

func TestNotifyDoesNotBlockWhenUnread(t *testing.T) {
	t.Parallel()

	reg := New()
	_ = reg.Subscribe("alice")
	done := make(chan struct{})
	go func() {
		reg.Notify("alice")
		reg.Notify("alice")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second Notify blocked")
	}
}

func TestNotifyIsolatesUsers(t *testing.T) {
	t.Parallel()

	reg := New()
	wb := reg.Subscribe("bob")
	reg.Notify("alice")
	select {
	case <-wb.C():
		t.Fatal("bob woken by Notify(alice)")
	default:
	}
}

func TestUnsubscribeEmptiesRegistry(t *testing.T) {
	t.Parallel()

	reg := New()
	w := reg.Subscribe("alice")
	reg.Unsubscribe(w)
	if reg.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", reg.Len())
	}
	reg.Unsubscribe(w)
	if reg.Len() != 0 {
		t.Fatalf("Len() after second Unsubscribe = %d, want 0", reg.Len())
	}
}

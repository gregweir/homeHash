package alerts

import (
	"context"
	"errors"
	"testing"
)

func TestMultiNotifiesEachNotifier(t *testing.T) {
	a, b := &recorder{}, &recorder{}
	m := NewMulti(a, b)
	if err := m.Notify(context.Background(), Alert{Message: "x"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(a.all()) != 1 || len(b.all()) != 1 {
		t.Fatalf("a=%d b=%d, want 1 each", len(a.all()), len(b.all()))
	}
}

func TestMultiCombinesErrors(t *testing.T) {
	m := NewMulti(&failingNotifier{}, LogNotifier{})
	err := m.Notify(context.Background(), Alert{Message: "x"})
	if err == nil || !errors.Is(err, errTest) {
		t.Fatalf("err = %v, want combined test error", err)
	}
}

type failingNotifier struct{}

func (failingNotifier) Notify(context.Context, Alert) error { return errTest }

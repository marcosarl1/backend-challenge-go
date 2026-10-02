package application

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	if got := backoff(0); got != time.Second {
		t.Fatalf("base = %v", got)
	}
	if got := backoff(1); got != 2*time.Second {
		t.Fatalf("dobro = %v", got)
	}
	if got := backoff(20); got != pendingMaxDelay {
		t.Fatalf("teto = %v", got)
	}
}

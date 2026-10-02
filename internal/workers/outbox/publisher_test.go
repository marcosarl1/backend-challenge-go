package outbox

import (
	"testing"
	"time"
)

func TestBackoffBounds(t *testing.T) {
	base, max := time.Second, time.Minute
	for attempts := range 12 {
		for range 50 {
			got := backoffWithJitter(base, max, attempts)
			ceiling := base << attempts
			if ceiling <= 0 || ceiling > max {
				ceiling = max
			}
			if got < ceiling/2 || got > ceiling {
				t.Fatalf("tentativa %d: %v fora de [%v, %v]", attempts, got, ceiling/2, ceiling)
			}
		}
	}
}

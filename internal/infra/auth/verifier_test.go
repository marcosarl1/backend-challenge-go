package auth

import (
	"errors"
	"testing"
	"time"
)

func TestCheckExpiry(t *testing.T) {
	now := time.Now()
	leeway := 30 * time.Second
	if err := checkExpiry(now.Add(time.Hour), now, leeway); err != nil {
		t.Fatalf("futuro = %v", err)
	}
	// Vencido há menos que a tolerância: ainda vale.
	if err := checkExpiry(now.Add(-29*time.Second), now, leeway); err != nil {
		t.Fatalf("dentro da tolerância = %v", err)
	}
	// Vencido além da tolerância: vencido mesmo.
	if err := checkExpiry(now.Add(-31*time.Second), now, leeway); !errors.Is(err, ErrExpired) {
		t.Fatalf("fora da tolerância = %v", err)
	}
}

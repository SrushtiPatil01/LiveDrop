package id

import (
	"strings"
	"testing"
)

func TestNewReturnsUniqueUUIDs(t *testing.T) {
	first, err := New()
	if err != nil {
		t.Fatal(err)
	}
	second, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("generated duplicate IDs")
	}
	if len(first) != 36 || strings.Count(first, "-") != 4 {
		t.Fatalf("generated invalid UUID: %q", first)
	}
}

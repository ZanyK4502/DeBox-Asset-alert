package app

import "testing"

func TestName(t *testing.T) {
	if Name != "DeBox Asset Alert" {
		t.Fatalf("unexpected application name: %q", Name)
	}
}

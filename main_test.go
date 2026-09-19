package main

import "testing"

// A plain `go build` must still report something; CI and `make image` stamp the
// real version in with -ldflags.
func TestVersionHasADefault(t *testing.T) {
	if version == "" {
		t.Fatal("version must default to a non-empty value (\"dev\")")
	}
}

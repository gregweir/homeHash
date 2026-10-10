package main

import (
	"bytes"
	"testing"
)

func TestParseArgsVersionFlag(t *testing.T) {
	var out bytes.Buffer
	show, err := parseArgs([]string{"--version"}, &out)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if !show {
		t.Fatal("parseArgs(--version) = false, want true")
	}
	if out.Len() != 0 {
		t.Fatalf("parseArgs wrote to stdout, want silent (main prints): %q", out.String())
	}
}

func TestParseArgsPlainStart(t *testing.T) {
	var out bytes.Buffer
	show, err := parseArgs([]string{}, &out)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if show {
		t.Fatal("parseArgs without flag = true, want false")
	}
}

func TestVersionNotEmpty(t *testing.T) {
	// CI injects the real tag with -ldflags -X; a plain build falls back to
	// "dev". Either way the string must never be empty.
	if version == "" {
		t.Fatal("version is empty")
	}
}

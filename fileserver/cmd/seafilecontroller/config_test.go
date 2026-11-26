//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoConfigCheckRequiresPackagedBinaries(t *testing.T) {
	bin := t.TempDir()
	other := t.TempDir()
	t.Setenv("PATH", other)
	logPath := filepath.Join(t.TempDir(), "commands")
	script := []byte("#!/bin/sh\nprintf '%s\\n' \"$0|$*\" >> \"$CHECK_LOG\"\n")
	for _, path := range []string{
		filepath.Join(bin, "seaf-server-go"),
		filepath.Join(bin, "fileserver"),
		filepath.Join(other, "fileserver"),
	} {
		if err := os.WriteFile(path, script, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c := config{
		options: options{central: "config", ccnet: "ccnet", data: "data"},
		bin:     bin,
		useGo:   true,
		env:     []string{"CHECK_LOG=" + logPath},
	}
	if err := testServerConfig(c); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	const args = "|-F config -c ccnet -d data -t\n"
	want := filepath.Join(bin, "seaf-server-go") + args + filepath.Join(bin, "fileserver") + args
	if string(log) != want {
		t.Fatalf("checked %q, want %q", log, want)
	}
	if err := os.Remove(filepath.Join(bin, "fileserver")); err != nil {
		t.Fatal(err)
	}
	if err := testServerConfig(c); err == nil || !strings.Contains(err.Error(), "check fileserver configuration") {
		t.Fatalf("missing packaged fileserver returned %v", err)
	}
}

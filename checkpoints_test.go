package main

import (
	"os"
	"strings"
	"testing"

	"github.com/yosukeno/pi-go/web"
)

// The store is addressed by a hash of the working directory string, so a command
// that forwarded -C's empty default unresolved would look under sha256("") and
// then report — accurately, for that path — that there is nothing to prune. The
// bug is invisible in the output, which is why it gets a test.
func TestCheckpointCommandResolvesTheDefaultWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("PIGO_SESSION_DIR", t.TempDir())

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := checkpointCommand(&out, ""); err != nil {
		t.Fatal(err)
	}
	want := web.CheckpointDir(os.Getenv("PIGO_SESSION_DIR"), wd)
	if !strings.Contains(out.String(), want) {
		t.Errorf("output = %q, want the store for the resolved cwd (%s)", out.String(), want)
	}
	// The specific wrong answer: the key for the empty string.
	if bad := web.CheckpointDir(os.Getenv("PIGO_SESSION_DIR"), ""); strings.Contains(out.String(), bad) {
		t.Errorf("output names the empty-string workspace key %q", bad)
	}
}

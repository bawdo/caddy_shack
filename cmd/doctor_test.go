package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestDoctorYesWithoutFixIsError(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"doctor", "--yes"})
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected --yes without --fix to error")
	}
	if !strings.Contains(err.Error(), "--yes requires --fix") {
		t.Errorf("error message: %v", err)
	}
}

func TestDoctorJSONWithFixIsError(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"doctor", "--json", "--fix"})
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected --json --fix to error")
	}
	if !strings.Contains(err.Error(), "--json cannot be combined with --fix") {
		t.Errorf("error message: %v", err)
	}
}

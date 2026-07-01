package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteCAEmitsOnlyPEM(t *testing.T) {
	pem := "-----BEGIN CERTIFICATE-----\nROOT\n-----END CERTIFICATE-----\n"
	var buf bytes.Buffer
	writeCA(&buf, pem)

	out := buf.String()
	if out != pem {
		t.Errorf("stdout must be the PEM verbatim (no extra output)\ngot:\n%q\nwant:\n%q", out, pem)
	}
	if !strings.HasPrefix(out, "-----BEGIN CERTIFICATE-----") {
		t.Errorf("missing PEM header: %q", out)
	}
	if !strings.Contains(out, "-----END CERTIFICATE-----") {
		t.Errorf("missing PEM footer: %q", out)
	}
}

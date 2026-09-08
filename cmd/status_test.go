package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/app"
)

func TestRenderStatusHumanHealthy(t *testing.T) {
	rep := app.StatusReport{
		ContainerState: "running",
		AdminReachable: true,
		BackendCount:   2,
		Healthy:        true,
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, false)
	out := buf.String()
	for _, want := range []string{
		"container:       running",
		"admin reachable: true",
		"backends:        2",
		"healthy:         true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestRenderStatusHumanShowsVersionWhenKnown(t *testing.T) {
	rep := app.StatusReport{
		ContainerState: "running",
		AdminReachable: true,
		BackendCount:   1,
		Healthy:        true,
		CaddyVersion:   "v2.8.4",
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, false)
	if got := buf.String(); !strings.Contains(got, "version:         v2.8.4") {
		t.Errorf("missing version line\nfull:\n%s", got)
	}
}

func TestRenderStatusHumanOmitsVersionWhenUnknown(t *testing.T) {
	rep := app.StatusReport{
		ContainerState: "stopped",
		AdminReachable: false,
		Healthy:        false,
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, false)
	if got := buf.String(); strings.Contains(got, "version:") {
		t.Errorf("version line should be absent when unknown\nfull:\n%s", got)
	}
}

func TestRenderStatusHumanUnhealthy(t *testing.T) {
	rep := app.StatusReport{
		ContainerState: "stopped",
		AdminReachable: false,
		BackendCount:   0,
		Healthy:        false,
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, false)
	out := buf.String()
	for _, want := range []string{
		"container:       stopped",
		"admin reachable: false",
		"healthy:         false",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestRenderStatusHumanOmitsCertLinesWhenNoneStored(t *testing.T) {
	rep := app.StatusReport{
		ContainerState: "running",
		AdminReachable: true,
		BackendCount:   1,
		Healthy:        true,
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, false)
	out := buf.String()
	if strings.Contains(out, "certificates:") || strings.Contains(out, "soonest expiry:") {
		t.Errorf("cert lines should be absent when there are no certificates\nfull:\n%s", out)
	}
}

func TestRenderStatusHumanShowsCertCountAndSoonestExpiry(t *testing.T) {
	days := 12
	rep := app.StatusReport{
		ContainerState:        "running",
		AdminReachable:        true,
		BackendCount:          1,
		Healthy:               true,
		CertCount:             3,
		SoonestCertExpiryDays: &days,
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, false)
	out := buf.String()
	for _, want := range []string{
		"certificates:    3",
		"soonest expiry:  12 days",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestRenderStatusHumanShowsExpiredCertificateInPlainLanguage(t *testing.T) {
	days := -3
	rep := app.StatusReport{
		ContainerState:        "running",
		AdminReachable:        true,
		BackendCount:          1,
		Healthy:               true,
		CertCount:             1,
		SoonestCertExpiryDays: &days,
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, false)
	out := buf.String()
	want := "soonest expiry:  expired 3 days ago"
	if !strings.Contains(out, want) {
		t.Errorf("missing %q\nfull:\n%s", want, out)
	}
	if strings.Contains(out, "-3") {
		t.Errorf("must not print a bare negative number\nfull:\n%s", out)
	}
}

func TestRenderStatusHumanSingularisesOneDayRemaining(t *testing.T) {
	days := 1
	rep := app.StatusReport{
		ContainerState:        "running",
		AdminReachable:        true,
		BackendCount:          1,
		Healthy:               true,
		CertCount:             1,
		SoonestCertExpiryDays: &days,
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, false)
	out := buf.String()
	want := "soonest expiry:  1 day"
	if !strings.Contains(out, want) {
		t.Errorf("missing %q\nfull:\n%s", want, out)
	}
	if strings.Contains(out, "1 days") {
		t.Errorf("must not pluralise a single day\nfull:\n%s", out)
	}
}

func TestRenderStatusHumanSingularisesExpiredOneDayAgo(t *testing.T) {
	days := -1
	rep := app.StatusReport{
		ContainerState:        "running",
		AdminReachable:        true,
		BackendCount:          1,
		Healthy:               true,
		CertCount:             1,
		SoonestCertExpiryDays: &days,
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, false)
	out := buf.String()
	want := "soonest expiry:  expired 1 day ago"
	if !strings.Contains(out, want) {
		t.Errorf("missing %q\nfull:\n%s", want, out)
	}
	if strings.Contains(out, "1 days ago") {
		t.Errorf("must not pluralise a single expired day\nfull:\n%s", out)
	}
}

func TestRenderStatusHumanShowsZeroExpiryAsAmbiguous(t *testing.T) {
	days := 0
	rep := app.StatusReport{
		ContainerState:        "running",
		AdminReachable:        true,
		BackendCount:          1,
		Healthy:               true,
		CertCount:             1,
		SoonestCertExpiryDays: &days,
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, false)
	out := buf.String()
	want := "soonest expiry:  today or in the last day (check cert list)"
	if !strings.Contains(out, want) {
		t.Errorf("missing %q\nfull:\n%s", want, out)
	}
}

func TestRenderStatusJSONCarriesCertFields(t *testing.T) {
	days := -2
	rep := app.StatusReport{
		ContainerState:        "running",
		AdminReachable:        true,
		BackendCount:          1,
		Healthy:               true,
		CertCount:             1,
		SoonestCertExpiryDays: &days,
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, true)
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw:\n%s", err, buf.String())
	}
	if got["cert_count"] != float64(1) {
		t.Errorf("cert_count: got %v want 1", got["cert_count"])
	}
	if got["soonest_cert_expiry_days"] != float64(-2) {
		t.Errorf("soonest_cert_expiry_days: got %v want -2", got["soonest_cert_expiry_days"])
	}
}

func TestRenderStatusJSONOmitsSoonestExpiryWhenNoCertificates(t *testing.T) {
	rep := app.StatusReport{
		ContainerState: "running",
		AdminReachable: true,
		BackendCount:   1,
		Healthy:        true,
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, true)
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw:\n%s", err, buf.String())
	}
	if _, present := got["soonest_cert_expiry_days"]; present {
		t.Errorf("soonest_cert_expiry_days must be omitted when there are no certificates, got %v", got["soonest_cert_expiry_days"])
	}
	if got["cert_count"] != float64(0) {
		t.Errorf("cert_count: got %v want 0", got["cert_count"])
	}
}

func TestRenderStatusJSON(t *testing.T) {
	rep := app.StatusReport{
		ContainerState: "running",
		AdminReachable: true,
		BackendCount:   3,
		Healthy:        true,
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, true)
	var got app.StatusReport
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw:\n%s", err, buf.String())
	}
	if got != rep {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, rep)
	}
}

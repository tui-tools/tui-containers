package main

import (
	"os"
	"os/user"
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/compat"
)

// TestRunReportDemo checks the half of the block this tool owns. The kit's own
// tests cover the machine facts and the scrubbing; what has to be right here is
// that --demo says demo, that the backend the fake imitates is named beside it,
// that nothing claims to have probed an engine on a machine that was never
// read, and that no engine was reached to produce any of it.
func TestRunReportDemo(t *testing.T) {
	var out strings.Builder
	opts := options{demo: true, report: true}
	if err := runReport(baseConfig(), opts, &out); err != nil {
		t.Fatalf("runReport: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"backend: demo\n",
		"mode: demo (sample data, the system was not read)\n",
		"demo backend: host\n",
		"engines: not probed (demo reads no machine)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, toolName+" ") {
		t.Errorf("report should start with the tool name:\n%s", got)
	}
}

// TestRunReportNamesNobody is the privacy promise, asserted where it is cheap
// to assert: the block is pasted into a public issue, so the user name, the
// host name and a home path must not be in it. It runs in demo mode because
// what is being checked is the shape of the block, not the machine.
func TestRunReportNamesNobody(t *testing.T) {
	var out strings.Builder
	if err := runReport(baseConfig(), options{demo: true}, &out); err != nil {
		t.Fatalf("runReport: %v", err)
	}
	got := out.String()

	if strings.Contains(got, "/home/") || strings.Contains(got, "/root/") {
		t.Errorf("the report carries a home path:\n%s", got)
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		// The distro line is exempt: a machine named after its distribution
		// ("fedora") would match it for a reason that has nothing to do with
		// the host name being printed.
		if strings.Contains(withoutDistro(got), host) {
			t.Errorf("the report carries the host name %q:\n%s", host, got)
		}
	}
	if u, err := user.Current(); err == nil && u.Username != "" &&
		strings.Contains(got, u.Username) {
		t.Errorf("the report carries the user name %q:\n%s", u.Username, got)
	}
}

// withoutDistro drops the distro line, which is the one line entitled to carry
// a word a machine may also have been named after.
func withoutDistro(report string) string {
	var kept []string
	for _, line := range strings.Split(report, "\n") {
		if !strings.HasPrefix(line, "distro: ") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// TestDescribeEngines renders every declared engine with the version it
// answered or the reason it did not, which is what tells "podman is not here"
// from "podman is here and its version could not be read".
func TestDescribeEngines(t *testing.T) {
	tests := []struct {
		name    string
		results []compat.Result
		demo    bool
		want    string
	}{
		{
			name: "both engines answered",
			results: []compat.Result{
				{Backend: dockerBackend, Version: "27.3.1"},
				{Backend: podmanBackend, Version: "5.2.2"},
			},
			want: "docker 27.3.1, podman 5.2.2",
		},
		{
			name: "an engine that is not here keeps its reason",
			results: []compat.Result{
				{Backend: dockerBackend, Detail: "executable file not found"},
				{Backend: podmanBackend, Version: "5.2.2"},
			},
			want: "docker (no version: executable file not found), podman 5.2.2",
		},
		{
			name:    "a machine with neither",
			results: nil,
			want:    "none",
		},
		{
			name: "demo probes nothing at all",
			demo: true,
			want: "not probed (demo reads no machine)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeEngines(tc.results, tc.demo); got != tc.want {
				t.Errorf("describeEngines = %q, want %q", got, tc.want)
			}
		})
	}
}

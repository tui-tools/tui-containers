package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/tui-tools/tui-containers/internal/engines"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/config"
)

// baseConfig is the configuration as it stands before the flags are folded in.
func baseConfig() config.Config {
	return config.Config{Tool: toolName, Values: defaults()}
}

func TestParseFlags(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()

	opts, err := parseFlags([]string{"--demo", "--theme", "/t/colors.toml"}, devNull)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !opts.demo || opts.themePath != "/t/colors.toml" {
		t.Errorf("opts = %+v", opts)
	}
	if opts.sudoSet {
		t.Error("sudoSet should be false when -sudo is absent")
	}
}

func TestApplyOverrides(t *testing.T) {
	cfg := baseConfig()
	applyOverrides(&cfg, options{themePath: "/t/colors.toml"})
	if got := cfg.Theme(); got != "/t/colors.toml" {
		t.Errorf("Theme() = %q", got)
	}
	// An untouched -sudo must not clear the configured prefix.
	if got := cfg.String(config.KeySudo, ""); got != "sudo -n" {
		t.Errorf("sudo = %q, want the config value", got)
	}

	// An explicitly empty -sudo disables escalation, which on this tool means
	// dropping root's Podman from the picture entirely.
	cfg = baseConfig()
	applyOverrides(&cfg, options{sudoSet: true, sudo: ""})
	if got := cfg.String(config.KeySudo, "unset"); got != "" {
		t.Errorf("sudo = %q, want empty", got)
	}
	if got := cfg.SudoPrefix(); got != nil {
		t.Errorf("SudoPrefix = %q, want nil", got)
	}
}

func TestDefaultsCoverEveryFlag(t *testing.T) {
	// Every key a flag can override must be declared, otherwise the environment
	// layer silently skips it.
	for _, key := range []string{config.KeySudo, config.KeyTheme} {
		if _, ok := defaults()[key]; !ok {
			t.Errorf("defaults() is missing %q", key)
		}
	}
}

func TestPickBackendDemo(t *testing.T) {
	backend, err := pickBackend(baseConfig(), options{demo: true}, nil)
	if err != nil {
		t.Fatalf("pickBackend: %v", err)
	}
	if !strings.Contains(backend.Describe(), "demo") {
		t.Errorf("Describe = %q, want it to say it is a demo", backend.Describe())
	}
}

// checkOutput runs --check against the demo and returns both the raw text and
// the decoded report, which is what the smoke test asserts on.
func checkOutput(t *testing.T) (string, map[string]any) {
	t.Helper()
	backend, err := pickBackend(baseConfig(), options{demo: true}, nil)
	if err != nil {
		t.Fatalf("pickBackend: %v", err)
	}
	var out bytes.Buffer
	if err := runCheck(backend, nil, &out); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("the report is not valid JSON: %v", err)
	}
	return out.String(), report
}

// TestCheckReportsTheState covers the contract the smoke test depends on: the
// counts, the engines, the containers worth looking at, all where a shell
// script can grep for them without walking the model.
func TestCheckReportsTheState(t *testing.T) {
	text, _ := checkOutput(t)
	for _, want := range []string{
		`"tool": "tui-containers"`,
		`"backend": "host"`,
		// The sample machine is the one the README describes.
		`"containers": 6`,
		`"images": 8`,
		`"dangling": 2`,
		`"projects": 1`,
		`"engineCount": 2`,
		`"attentionCount": 3`,
		`"unhealthyCount": 1`,
		// The engines are reported one per scope, including the one that was
		// not read, with the reason.
		`"engine": "docker"`,
		`"scope": "user"`,
		`"scope": "system"`,
		`"available": false`,
		`sudo -n`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("--check output is missing %s", want)
		}
	}
}

// TestCheckReportsEveryStateEvenTheEmptyOnes: a script asserting on a count
// needs a zero to assert on, not a key that may or may not be there.
func TestCheckReportsEveryStateEvenTheEmptyOnes(t *testing.T) {
	_, report := checkOutput(t)
	states, ok := report["states"].(map[string]any)
	if !ok {
		t.Fatal("--check reports no states map")
	}
	for _, state := range []string{"running", "exited", "restarting", "paused",
		"created", "dead", "removing"} {
		if _, ok := states[state]; !ok {
			t.Errorf("--check does not report a count for %q", state)
		}
	}
}

// TestCheckDistinguishesExitedZeroFromUnread: a script that treated a missing
// exit code as a zero would call a container that never ran a success.
func TestCheckDistinguishesExitedZeroFromUnread(t *testing.T) {
	_, report := checkOutput(t)
	containers, ok := report["model"].(map[string]any)["containers"].([]any)
	if !ok {
		t.Fatal("--check reports no containers")
	}
	var withCode, withoutCode int
	for _, entry := range containers {
		c := entry.(map[string]any)
		if c["exitCodeKnown"] == true {
			withCode++
			continue
		}
		withoutCode++
	}
	if withCode == 0 || withoutCode == 0 {
		t.Errorf("%d containers report an exit code and %d do not; the sample "+
			"machine is meant to have both", withCode, withoutCode)
	}
}

// TestCheckRunsNothing: --check exists to be safe to run anywhere, including
// in CI against a production-shaped machine, so it must not run a single
// command through the backend.
func TestCheckRunsNothing(t *testing.T) {
	backend := engines.NewFake()
	var out bytes.Buffer
	if err := runCheck(backend, nil, &out); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	if ran := backend.Ran(); len(ran) != 0 {
		t.Errorf("--check ran %d commands: %v", len(ran), ran)
	}
	// And it prints no command line: one in the output would mean it had built
	// one.
	for _, forbidden := range []string{
		"docker rm", "podman rm", "system prune", "docker stop", "podman kill",
		"--restart=", "compose up",
	} {
		if strings.Contains(out.String(), forbidden) {
			t.Errorf("--check printed a mutation: %q", forbidden)
		}
	}
}

// TestCheckMasksSecretsToo: --check prints the model in full, which includes
// nothing the detail screen would have masked — because the model carries no
// environment at all until a container is inspected.
func TestCheckMasksSecretsToo(t *testing.T) {
	text, _ := checkOutput(t)
	for _, secret := range []string{"hunter2", "sk_live", "a-long-string"} {
		if strings.Contains(text, secret) {
			t.Errorf("--check printed a value that should never leave the "+
				"detail screen: %q", secret)
		}
	}
}

// TestCapsForFallsBackToCapable: an engine that was not probed must not have a
// key silently do nothing. The backend refuses in its own words instead.
func TestCapsForFallsBackToCapable(t *testing.T) {
	results := []compat.Result{{Backend: dockerBackend}}
	if !capsFor(results, podmanBackend).Has("update-restart") {
		t.Error("an unprobed engine was treated as incapable")
	}
	if !capsFor(nil, dockerBackend).Has("format-json") {
		t.Error("an empty probe list was treated as incapable")
	}
}

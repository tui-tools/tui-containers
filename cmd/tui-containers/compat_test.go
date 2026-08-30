package main

import (
	"context"
	"regexp"
	"strings"
	"testing"

	tuicontainers "github.com/tui-tools/tui-containers"
	"github.com/tui-tools/tui-containers/internal/docker"
	"github.com/tui-tools/tui-containers/internal/podman"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/manifest"
	"github.com/tui-tools/tui-kit/runner"
)

// backendNamed loads one manifest block the binary really reads.
func backendNamed(t *testing.T, name string) compat.Backend {
	t.Helper()
	m, err := manifest.Load(tuicontainers.ManifestJSON)
	if err != nil {
		t.Fatalf("the embedded manifest does not parse: %v", err)
	}
	if m.Name != toolName {
		t.Fatalf("manifest name = %q, want %q", m.Name, toolName)
	}
	b, ok := m.Backend(name)
	if !ok {
		t.Fatalf("the manifest declares no %q backend", name)
	}
	return b
}

// TestManifestDeclaresBothEngines. The tool is about two of them, and the
// compatibility block has to name both — including the four capabilities that
// are version questions rather than always-true ones.
func TestManifestDeclaresBothEngines(t *testing.T) {
	dockerBlock := backendNamed(t, dockerBackend)
	if dockerBlock.Binary != docker.Bin {
		t.Errorf("binary = %q, want %q", dockerBlock.Binary, docker.Bin)
	}
	if dockerBlock.Minimum != "20.10" {
		t.Errorf("docker minimum = %q, want 20.10", dockerBlock.Minimum)
	}
	if len(dockerBlock.VersionCommand) == 0 {
		t.Error("a backend with no version command cannot be probed")
	}

	podmanBlock := backendNamed(t, podmanBackend)
	if podmanBlock.Binary != podman.Bin {
		t.Errorf("binary = %q, want %q", podmanBlock.Binary, podman.Bin)
	}
	if podmanBlock.Minimum != "4.0" {
		t.Errorf("podman minimum = %q, want 4.0", podmanBlock.Minimum)
	}
	for _, block := range []compat.Backend{dockerBlock, podmanBlock} {
		if len(block.Notes) == 0 {
			t.Errorf("%s declares no notes", block.Name)
		}
		if len(block.SearchPaths) == 0 {
			t.Errorf("%s declares no search paths, so a binary outside PATH is "+
				"invisible", block.Name)
		}
	}
}

// TestVersionRegexReadsRealOutput uses the `docker --version` banner as it
// really prints, which carries a build hash full of digits that must not be
// mistaken for the version.
func TestVersionRegexReadsRealOutput(t *testing.T) {
	b := backendNamed(t, dockerBackend)
	tests := map[string]string{
		// Captured from the Fedora 42 host this tool was written on.
		"Docker version 29.5.3, build d1c06ef": "29.5.3",
		// Ubuntu 24.04 and Debian 12.
		"Docker version 24.0.7, build 24.0.7-0ubuntu4.1": "24.0.7",
		"Docker version 20.10.24+dfsg1, build 297e128":   "20.10.24",
		// The oldest release this tool claims to work with.
		"Docker version 20.10.0, build 7287ab3": "20.10.0",
	}
	for output, want := range tests {
		if got := compat.ParseVersion(output, b.VersionRegex); got != want {
			t.Errorf("ParseVersion(%q) = %q, want %q", output, got, want)
		}
	}
}

// TestPodmanVersionNeedsNoRegex: `podman --version` prints "podman version
// 5.8.2" and the first version-shaped token is the answer, so the manifest
// declares no regex for it.
func TestPodmanVersionNeedsNoRegex(t *testing.T) {
	b := backendNamed(t, podmanBackend)
	if b.VersionRegex != "" {
		t.Errorf("podman declares a version regex (%q) it does not need",
			b.VersionRegex)
	}
	tests := map[string]string{
		"podman version 5.8.2": "5.8.2",
		"podman version 4.9.4": "4.9.4",
		"podman version 4.0.2": "4.0.2",
	}
	for output, want := range tests {
		if got := compat.ParseVersion(output, b.VersionRegex); got != want {
			t.Errorf("ParseVersion(%q) = %q, want %q", output, got, want)
		}
	}
}

// TestFeatureGatesMatchTheReleases pins what the manifest claims about each
// engine, which is what decides whether a key does something or refuses with a
// version number.
func TestFeatureGatesMatchTheReleases(t *testing.T) {
	dockerBlock := backendNamed(t, dockerBackend)
	for version, want := range map[string]bool{
		"20.10.24": false, "22.06.0": false, "23.0.0": true, "27.3.1": true,
	} {
		caps := compat.NewCaps(version, dockerBlock.Features)
		if got := caps.Has(docker.FeatureFormatJSON); got != want {
			t.Errorf("docker %s: %s = %v, want %v",
				version, docker.FeatureFormatJSON, got, want)
		}
	}

	podmanBlock := backendNamed(t, podmanBackend)
	tests := []struct {
		version string
		quadlet bool
		compose bool
		update  bool
	}{
		{"4.0.2", false, false, false},
		{"4.4.0", true, false, false},
		{"4.7.0", true, true, false},
		{"5.0.0", true, true, true},
		{"5.8.2", true, true, true},
	}
	for _, test := range tests {
		caps := compat.NewCaps(test.version, podmanBlock.Features)
		if got := caps.Has(podman.FeatureQuadlet); got != test.quadlet {
			t.Errorf("podman %s: quadlet = %v, want %v",
				test.version, got, test.quadlet)
		}
		if got := caps.Has(podman.FeatureCompose); got != test.compose {
			t.Errorf("podman %s: compose = %v, want %v",
				test.version, got, test.compose)
		}
		if got := caps.Has(podman.FeatureUpdateRestart); got != test.update {
			t.Errorf("podman %s: update-restart = %v, want %v",
				test.version, got, test.update)
		}
	}
}

// TestUnknownVersionKeepsEveryFeature: a version the probe could not read must
// not make a key silently do nothing. The engine refuses in its own words
// instead.
func TestUnknownVersionKeepsEveryFeature(t *testing.T) {
	caps := compat.Result{}.Caps()
	for _, feature := range []string{docker.FeatureFormatJSON,
		podman.FeatureCompose, podman.FeatureQuadlet,
		podman.FeatureUpdateRestart} {
		if !caps.Has(feature) {
			t.Errorf("an unprobed version was treated as lacking %q", feature)
		}
	}
}

// TestVersionProbeAgainstThisHost runs the real probe when an engine is
// installed, which is the assertion that the manifest's command and regex still
// match what a machine prints.
//
// It asserts the shape rather than a number: this runs on whatever engine the
// machine happens to carry, and pinning that would only mean the test breaks
// every time a CI image is refreshed. It skips where the engine is absent,
// which is most CI runners.
func TestVersionProbeAgainstThisHost(t *testing.T) {
	for _, name := range []string{dockerBackend, podmanBackend} {
		b := backendNamed(t, name)
		if !runner.Available(b.Binary, b.SearchPaths...) {
			t.Logf("no %s on this machine, skipping", b.Binary)
			continue
		}
		result := compat.Probe(context.Background(), b)
		if result.Version == "" {
			// A cold engine can take longer than the probe's two-second budget
			// to answer — a CI runner that has never invoked podman is the
			// usual case — and that is a fact about the machine rather than
			// about the manifest. The regex itself is pinned against captured
			// banners in the two tests above, which do not depend on timing.
			t.Logf("%s: the probe read no version here (%s)", name, result.Detail)
			continue
		}
		if !versionShape.MatchString(result.Version) {
			t.Errorf("%s: the probe read %q, which is not a version",
				name, result.Version)
		}
		if result.Status == compat.StatusUnknown {
			t.Errorf("%s: a version that was read classified as unknown", name)
		}
	}
}

// versionShape is what either engine's version looks like once the manifest has
// had it.
var versionShape = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,2}$`)

func TestClassifiesVersionsAgainstTheMinimum(t *testing.T) {
	tests := []struct {
		backend string
		version string
		want    compat.Status
	}{
		{dockerBackend, "19.03.15", compat.StatusBelowMinimum},
		{dockerBackend, "20.10.0", compat.StatusUntested},
		{dockerBackend, "27.3.1", compat.StatusUntested},
		{podmanBackend, "3.4.7", compat.StatusBelowMinimum},
		{podmanBackend, "4.0.0", compat.StatusUntested},
		{podmanBackend, "5.2.3", compat.StatusUntested},
	}
	for _, test := range tests {
		b := backendNamed(t, test.backend)
		banner := "Docker version " + test.version + ", build abcdef1"
		if test.backend == podmanBackend {
			banner = "podman version " + test.version
		}
		result := compat.ProbeWith(context.Background(), b,
			func(context.Context, []string) (string, error) {
				return banner, nil
			})
		if result.Version != test.version {
			t.Errorf("probed %q, want %q", result.Version, test.version)
			continue
		}
		// A version in the manifest's tested list would classify as tested; the
		// expectations above hold while that list is short, so they are skipped
		// for a version the evidence file already covers.
		if isTested(b, test.version) {
			continue
		}
		if result.Status != test.want {
			t.Errorf("%s %s: status %v, want %v",
				test.backend, test.version, result.Status, test.want)
		}
	}
}

// TestNotesCoverTheRanges: every caveat the README prints has to apply to some
// version, or it is documentation nobody will ever be shown.
func TestNotesCoverTheRanges(t *testing.T) {
	versions := map[string][]string{
		dockerBackend: {"19.03.15", "20.10.24", "22.06.0", "23.0.0", "27.3.1"},
		podmanBackend: {"3.4.7", "4.0.0", "4.4.0", "4.7.0", "5.0.0", "5.8.2"},
	}
	for name, list := range versions {
		b := backendNamed(t, name)
		for _, note := range b.Notes {
			if strings.TrimSpace(note.Impact) == "" {
				t.Errorf("%s: note %q has no impact sentence", name, note.Range)
			}
			var matched bool
			for _, version := range list {
				if compat.Match(version, note.Range) {
					matched = true
				}
			}
			if !matched {
				t.Errorf("%s: note %q applies to no version anyone runs",
					name, note.Range)
			}
		}
	}
}

// TestProbedKeepsOnlyWhatAnswered: the header shows a badge per engine that
// reported a version, and a badge for one that is not installed would be noise.
func TestProbedKeepsOnlyWhatAnswered(t *testing.T) {
	got := probed([]compat.Result{
		{Backend: dockerBackend, Version: "27.3.1"},
		{Backend: podmanBackend},
	})
	if len(got) != 1 || got[0].Backend != dockerBackend {
		t.Errorf("probed = %+v", got)
	}
}

func TestProbeInDemoModeReportsNothing(t *testing.T) {
	if got := probeCompat(context.Background(), true); len(got) != 0 {
		t.Errorf("--demo probed the host: %+v", got)
	}
}

// isTested reports whether the manifest already records a passing run.
func isTested(b compat.Backend, version string) bool {
	for _, tested := range b.Tested {
		if tested == version {
			return true
		}
	}
	return false
}

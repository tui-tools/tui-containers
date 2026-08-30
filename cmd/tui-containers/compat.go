package main

import (
	"context"

	tuicontainers "github.com/tui-tools/tui-containers"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/manifest"
)

// probeCompat reads the version of every engine this tool drives.
//
// There are two, and both answer a plain `--version`. What differs is what the
// version is *for*. Docker's gates one thing: `--format json` as a shorthand
// arrived in 23.0, and below it the same output comes from
// `--format '{{json .}}'`. Podman's gates three, because Podman has grown:
// Quadlet units did not exist before 4.4, `podman compose` before 4.7, and
// `podman update --restart` before 5.0 — so on an older Podman those keys
// refuse with the version that would have them rather than running a command
// the engine would reject.
//
// What each is judged against — the minimum version, the versions the lab has
// actually run against, the caveats that apply to a range — comes from the
// repository's own tool.json, embedded in the binary, so there is no second
// copy of them in the code.
//
// It never fails. A manifest that cannot be parsed produces no results at all,
// and an engine this machine does not have produces one with an empty version
// and the reason: on a tool about containers, "podman is not installed here" is
// an answer worth showing rather than an error.
func probeCompat(ctx context.Context, demo bool) []compat.Result {
	// --demo drives an in-memory machine; probing the real engines on the host
	// would report versions that have nothing to do with what is on screen.
	if demo {
		return nil
	}
	m, err := manifest.Load(tuicontainers.ManifestJSON)
	if err != nil {
		return nil
	}
	results := make([]compat.Result, 0, len(m.Backends))
	for _, backend := range m.Backends {
		results = append(results, compat.Probe(ctx, backend))
	}
	return results
}

// capsFor is one engine's capability set, which is what gates a version-
// dependent read path or action.
//
// The zero Caps answers yes to everything, and that is the right default for an
// engine that was not probed: an engine that cannot do what was asked refuses
// in its own words, and that is a better message than a key that silently does
// nothing over an unreadable version string.
func capsFor(results []compat.Result, name string) compat.Caps {
	for _, result := range results {
		if result.Backend == name {
			return result.Caps()
		}
	}
	return compat.Result{}.Caps()
}

// probed keeps the engines that answered with a version, which are the ones
// this machine actually has. It is what the header shows: a badge for an engine
// that is not installed would be noise, and the system screen says so in words
// instead.
func probed(results []compat.Result) []compat.Result {
	kept := make([]compat.Result, 0, len(results))
	for _, result := range results {
		if result.Version != "" {
			kept = append(kept, result)
		}
	}
	return kept
}

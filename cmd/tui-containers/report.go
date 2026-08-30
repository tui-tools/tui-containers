package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/report"
	"github.com/tui-tools/tui-kit/theme"
)

// hostDetail explains the one line of the block that would otherwise read as a
// failed probe. This tool's backend is the machine itself, and a machine has no
// version: the versions that matter are the engines', and they get their own
// line right below.
const hostDetail = "the machine itself; the engine versions are on the engines line"

// runReport prints the block a bug report needs and exits. Everything generic
// — the kit version, the distribution, the kernel, the terminal, where the
// binary came from — is collected by the kit, so the whole family answers
// --report in the same shape. What this function adds is the part only
// tui-containers knows: which engines this machine has and at which versions,
// which is what decides nearly every question asked of a report here — an
// action refused by a Podman below 5.0 and one refused by a bug look the same
// from the outside.
//
// It reads no engine. --check is the flag that does that, and it needs a socket
// this account may not be able to reach; a report has to work for a user who
// cannot reach it, because the unreachable socket may be the bug. A machine
// with neither engine installed still gets a report, with "none" on the engines
// line: "there is nothing here to drive" is a bug report, not a refusal.
func runReport(cfg config.Config, opts options, out io.Writer) error {
	palette, _ := theme.ResolvePalette()

	// The same probe the header and --check use. There is one version probe in
	// this tool and this is it. It runs `docker --version` and
	// `podman --version`, which read nothing and change nothing.
	results := probeCompat(context.Background(), opts.demo)

	var backendName, selectError string
	if backend, err := pickBackend(cfg, opts, results); err != nil {
		selectError = err.Error()
	} else {
		backendName = backend.Name()
	}

	info := report.Info{
		Tool:          toolName,
		Version:       version,
		Backend:       backendName,
		BackendDetail: hostDetail,
		Demo:          opts.demo,
		Sudo:          cfg.String(config.KeySudo, ""),
		Theme:         palette.Name,
	}
	if opts.demo {
		// The fake stands in for the same backend the real one is, and it
		// answers to the same name; naming it on its own line keeps the
		// backend line free to say demo, which is the thing a reader must not
		// miss.
		info.Backend = "demo"
		imitated := backendName
		if imitated == "" {
			imitated = "host"
		}
		info.Extra = append(info.Extra, report.Field{
			Key: "demo backend", Value: imitated,
		})
	}
	info.Extra = append(info.Extra, report.Field{
		Key: "engines", Value: describeEngines(results, opts.demo),
	})
	if selectError != "" {
		info.Extra = append(info.Extra, report.Field{
			Key: "backend error", Value: selectError,
		})
	}

	_, err := io.WriteString(out, report.Render(info))
	return err
}

// describeEngines renders every engine this tool declares, with the version it
// answered or the reason it did not, as one line. A report that named only the
// engine that answered would leave the reader guessing whether the other was
// absent or merely unreadable from this account, and that difference is most of
// the "nothing is listed" reports.
func describeEngines(results []compat.Result, demo bool) string {
	if demo {
		// --demo drives an in-memory machine, so nothing was probed. "none"
		// here would read as a machine with no engine installed.
		return "not probed (demo reads no machine)"
	}
	parts := make([]string, 0, len(results))
	for _, result := range results {
		name := strings.TrimSpace(result.Backend)
		if name == "" {
			continue
		}
		switch {
		case strings.TrimSpace(result.Version) != "":
			parts = append(parts, name+" "+strings.TrimSpace(result.Version))
		case strings.TrimSpace(result.Detail) != "":
			parts = append(parts, name+" (no version: "+result.Detail+")")
		default:
			parts = append(parts, name+" (no version)")
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// reportUsage is the flag's one-line help, kept here next to what it prints.
var reportUsage = fmt.Sprintf(
	"print the versions and machine facts a bug report needs, then exit "+
		"(no UI, no privileges, nothing about you: paste it into a %s issue)",
	toolName)

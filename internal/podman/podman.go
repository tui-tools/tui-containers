// Package podman drives the `podman` CLI. It is one of the two packages in
// this tool allowed to start a process; the UI never builds an argv itself, and
// tui-kit/tools/check-exec.sh fails the build if any other package imports
// os/exec.
//
// Podman has two scopes and they are not two views of one thing. The containers
// in your own account are rootless: their storage is under your home, their
// networking is a user-space stack, and root cannot see them without becoming
// you. The containers root owns are somewhere else entirely. A machine can have
// both, with different containers in each, and a tool that showed one and
// called it "the containers" would be wrong on exactly the machines where it
// matters. So a Backend is one scope, and the joiner holds one per scope that
// answered.
package podman

import (
	"context"
	"strings"
	"time"

	"github.com/tui-tools/tui-containers/internal/container"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/runner"
)

// readTimeout bounds one read. Podman's first call in a session initialises its
// store, which on a cold machine is not instant.
const readTimeout = 20 * time.Second

// searchPaths are the absolute fallbacks tried when podman is not on PATH.
var searchPaths = []string{"/usr/bin/podman", "/usr/local/bin/podman"}

// Backend is one scope of Podman.
type Backend struct {
	scope  container.Scope
	runner *runner.Runner
	caps   compat.Caps
	now    func() time.Time
}

// Available reports whether the podman binary is on this machine.
func Available() bool { return runner.Available(Bin, searchPaths...) }

// New builds the backend for one scope.
//
// The user scope runs as the calling account with no escalation at all — that
// is what rootless means, and escalating into it would reach a different set of
// containers. The system scope runs through the privilege prefix for reads as
// well as for writes, because root's containers are not visible to anyone else:
// there is no unprivileged read of them to try first.
func New(scope container.Scope, sudoPrefix []string, caps compat.Caps) (*Backend, error) {
	options := runner.Options{
		Bin:         Bin,
		SearchPaths: searchPaths,
		Timeout:     readTimeout,
		InstallHint: "install podman to manage its containers",
	}
	privileged := scope == container.ScopeSystem
	options.PrivilegedReads = &privileged
	if privileged {
		if len(sudoPrefix) == 0 {
			return nil, runner.ErrNotAvailable
		}
		options.SudoPrefix = sudoPrefix
	}
	r, err := runner.New(options)
	if err != nil {
		return nil, err
	}
	return &Backend{scope: scope, runner: r, caps: caps, now: time.Now}, nil
}

// Scope is which set of containers this backend is about.
func (b *Backend) Scope() container.Scope { return b.scope }

// Target is the engine and scope every row this backend produces carries.
func (b *Backend) Target() container.Target { return TargetFor(b.scope) }

// Describe names this half for the header.
func (b *Backend) Describe() string {
	if b.scope == container.ScopeSystem {
		return "podman (system) via " + strings.Join(b.runner.Privilege, " ")
	}
	return "podman rootless"
}

// Owns reports whether a command belongs to this package.
func (b *Backend) Owns(cmd container.Command) bool {
	return len(cmd.Argv) > 0 && cmd.Argv[0] == Bin
}

// Preview renders the exact command line Run will execute, privilege prefix
// included — which for the system scope is the whole point: a reader has to see
// that this one goes to root's Podman and the row above it did not.
func (b *Backend) Preview(cmd container.Command) string {
	return b.runner.Preview(cmd)
}

// Run executes a previously previewed command.
func (b *Backend) Run(ctx context.Context, cmd container.Command) (string, error) {
	return b.runner.Run(ctx, cmd)
}

// read runs one read through this scope's runner.
func (b *Backend) read(ctx context.Context, argv ...string) (string, error) {
	return b.runner.Read(ctx, argv...)
}

// Probe asks this scope's Podman about itself.
//
// It is what decides whether a scope is shown at all. The system scope in
// particular is only real when `sudo -n podman info` answers: a machine where
// this account cannot escalate without a password has a system scope it cannot
// read, and reporting it as empty would be reporting that root runs nothing.
func (b *Backend) Probe(ctx context.Context) container.EngineInfo {
	out, err := b.read(ctx, InfoArgv()...)
	if err == nil {
		if info, parseErr := ParseInfo(out, b.scope); parseErr == nil {
			info.Escalated = b.scope == container.ScopeSystem
			return info
		} else {
			err = parseErr
		}
	}
	return container.EngineInfo{
		Target:    b.Target(),
		Installed: true,
		Escalated: b.scope == container.ScopeSystem,
		Detail:    b.probeDetail(err),
	}
}

// probeDetail turns a failed probe into a sentence a reader can act on. The
// system scope's usual failure is not an engine failure at all — it is `sudo
// -n` refusing to run without a password — and saying so is more use than
// repeating sudo's own words.
func (b *Backend) probeDetail(err error) string {
	message := "podman did not answer"
	if err != nil {
		message = runner.FirstLine(err.Error())
	}
	if b.scope == container.ScopeSystem &&
		(strings.Contains(message, "password") || strings.Contains(message, "sudo")) {
		return "root's containers were not read: `sudo -n` needs a password " +
			"here, and this tool never prompts for one"
	}
	return message
}

// Load reads everything this scope knows, and reports what it could not read
// rather than returning an error: a scope whose volume list is unreadable still
// has containers worth showing, and a screen that refused to draw over one
// failed read would be a screen nobody could use.
func (b *Backend) Load(ctx context.Context, info *container.EngineInfo) (
	[]container.Container, []container.Image, []container.Volume,
	[]container.Network) {
	var containers []container.Container
	if out, err := b.read(ctx, PSArgv()...); err != nil {
		note(info, err)
	} else if parsed, err := ParseContainers(out, b.scope, b.now()); err != nil {
		note(info, err)
	} else {
		containers = parsed
	}

	var images []container.Image
	if out, err := b.read(ctx, ImagesArgv()...); err != nil {
		note(info, err)
	} else if parsed, err := ParseImages(out, b.scope); err != nil {
		note(info, err)
	} else {
		images = parsed
	}

	var volumes []container.Volume
	if out, err := b.read(ctx, VolumesArgv()...); err != nil {
		note(info, err)
	} else if parsed, err := ParseVolumes(out, b.scope); err != nil {
		note(info, err)
	} else {
		volumes = parsed
	}

	var networks []container.Network
	if out, err := b.read(ctx, NetworksArgv()...); err != nil {
		note(info, err)
	} else if parsed, err := ParseNetworks(out, b.scope); err != nil {
		note(info, err)
	} else {
		networks = parsed
	}

	if out, err := b.read(ctx, DiskArgv()...); err == nil {
		info.Disk = ParseDisk(out)
	}
	if b.caps.Has(FeatureCompose) {
		if out, err := b.read(ctx, ComposeVersionArgv()...); err == nil {
			info.Compose = true
			info.ComposeVersion = lastLine(out)
		}
	}
	if b.caps.Has(FeatureQuadlet) {
		info.Quadlets = FindQuadlets(b.scope)
	}
	return containers, images, volumes, networks
}

// note appends a reason to the engine summary, so a partial read says which
// part is missing instead of leaving a blank screen to interpret.
func note(info *container.EngineInfo, err error) {
	message := runner.FirstLine(err.Error())
	if info.Detail == "" {
		info.Detail = message
		return
	}
	if !strings.Contains(info.Detail, message) {
		info.Detail += "; " + message
	}
}

// lastLine is the useful half of `podman compose version`, which prints a
// banner naming the external provider it found before the provider's own
// version line.
func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// CanUpdateRestart reports whether this Podman can change a restart policy in
// place, which it has only been able to do since 5.0.
func (b *Backend) CanUpdateRestart() bool { return b.caps.Has(FeatureUpdateRestart) }

// CanCompose reports whether `podman compose` exists on this version.
func (b *Backend) CanCompose() bool { return b.caps.Has(FeatureCompose) }

// UpdateRestartSince is the version the restart policy became updatable in, for
// the message that explains why the key did nothing.
func (b *Backend) UpdateRestartSince() string {
	since, _ := b.caps.Since(FeatureUpdateRestart)
	return since
}

// Inspect reads one container in full, and takes a stats sample for it when it
// is running.
func (b *Backend) Inspect(ctx context.Context, c container.Container) (
	container.Detail, error) {
	argv, err := InspectArgv(c.Ref())
	if err != nil {
		return container.Detail{}, err
	}
	out, err := b.read(ctx, argv...)
	if err != nil {
		return container.Detail{}, err
	}
	detail, err := ParseInspect(out)
	if err != nil {
		return container.Detail{}, err
	}
	detail.Container = InspectContainer(c, out)

	if !c.Running() {
		detail.StatsErr = c.Name + " is not running, so there is nothing to sample"
		return detail, nil
	}
	statsArgv, err := StatsArgv(c.Ref())
	if err != nil {
		detail.StatsErr = err.Error()
		return detail, nil
	}
	sample, err := b.read(ctx, statsArgv...)
	if err != nil {
		detail.StatsErr = runner.FirstLine(err.Error())
		return detail, nil
	}
	stats, err := ParseStats(sample)
	if err != nil {
		detail.StatsErr = err.Error()
		return detail, nil
	}
	detail.Stats = stats
	return detail, nil
}

// Logs reads the end of a container's log.
func (b *Backend) Logs(ctx context.Context, c container.Container,
	opts container.LogOptions) (string, error) {
	argv, err := LogsArgv(c.Ref(), opts)
	if err != nil {
		return "", err
	}
	// A container that has never started has no log, and `podman logs` says so
	// with a message rather than an empty answer; that message is the useful
	// thing to show, so a failure here returns whatever came back.
	out, err := b.read(ctx, argv...)
	if err != nil && strings.TrimSpace(out) == "" {
		return "", err
	}
	return out, nil
}

// LogsCommand renders the command line the log pane is running, so the pane can
// name it. A pane that re-reads on a timer has to be able to say what it is
// re-reading.
func (b *Backend) LogsCommand(c container.Container,
	opts container.LogOptions) string {
	argv, err := LogsArgv(c.Ref(), opts)
	if err != nil {
		return ""
	}
	return b.Preview(container.Command{Argv: argv})
}

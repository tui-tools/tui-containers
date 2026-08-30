// Package docker drives the `docker` CLI. It is one of the two packages in
// this tool allowed to start a process; the UI never builds an argv itself, and
// tui-kit/tools/check-exec.sh fails the build if any other package imports
// os/exec.
//
// Docker has one scope. dockerd owns every container on the machine, and who
// may talk to it is a question about the socket rather than about the
// container: an account in the `docker` group reaches it directly, a rootless
// installation reaches its own daemon, and everyone else needs root. That last
// case is the only reason this package holds two runners — see Probe.
package docker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tui-tools/tui-containers/internal/container"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/runner"
)

// readTimeout bounds one read. `docker ps` on a busy daemon is not instant and
// `docker system df` walks the whole store, so it is generous; nothing here can
// hang the UI, because every read runs off the main loop.
const readTimeout = 20 * time.Second

// searchPaths are the absolute fallbacks tried when docker is not on PATH.
var searchPaths = []string{"/usr/bin/docker", "/usr/local/bin/docker"}

// Backend is the Docker half of the tool.
type Backend struct {
	// plain reaches the daemon as the calling account, which is how it works
	// on a machine where that account is in the `docker` group or is running a
	// rootless daemon of its own.
	plain *runner.Runner
	// escalated reaches it through the privilege prefix, and is nil when none
	// is configured. It exists for the third case: a machine where the socket
	// is root's and this account is not in the group.
	escalated *runner.Runner
	// useSudo records which of the two answered, decided once by Probe. It is
	// a property of the machine rather than of a command, so it is decided
	// before anything is previewed and the preview shows the prefix that will
	// really be used.
	useSudo bool
	caps    compat.Caps
}

// Available reports whether the docker binary is on this machine.
func Available() bool { return runner.Available(Bin, searchPaths...) }

// New resolves the binary and both runners. caps comes from the version probe,
// so no version number is written into this package.
func New(sudoPrefix []string, caps compat.Caps) (*Backend, error) {
	unprivileged := false
	plain, err := runner.New(runner.Options{
		Bin:             Bin,
		SearchPaths:     searchPaths,
		Timeout:         readTimeout,
		PrivilegedReads: &unprivileged,
		InstallHint:     "install docker-ce or moby-engine to manage Docker containers",
	})
	if err != nil {
		return nil, err
	}
	b := &Backend{plain: plain, caps: caps}
	if len(sudoPrefix) > 0 {
		privileged := true
		if escalated, err := runner.New(runner.Options{
			Bin:             Bin,
			SearchPaths:     searchPaths,
			Timeout:         readTimeout,
			SudoPrefix:      sudoPrefix,
			PrivilegedReads: &privileged,
		}); err == nil {
			b.escalated = escalated
		}
	}
	return b, nil
}

// shorthand reports whether this Docker understands `--format json`. Below
// 23.0 it does not, and the template form is used instead.
func (b *Backend) shorthand() bool { return b.caps.Has(FeatureFormatJSON) }

// active is the runner every call goes through, which Probe decided.
func (b *Backend) active() *runner.Runner {
	if b.useSudo && b.escalated != nil {
		return b.escalated
	}
	return b.plain
}

// Describe names this half for the header.
func (b *Backend) Describe() string {
	if b.useSudo && b.escalated != nil {
		return "docker via " + strings.Join(b.escalated.Privilege, " ")
	}
	return "docker as " + b.plain.Name
}

// Owns reports whether a command belongs to this package, which is what routes
// a confirmed command back to the runner that previewed it.
func (b *Backend) Owns(cmd container.Command) bool {
	return len(cmd.Argv) > 0 && cmd.Argv[0] == Bin
}

// Preview renders the exact command line Run will execute.
func (b *Backend) Preview(cmd container.Command) string {
	return b.active().Preview(cmd)
}

// Run executes a previously previewed command.
func (b *Backend) Run(ctx context.Context, cmd container.Command) (string, error) {
	return b.active().Run(ctx, cmd)
}

// read runs one read through the active runner.
func (b *Backend) read(ctx context.Context, argv ...string) (string, error) {
	return b.active().Read(ctx, argv...)
}

// Probe asks the daemon about itself, and decides how to reach it.
//
// It is the one call that matters on a strange machine, because "docker is
// installed" and "docker answers" are different facts and only the second one
// is worth building a screen on. The order is deliberate: the unprivileged call
// first, because on a correctly set up machine it works and nothing should
// escalate to read; then the privileged one, because a machine where the socket
// belongs to root and this account is not in the `docker` group is an ordinary
// machine and not an error.
//
// What it never does is guess. An engine that answers neither way is reported
// as not available with the daemon's own words for why, and the screen says
// that rather than showing an empty list.
func (b *Backend) Probe(ctx context.Context) container.EngineInfo {
	argv := InfoArgv(b.shorthand())

	out, err := b.plain.Read(ctx, argv...)
	if err == nil {
		if info, parseErr := ParseInfo(out); parseErr == nil {
			b.useSudo = false
			return info
		}
	}
	firstErr := err

	if b.escalated != nil {
		out, err = b.escalated.Read(ctx, argv...)
		if err == nil {
			if info, parseErr := ParseInfo(out); parseErr == nil {
				b.useSudo = true
				info.Escalated = true
				return info
			}
		}
	}

	detail := "the docker daemon did not answer"
	if firstErr != nil {
		detail = runner.FirstLine(firstErr.Error())
	}
	return container.EngineInfo{
		Target:    Target,
		Installed: true,
		Detail:    detail,
	}
}

// Load reads everything this engine knows, and reports what it could not read
// rather than returning an error: a machine whose volume list is unreadable
// still has containers worth showing, and a screen that refused to draw over
// one failed read would be a screen nobody could use.
func (b *Backend) Load(ctx context.Context, info *container.EngineInfo) (
	[]container.Container, []container.Image, []container.Volume,
	[]container.Network) {
	shorthand := b.shorthand()

	var containers []container.Container
	if out, err := b.read(ctx, PSArgv(shorthand)...); err != nil {
		note(info, err)
	} else if parsed, err := ParseContainers(out); err != nil {
		note(info, err)
	} else {
		containers = parsed
	}

	var images []container.Image
	if out, err := b.read(ctx, ImagesArgv(shorthand)...); err != nil {
		note(info, err)
	} else if parsed, err := ParseImages(out); err != nil {
		note(info, err)
	} else {
		images = parsed
	}

	var volumes []container.Volume
	if out, err := b.read(ctx, VolumesArgv(shorthand)...); err != nil {
		note(info, err)
	} else if parsed, err := ParseVolumes(out); err != nil {
		note(info, err)
	} else {
		volumes = parsed
	}

	var networks []container.Network
	if out, err := b.read(ctx, NetworksArgv(shorthand)...); err != nil {
		note(info, err)
	} else if parsed, err := ParseNetworks(out); err != nil {
		note(info, err)
	} else {
		networks = parsed
	}

	if out, err := b.read(ctx, DiskArgv()...); err == nil {
		info.Disk = ParseDisk(out)
	}
	if out, err := b.read(ctx, ComposeVersionArgv()...); err == nil {
		info.Compose = true
		info.ComposeVersion = runner.FirstLine(out)
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
	statsArgv, err := StatsArgv(c.Ref(), b.shorthand())
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
	// A container that has never started has no log, and `docker logs` says so
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

// CheckPolicy reports whether a restart policy is one this engine accepts,
// which is what the update picker is built from.
func CheckPolicy(policy string) error {
	for _, allowed := range RestartPolicies {
		if policy == allowed {
			return nil
		}
	}
	return fmt.Errorf("docker: %q is not a restart policy", policy)
}

package engines

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tui-tools/tui-containers/internal/container"
	"github.com/tui-tools/tui-containers/internal/docker"
	"github.com/tui-tools/tui-containers/internal/podman"
	"github.com/tui-tools/tui-kit/runner"
)

// Fake is the sample machine behind --demo. It satisfies container.Backend and
// touches nothing.
//
// It is not a mock of the UI: every command is built by the same
// internal/docker and internal/podman functions the real backend uses, and
// previewed through a runner.Fake, so every key works and every confirm dialog
// shows a real command line. What is faked is the machine underneath — the two
// engines, their containers, their images — and the runner that would have run
// the command.
//
// The machine it describes is a deliberate one, because a demo of a tool about
// containers is worthless if everything on it is fine. It has both engines at
// once; a Compose project of three services with one of them unhealthy; a job
// that was killed by the out-of-memory killer and left exit 137 behind; a
// container stuck in a restart loop; and two dangling images from a rebuild.
// Those are the four things a reader opens this tool to find.
type Fake struct {
	fake  *runner.Fake
	now   time.Time
	model container.Model
}

// NewFake builds the sample machine.
func NewFake() *Fake {
	// A fixed clock keeps the demo — and the screenshots taken from it —
	// identical on every machine and every day. The times below are all
	// relative to it, so the columns read like a machine that has been up for
	// a while rather than one built this second.
	now := time.Date(2026, 8, 30, 9, 45, 0, 0, time.UTC)
	f := &Fake{
		fake: &runner.Fake{Prefix: "sudo -n", Default: "ok"},
		now:  now,
	}
	f.model = sampleModel(now)
	return f
}

// Name identifies the backend, which is the same name the real one answers, so
// a script reading --check does not have to care which it ran against.
func (f *Fake) Name() string { return "host" }

// Describe says plainly that this is not the machine.
func (f *Fake) Describe() string {
	return "demo: a sample machine with docker and podman, nothing real"
}

// Capabilities reports everything supported, because the demo is where a
// reader tries the keys.
func (f *Fake) Capabilities() container.Capabilities {
	return container.Capabilities{
		SupportsLifecycle:  true,
		SupportsRemove:     true,
		SupportsUpdate:     true,
		SupportsCompose:    true,
		SupportsPrune:      true,
		SupportsAutoUpdate: true,
		RestartPolicies:    docker.RestartPolicies,
	}
}

// Preview renders the command the way the real runner would, prefix included,
// so the demo's dialogs look like the real thing.
func (f *Fake) Preview(target container.Target, cmd container.Command) string {
	// Only the escalated targets carry a prefix, the same rule the real
	// backend applies: rootless Podman and a Docker whose socket this account
	// can reach are called directly.
	if target.Scope == container.ScopeSystem {
		return f.fake.Preview(cmd)
	}
	return cmd.String()
}

// Run records the command and answers from the canned table. Nothing runs.
func (f *Fake) Run(ctx context.Context, target container.Target,
	cmd container.Command) (string, error) {
	out, err := f.fake.Run(ctx, cmd)
	if err != nil {
		return "", err
	}
	// The engines echo what they acted on, and the status line shows it, so
	// the demo's feedback looks like the real one's.
	if len(cmd.Argv) > 0 {
		return cmd.Argv[len(cmd.Argv)-1], nil
	}
	return out, nil
}

// Ran is every command the demo was asked to run, for the tests that assert a
// key produced exactly one command with exactly the argv the preview showed.
func (f *Fake) Ran() []container.Command { return f.fake.Ran }

// Load returns the sample machine.
func (f *Fake) Load(context.Context) (container.Model, error) {
	model := f.model
	CrossReference(&model)
	container.SortContainers(model.Containers)
	container.SortImages(model.Images)
	model.Projects = container.GroupProjects(model.Containers)
	return model, nil
}

// Inspect returns the sample detail for a container.
func (f *Fake) Inspect(_ context.Context, c container.Container) (
	container.Detail, error) {
	detail := sampleDetail(c, f.now)
	detail.Container = c
	return detail, nil
}

// Logs returns sample log lines, honouring the options so the pane's controls
// visibly do something.
func (f *Fake) Logs(_ context.Context, c container.Container,
	opts container.LogOptions) (string, error) {
	return sampleLogs(c, opts, f.now), nil
}

// LogsCommand renders the command line the pane is re-reading.
func (f *Fake) LogsCommand(c container.Container, opts container.LogOptions) string {
	var argv []string
	var err error
	if c.Target.Engine == container.EnginePodman {
		argv, err = podman.LogsArgv(c.Ref(), opts)
	} else {
		argv, err = docker.LogsArgv(c.Ref(), opts)
	}
	if err != nil {
		return ""
	}
	return f.Preview(c.Target, container.Command{Argv: argv})
}

// The build methods below are the real ones. A demo whose confirm dialogs
// showed invented command lines would be teaching the wrong thing, so every one
// of these goes through the same argv builder the real backend uses and only
// the execution is faked.

func (f *Fake) BuildStart(c container.Container) (container.Action, error) {
	return lifecycleAction(c, "start")
}

func (f *Fake) BuildStop(c container.Container) (container.Action, error) {
	return lifecycleAction(c, "stop")
}

func (f *Fake) BuildRestart(c container.Container) (container.Action, error) {
	return lifecycleAction(c, "restart")
}

func (f *Fake) BuildKill(c container.Container) (container.Action, error) {
	return lifecycleAction(c, "kill")
}

func (f *Fake) BuildPause(c container.Container) (container.Action, error) {
	return lifecycleAction(c, "pause")
}

func (f *Fake) BuildUnpause(c container.Container) (container.Action, error) {
	return lifecycleAction(c, "unpause")
}

func (f *Fake) BuildRemove(c container.Container, force bool) (container.Action, error) {
	return removeAction(c, force)
}

// BuildUpdateRestart. The sample Podman is a 5.2, which is above the release
// that made a restart policy updatable, so the demo answers yes — the same
// answer the real backend gives on the same version.
func (f *Fake) BuildUpdateRestart(c container.Container, policy string) (
	container.Action, error) {
	return updateRestartAction(c, policy, true, "")
}

func (f *Fake) BuildPullImage(c container.Container) (container.Action, error) {
	return pullAction(c)
}

func (f *Fake) BuildCompose(p container.Project, verb string) (container.Action, error) {
	return composeAction(p, verb)
}

func (f *Fake) BuildRemoveImage(i container.Image, force bool) (container.Action, error) {
	return removeImageAction(i, force)
}

func (f *Fake) BuildPruneImages(target container.Target, all bool) (
	container.Action, error) {
	return pruneImagesAction(target, all), nil
}

func (f *Fake) BuildRemoveVolume(v container.Volume) (container.Action, error) {
	return removeVolumeAction(v)
}

func (f *Fake) BuildPruneVolumes(target container.Target) (container.Action, error) {
	return pruneVolumesAction(target), nil
}

func (f *Fake) BuildRemoveNetwork(n container.Network) (container.Action, error) {
	return removeNetworkAction(n)
}

func (f *Fake) BuildPruneNetworks(target container.Target) (container.Action, error) {
	return pruneNetworksAction(target), nil
}

func (f *Fake) BuildSystemPrune(target container.Target,
	opts container.PruneOptions) (container.Action, error) {
	return systemPruneAction(target, opts), nil
}

func (f *Fake) BuildAutoUpdate(target container.Target) (container.Action, error) {
	return autoUpdateAction(target)
}

// dockerTarget and the two podman targets are the sample machine's engines.
var (
	dockerTarget = docker.Target
	podmanUser   = podman.TargetFor(container.ScopeUser)
	podmanSystem = podman.TargetFor(container.ScopeSystem)
)

// composeLabels builds the four labels Compose writes, which is what makes a
// container part of a project as far as this tool is concerned.
func composeLabels(project, service, dir string) map[string]string {
	return map[string]string{
		docker.LabelProject:     project,
		docker.LabelService:     service,
		docker.LabelWorkingDir:  dir,
		docker.LabelConfigFiles: dir + "/compose.yaml",
	}
}

// applyLabels fills a sample container's Compose fields from its labels, the
// same way the parsers do for a real one. The demo sets the labels and lets
// this derive the rest, so a sample container that is missing a label behaves
// exactly like a real one that is: no project, and the Compose key refusing
// with the reason.
func applyLabels(list []container.Container) []container.Container {
	for i := range list {
		c := &list[i]
		c.Project = c.Labels[docker.LabelProject]
		c.Service = c.Labels[docker.LabelService]
		c.WorkingDir = c.Labels[docker.LabelWorkingDir]
	}
	return list
}

// sampleModel is the machine the demo drives.
func sampleModel(now time.Time) container.Model {
	const projectDir = "/srv/shopfront"

	containers := []container.Container{
		{
			ID: "3f9c1a2b7d40", Name: "shopfront-web", Target: dockerTarget,
			Image: "shopfront/web:2.4.1", Command: "nginx -g daemon off;",
			State: container.StateRunning, Status: "Up 6 days (healthy)",
			Health: container.HealthHealthy, Uptime: "6 days",
			Created: now.Add(-9 * 24 * time.Hour), Started: now.Add(-6 * 24 * time.Hour),
			RestartPolicy: "unless-stopped",
			Ports: []container.Port{
				{HostPort: 443, ContainerPort: 443, Protocol: "tcp"},
				{HostPort: 80, ContainerPort: 80, Protocol: "tcp"},
			},
			Networks: []string{"shopfront_default"},
			Mounts:   []string{"shopfront_static"},
			Labels:   composeLabels("shopfront", "web", projectDir),
		},
		{
			ID: "b71e4d5c9a08", Name: "shopfront-api", Target: dockerTarget,
			Image: "shopfront/api:2.4.1", Command: "/app/server",
			State: container.StateRunning, Status: "Up 6 days (unhealthy)",
			Health: container.HealthUnhealthy, Uptime: "6 days",
			Created: now.Add(-9 * 24 * time.Hour), Started: now.Add(-6 * 24 * time.Hour),
			RestartPolicy: "unless-stopped",
			Ports:         []container.Port{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 8080, Protocol: "tcp"}},
			Networks:      []string{"shopfront_default"},
			Labels:        composeLabels("shopfront", "api", projectDir),
		},
		{
			ID: "c04a8e11f6b2", Name: "shopfront-db", Target: dockerTarget,
			Image: "postgres:17", Command: "docker-entrypoint.sh postgres",
			State: container.StateRunning, Status: "Up 6 days (healthy)",
			Health: container.HealthHealthy, Uptime: "6 days",
			Created: now.Add(-9 * 24 * time.Hour), Started: now.Add(-6 * 24 * time.Hour),
			RestartPolicy: "unless-stopped",
			Networks:      []string{"shopfront_default"},
			Mounts:        []string{"shopfront_pgdata"},
			Labels:        composeLabels("shopfront", "db", projectDir),
		},
		{
			ID: "5d2f77e0c913", Name: "nightly-backup", Target: dockerTarget,
			Image: "restic/restic:0.17.3", Command: "restic backup /data",
			State: container.StateExited, Status: "Exited (137) 9 hours ago",
			Created:  now.Add(-31 * 24 * time.Hour),
			Started:  now.Add(-10 * time.Hour),
			ExitCode: 137, ExitCodeKnown: true,
			RestartPolicy: "no",
			Mounts:        []string{"backup_cache"},
		},
		{
			ID: "9ab3c6d41e57", Name: "metrics-agent", Target: dockerTarget,
			Image: "telemetry/agent:1.9.0", Command: "/agent --config /etc/agent.yaml",
			State: container.StateRestarting, Status: "Restarting (1) 12 seconds ago",
			Created:  now.Add(-2 * 24 * time.Hour),
			Started:  now.Add(-12 * time.Second),
			ExitCode: 1, ExitCodeKnown: true,
			RestartCount:  148,
			RestartPolicy: "always",
			Networks:      []string{"bridge"},
		},
		{
			ID: "e8107b3a5cd6", Name: "registry-mirror", Target: podmanUser,
			Image: "docker.io/library/registry:2", Command: "/entrypoint.sh /etc/distribution/config.yml",
			State: container.StateRunning, Status: "Up 3 hours",
			Uptime:  "3 hours",
			Created: now.Add(-64 * 24 * time.Hour), Started: now.Add(-3 * time.Hour),
			RestartPolicy: "always",
			Ports:         []container.Port{{HostPort: 5000, ContainerPort: 5000, Protocol: "tcp"}},
			Networks:      []string{"podman"},
			Mounts:        []string{"registry-data"},
		},
	}

	images := []container.Image{
		{ID: "7c2e91af04d3", Repository: "shopfront/web", Tag: "2.4.1",
			Target: dockerTarget, SizeBytes: 184_000_000, SizeText: "184MB",
			Created: now.Add(-9 * 24 * time.Hour)},
		{ID: "1b6d3fa07c58", Repository: "shopfront/api", Tag: "2.4.1",
			Target: dockerTarget, SizeBytes: 96_400_000, SizeText: "96.4MB",
			Created: now.Add(-9 * 24 * time.Hour)},
		{ID: "d19f4c8b2e70", Repository: "postgres", Tag: "17",
			Target: dockerTarget, SizeBytes: 454_000_000, SizeText: "454MB",
			Created: now.Add(-24 * 24 * time.Hour)},
		{ID: "a0e57c9d3b41", Repository: "restic/restic", Tag: "0.17.3",
			Target: dockerTarget, SizeBytes: 33_100_000, SizeText: "33.1MB",
			Created: now.Add(-40 * 24 * time.Hour)},
		{ID: "6f81d2a4c095", Repository: "telemetry/agent", Tag: "1.9.0",
			Target: dockerTarget, SizeBytes: 61_800_000, SizeText: "61.8MB",
			Created: now.Add(-2 * 24 * time.Hour)},
		// The two a rebuild left behind. Nothing points at them and nothing
		// can: they are what `image prune` is for.
		{ID: "44b09e7c1af6", Repository: "<none>", Tag: "<none>", Dangling: true,
			Target: dockerTarget, SizeBytes: 182_000_000, SizeText: "182MB",
			Created: now.Add(-11 * 24 * time.Hour)},
		{ID: "8e3210b7fd94", Repository: "<none>", Tag: "<none>", Dangling: true,
			Target: dockerTarget, SizeBytes: 95_700_000, SizeText: "95.7MB",
			Created: now.Add(-11 * 24 * time.Hour)},
		{ID: "2a7f6c05e1b8", Repository: "docker.io/library/registry", Tag: "2",
			Target: podmanUser, SizeBytes: 25_400_000, SizeText: "25.4MB",
			Created: now.Add(-64 * 24 * time.Hour)},
	}

	volumes := []container.Volume{
		{Name: "shopfront_pgdata", Driver: "local", Target: dockerTarget,
			Mountpoint: "/var/lib/docker/volumes/shopfront_pgdata/_data"},
		{Name: "shopfront_static", Driver: "local", Target: dockerTarget,
			Mountpoint: "/var/lib/docker/volumes/shopfront_static/_data"},
		{Name: "backup_cache", Driver: "local", Target: dockerTarget,
			Mountpoint: "/var/lib/docker/volumes/backup_cache/_data"},
		{Name: "old_uploads", Driver: "local", Target: dockerTarget,
			Mountpoint: "/var/lib/docker/volumes/old_uploads/_data"},
		{Name: "f3c81a0d5e94b27a", Driver: "local", Target: dockerTarget,
			Anonymous:  true,
			Mountpoint: "/var/lib/docker/volumes/f3c81a0d5e94b27a/_data"},
		{Name: "registry-data", Driver: "local", Target: podmanUser,
			Mountpoint: "/home/you/.local/share/containers/storage/volumes/registry-data/_data"},
	}

	networks := []container.Network{
		{ID: "0af3b2c81d54", Name: "bridge", Driver: "bridge",
			Target: dockerTarget, Builtin: true},
		{ID: "b71c04ea9236", Name: "host", Driver: "host",
			Target: dockerTarget, Builtin: true},
		{ID: "5e2d8a017fbc", Name: "none", Driver: "null",
			Target: dockerTarget, Builtin: true},
		{ID: "9d40c7b23e18", Name: "shopfront_default", Driver: "bridge",
			Target: dockerTarget},
		{ID: "c81b3f0d67a4", Name: "legacy_net", Driver: "bridge",
			Target: dockerTarget},
		{ID: "2f259bab93aa", Name: "podman", Driver: "bridge",
			Target: podmanUser, Builtin: true},
	}

	engines := []container.EngineInfo{
		{
			Target: dockerTarget, Available: true, Installed: true,
			Version: "27.3.1", ServerVersion: "27.3.1",
			StorageDriver: "overlay2", CgroupVersion: "2",
			Root:       "/var/lib/docker",
			Containers: 5, Running: 3, Stopped: 2, Images: 7,
			Compose: true, ComposeVersion: "Docker Compose version v2.29.7",
			Disk: []container.DiskRow{
				{Type: "Images", Total: "7", Active: "5", Size: "1.107GB", Reclaimable: "277.7MB (25%)"},
				{Type: "Containers", Total: "5", Active: "3", Size: "12.4MB", Reclaimable: "8.1MB (65%)"},
				{Type: "Local Volumes", Total: "5", Active: "3", Size: "3.204GB", Reclaimable: "812MB (25%)"},
				{Type: "Build Cache", Total: "38", Active: "0", Size: "2.71GB", Reclaimable: "2.71GB"},
			},
		},
		{
			Target: podmanUser, Available: true, Installed: true,
			Version: "5.2.3", ServerVersion: "5.2.3",
			StorageDriver: "overlay", CgroupVersion: "2", Rootless: true,
			Root:             "/home/you/.local/share/containers/storage",
			SearchRegistries: []string{"registry.fedoraproject.org", "docker.io"},
			Containers:       1, Running: 1, Images: 1,
			Compose:        true,
			ComposeVersion: "Docker Compose version v2.29.7",
			Quadlets: []container.QuadletUnit{
				{Path: "/home/you/.config/containers/systemd/registry-mirror.container",
					Name: "registry-mirror.service", Scope: container.ScopeUser},
				{Path: "/home/you/.config/containers/systemd/registry-data.volume",
					Name: "registry-data-volume.service", Scope: container.ScopeUser},
			},
		},
		{
			// Root's Podman is on this sample machine and was not read, which
			// is the ordinary outcome on a laptop: `sudo -n` needs a password
			// and this tool never asks for one.
			Target: podmanSystem, Installed: true, Escalated: true,
			Detail: "root's containers were not read: `sudo -n` needs a " +
				"password here, and this tool never prompts for one",
		},
	}

	return container.Model{
		Backend:    "host",
		Engines:    engines,
		Containers: applyLabels(containers),
		Images:     images,
		Volumes:    volumes,
		Networks:   networks,
	}
}

// sampleDetail is what the demo's detail screen shows, per container. The
// unhealthy one carries a health log, because that log is the answer to the
// question the colour raises.
func sampleDetail(c container.Container, now time.Time) container.Detail {
	detail := container.Detail{
		Entrypoint: strings.Fields(c.Command),
		Limits: container.Limits{
			MemoryBytes: 536_870_912,
			NanoCPUs:    1_500_000_000,
			PidsLimit:   512,
		},
		Env: podman.ParseEnv([]string{
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin",
			"TZ=Europe/Lisbon",
			"DATABASE_URL=postgres://shopfront@db:5432/shopfront",
			"DATABASE_PASSWORD=hunter2-but-not-really",
			"SESSION_SECRET=a-long-string-nobody-should-read",
			"STRIPE_API_KEY=sk_live_not_a_real_one",
		}),
		Mounts: []container.Mount{
			{Type: "volume", Source: "shopfront_pgdata",
				Destination: "/var/lib/postgresql/data", RW: true},
			{Type: "bind", Source: "/srv/shopfront/config",
				Destination: "/etc/app", Mode: "ro", RW: false},
		},
		Networks: []container.NetworkAttachment{
			{Name: "shopfront_default", IPAddress: "172.19.0.4",
				Gateway: "172.19.0.1", MacAddress: "02:42:ac:13:00:04"},
		},
	}
	if c.Running() {
		detail.Stats = container.Stats{
			CPUPercent: "2.41%", MemUsage: "218.4MiB / 512MiB",
			MemPercent: "42.66%", NetIO: "1.31GB / 884MB",
			BlockIO: "612MB / 3.94GB", PIDs: "17",
		}
	} else {
		detail.StatsErr = c.Name + " is not running, so there is nothing to sample"
	}
	if c.Health == container.HealthNone {
		return detail
	}
	detail.Health = container.HealthReport{Status: c.Health}
	if c.Unhealthy() {
		detail.Health.FailingStreak = 41
		detail.Health.Log = []container.HealthEntry{
			{Start: now.Add(-90 * time.Second), End: now.Add(-89 * time.Second),
				ExitCode: 1,
				Output:   "curl: (7) Failed to connect to localhost port 8080 after 0 ms: Connection refused"},
			{Start: now.Add(-60 * time.Second), End: now.Add(-59 * time.Second),
				ExitCode: 1,
				Output:   "curl: (7) Failed to connect to localhost port 8080 after 0 ms: Connection refused"},
			{Start: now.Add(-30 * time.Second), End: now.Add(-29 * time.Second),
				ExitCode: 1,
				Output:   "curl: (7) Failed to connect to localhost port 8080 after 0 ms: Connection refused"},
		}
		return detail
	}
	detail.Health.Log = []container.HealthEntry{
		{Start: now.Add(-30 * time.Second), End: now.Add(-30 * time.Second),
			ExitCode: 0, Output: ""},
	}
	return detail
}

// sampleLogs is what the demo's log pane shows. Each container gets lines that
// explain its own row, which is the whole reason the pane is next to the list.
func sampleLogs(c container.Container, opts container.LogOptions,
	now time.Time) string {
	lines := logLinesFor(c)
	if opts.Tail > 0 && opts.Tail < len(lines) {
		lines = lines[len(lines)-opts.Tail:]
	}
	if !opts.Timestamps {
		return strings.Join(lines, "\n")
	}
	stamped := make([]string, 0, len(lines))
	for i, line := range lines {
		at := now.Add(time.Duration(i-len(lines)) * 7 * time.Second)
		stamped = append(stamped,
			at.Format("2006-01-02T15:04:05.000000000Z07:00")+" "+line)
	}
	return strings.Join(stamped, "\n")
}

// logLinesFor is the canned log of one sample container.
func logLinesFor(c container.Container) []string {
	switch c.Name {
	case "shopfront-api":
		return []string{
			`{"level":"info","msg":"listening","addr":"0.0.0.0:8080"}`,
			`{"level":"info","msg":"connected to postgres","host":"db"}`,
			`{"level":"warn","msg":"pool at capacity","in_use":20,"max":20}`,
			`{"level":"error","msg":"query timed out after 30s","query":"orders.by_customer"}`,
			`{"level":"error","msg":"health endpoint unregistered: listener closed"}`,
			`{"level":"error","msg":"query timed out after 30s","query":"orders.by_customer"}`,
		}
	case "nightly-backup":
		return []string{
			"open repository",
			"lock repository",
			"load index files",
			"start scan on /data",
			"scan finished in 41.220s: 182431 files, 61.804 GiB",
			"Killed",
		}
	case "metrics-agent":
		return []string{
			`level=info msg="agent starting" version=1.9.0`,
			`level=error msg="cannot read /etc/agent.yaml: no such file or directory"`,
			`level=fatal msg="exiting"`,
		}
	case "registry-mirror":
		return []string{
			`time="09:41:02Z" level=info msg="redis not configured"`,
			`time="09:41:02Z" level=info msg="listening on [::]:5000"`,
			`time="09:44:18Z" level=info msg="response completed" http.request.uri="/v2/library/alpine/manifests/3.20"`,
		}
	default:
		return []string{
			`10.0.2.1 - - [30/Aug/2026:09:44:51 +0000] "GET / HTTP/2.0" 200 5182`,
			`10.0.2.1 - - [30/Aug/2026:09:44:52 +0000] "GET /assets/app.css HTTP/2.0" 200 18442`,
			`10.0.2.1 - - [30/Aug/2026:09:44:58 +0000] "POST /api/cart HTTP/2.0" 502 166`,
		}
	}
}

// verify at compile time that both backends satisfy the interface the UI
// depends on. It is cheaper than discovering a missing method from a demo that
// will not start.
var (
	_ container.Backend = (*Real)(nil)
	_ container.Backend = (*Fake)(nil)
	_ fmt.Stringer      = container.Target{}
)

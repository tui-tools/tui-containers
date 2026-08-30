package docker

import (
	"strings"
	"testing"

	"github.com/tui-tools/tui-containers/internal/container"
)

// These tests assert on the exact command line the confirm dialog will show.
// That is the whole of the family's promise — the argv previewed is the argv
// run — so an argv changing here is a change to what a user was told, and it
// has to be a change somebody made on purpose.

// running and stopped are the two shapes of container the builders branch on.
func running() container.Container {
	return container.Container{
		ID: "3f9c1a2b7d40", Name: "shopfront-web", Target: Target,
		Image: "shopfront/web:2.4.1", State: container.StateRunning,
	}
}

func stopped() container.Container {
	c := running()
	c.State, c.ExitCode, c.ExitCodeKnown = container.StateExited, 137, true
	return c
}

func TestLifecycleArgv(t *testing.T) {
	want := map[string]string{
		"start":   "docker start 3f9c1a2b7d40",
		"stop":    "docker stop 3f9c1a2b7d40",
		"restart": "docker restart 3f9c1a2b7d40",
		"kill":    "docker kill 3f9c1a2b7d40",
		"pause":   "docker pause 3f9c1a2b7d40",
		"unpause": "docker unpause 3f9c1a2b7d40",
	}
	for verb, expected := range want {
		cmd, err := BuildLifecycle(running(), verb)
		if err != nil {
			t.Fatalf("BuildLifecycle(%q): %v", verb, err)
		}
		if got := cmd.String(); got != expected {
			t.Errorf("%s = %q, want %q", verb, got, expected)
		}
		if cmd.Description == "" {
			t.Errorf("%s has no description for the dialog", verb)
		}
	}
	if _, err := BuildLifecycle(running(), "exec"); err == nil {
		t.Error("a verb this tool does not offer was built")
	}
}

// TestRemoveRefusesARunningContainerWithoutForce is the guard the d key rests
// on: `docker rm -f` kills a container before removing it, and that is a
// different act from removing something that already stopped.
func TestRemoveRefusesARunningContainerWithoutForce(t *testing.T) {
	if _, err := BuildRemove(running(), false); err == nil {
		t.Fatal("a running container was removed without force")
	}
	forced, err := BuildRemove(running(), true)
	if err != nil {
		t.Fatalf("BuildRemove(force): %v", err)
	}
	if got := forced.String(); got != "docker rm -f 3f9c1a2b7d40" {
		t.Errorf("forced removal = %q", got)
	}
	plain, err := BuildRemove(stopped(), false)
	if err != nil {
		t.Fatalf("BuildRemove(stopped): %v", err)
	}
	if got := plain.String(); got != "docker rm 3f9c1a2b7d40" {
		t.Errorf("plain removal = %q", got)
	}
	if !plain.Destructive || !forced.Destructive {
		t.Error("a removal is not marked destructive")
	}
}

func TestUpdateRestartArgv(t *testing.T) {
	for _, policy := range append(RestartPolicies, "on-failure:5") {
		cmd, err := BuildUpdateRestart(stopped(), policy)
		if err != nil {
			t.Fatalf("BuildUpdateRestart(%q): %v", policy, err)
		}
		want := "docker update --restart=" + policy + " 3f9c1a2b7d40"
		if got := cmd.String(); got != want {
			t.Errorf("policy %q = %q, want %q", policy, got, want)
		}
	}
	for _, bad := range []string{"", "maybe", "always;rm -rf /", "on-failure:x"} {
		if _, err := BuildUpdateRestart(stopped(), bad); err == nil {
			t.Errorf("%q was accepted as a restart policy", bad)
		}
	}
}

// TestPruneArgvVariants: each variant is a different command line, and the
// difference between them is what gets deleted. They are asserted here because
// a picker that produced the wrong one would be silent about it.
func TestPruneArgvVariants(t *testing.T) {
	tests := []struct {
		got  container.Command
		want string
	}{
		{BuildPruneImages(false), "docker image prune -f"},
		{BuildPruneImages(true), "docker image prune -a -f"},
		{BuildPruneVolumes(), "docker volume prune -f"},
		{BuildPruneNetworks(), "docker network prune -f"},
		{BuildSystemPrune(container.PruneOptions{}), "docker system prune -f"},
		{BuildSystemPrune(container.PruneOptions{All: true}),
			"docker system prune -f -a"},
		{BuildSystemPrune(container.PruneOptions{Volumes: true}),
			"docker system prune -f --volumes"},
		{BuildSystemPrune(container.PruneOptions{All: true, Volumes: true}),
			"docker system prune -f -a --volumes"},
	}
	for _, test := range tests {
		if got := test.got.String(); got != test.want {
			t.Errorf("prune = %q, want %q", got, test.want)
		}
		if !test.got.Destructive {
			t.Errorf("%q is not marked destructive", test.want)
		}
	}
}

// TestRemoveImageRefusesOneInUse: the image is what a container was created
// from, and removing it means that container cannot be started again.
func TestRemoveImageRefusesOneInUse(t *testing.T) {
	image := container.Image{ID: "d19f4c8b2e70", Repository: "postgres",
		Tag: "17", Target: Target, UsedBy: 2}
	if _, err := BuildRemoveImage(image, false); err == nil {
		t.Fatal("an image two containers use was removed")
	}
	forced, err := BuildRemoveImage(image, true)
	if err != nil {
		t.Fatalf("BuildRemoveImage(force): %v", err)
	}
	if got := forced.String(); got != "docker rmi -f postgres:17" {
		t.Errorf("forced = %q", got)
	}

	image.UsedBy = 0
	plain, err := BuildRemoveImage(image, false)
	if err != nil {
		t.Fatalf("BuildRemoveImage: %v", err)
	}
	if got := plain.String(); got != "docker rmi postgres:17" {
		t.Errorf("plain = %q", got)
	}

	// A dangling image has no name to remove it by, so the id is used.
	dangling := container.Image{ID: "44b09e7c1af6", Repository: "<none>",
		Tag: "<none>", Dangling: true, Target: Target}
	cmd, err := BuildRemoveImage(dangling, false)
	if err != nil {
		t.Fatalf("BuildRemoveImage(dangling): %v", err)
	}
	if got := cmd.String(); got != "docker rmi 44b09e7c1af6" {
		t.Errorf("dangling = %q", got)
	}
}

// TestRemoveVolumeAndNetworkGuards: the engine would refuse these too, but the
// refusal here can name what is still using them.
func TestRemoveVolumeAndNetworkGuards(t *testing.T) {
	if _, err := BuildRemoveVolume(container.Volume{Name: "pgdata",
		Target: Target, InUse: true}); err == nil {
		t.Error("a mounted volume was removed")
	}
	cmd, err := BuildRemoveVolume(container.Volume{Name: "pgdata", Target: Target})
	if err != nil {
		t.Fatalf("BuildRemoveVolume: %v", err)
	}
	if got := cmd.String(); got != "docker volume rm pgdata" {
		t.Errorf("volume rm = %q", got)
	}

	if _, err := BuildRemoveNetwork(container.Network{Name: "bridge",
		Target: Target, Builtin: true}); err == nil {
		t.Error("a network Docker owns was removed")
	}
	if _, err := BuildRemoveNetwork(container.Network{Name: "shopfront_default",
		Target: Target, InUse: true}); err == nil {
		t.Error("a network with a container on it was removed")
	}
	cmd, err = BuildRemoveNetwork(container.Network{Name: "legacy_net",
		Target: Target})
	if err != nil {
		t.Fatalf("BuildRemoveNetwork: %v", err)
	}
	if got := cmd.String(); got != "docker network rm legacy_net" {
		t.Errorf("network rm = %q", got)
	}
}

// TestComposeArgvUsesTheLabels: the project name, the directory and the files
// all come from what Compose itself wrote on the containers, so the command is
// exact rather than a guess about where this tool happens to be running.
func TestComposeArgvUsesTheLabels(t *testing.T) {
	project := container.Project{Name: "shopfront", Target: Target,
		WorkingDir: "/srv/shopfront"}
	files := []string{"/srv/shopfront/compose.yaml"}
	want := map[string]string{
		"up": "docker compose --project-name shopfront --project-directory " +
			"/srv/shopfront -f /srv/shopfront/compose.yaml up -d",
		"down": "docker compose --project-name shopfront --project-directory " +
			"/srv/shopfront -f /srv/shopfront/compose.yaml down",
		"pull": "docker compose --project-name shopfront --project-directory " +
			"/srv/shopfront -f /srv/shopfront/compose.yaml pull",
	}
	for verb, expected := range want {
		cmd, err := BuildCompose(project, files, verb)
		if err != nil {
			t.Fatalf("BuildCompose(%q): %v", verb, err)
		}
		if got := cmd.String(); got != expected {
			t.Errorf("%s = %q, want %q", verb, got, expected)
		}
	}
	if _, err := BuildCompose(project, files, "restart"); err == nil {
		t.Error("a compose verb this tool does not offer was built")
	}
}

// TestComposeRefusesAProjectWithNoDirectory: without the working_dir label
// there is nowhere to run compose, and running it in whatever directory this
// tool happens to be in would be a command about a different project.
func TestComposeRefusesAProjectWithNoDirectory(t *testing.T) {
	if _, err := BuildCompose(container.Project{Name: "shopfront",
		Target: Target}, nil, "up"); err == nil {
		t.Fatal("a project with no working directory was driven")
	}
	if _, err := BuildCompose(container.Project{Name: "shopfront",
		Target: Target, WorkingDir: "relative/path"}, nil, "up"); err == nil {
		t.Fatal("a relative working directory was accepted")
	}
}

// TestReferencesAreChecked: everything that reaches an argv comes from the
// engine's own output, and it is checked anyway.
func TestReferencesAreChecked(t *testing.T) {
	for _, bad := range []string{"", "-rf", "a b", "a;b", "a\nb", "--force"} {
		c := running()
		c.ID, c.Name = bad, bad
		if _, err := BuildLifecycle(c, "stop"); err == nil {
			t.Errorf("%q was accepted as a container reference", bad)
		}
	}
}

// TestLogsArgv covers the three things the pane can ask for, and the one it
// will not: a --since window it cannot parse.
func TestLogsArgv(t *testing.T) {
	argv, err := LogsArgv("3f9c1a2b7d40",
		container.LogOptions{Tail: 200, Timestamps: true})
	if err != nil {
		t.Fatalf("LogsArgv: %v", err)
	}
	want := "docker logs --tail 200 --timestamps 3f9c1a2b7d40"
	if got := strings.Join(argv, " "); got != want {
		t.Errorf("logs = %q, want %q", got, want)
	}

	argv, err = LogsArgv("3f9c1a2b7d40",
		container.LogOptions{Tail: 50, Since: "15m"})
	if err != nil {
		t.Fatalf("LogsArgv(since): %v", err)
	}
	want = "docker logs --tail 50 --since 15m 3f9c1a2b7d40"
	if got := strings.Join(argv, " "); got != want {
		t.Errorf("logs = %q, want %q", got, want)
	}

	// A tail outside the sane range falls back rather than being passed on.
	argv, _ = LogsArgv("3f9c1a2b7d40", container.LogOptions{Tail: -1})
	if got := strings.Join(argv, " "); got != "docker logs --tail 200 3f9c1a2b7d40" {
		t.Errorf("a nonsense tail produced %q", got)
	}
	for _, bad := range []string{"yesterday", "15", "1 h", "-5m"} {
		if _, err := LogsArgv("3f9c1a2b7d40",
			container.LogOptions{Since: bad}); err == nil {
			t.Errorf("%q was accepted as a time window", bad)
		}
	}
}

// TestFormatShorthandFallsBackBelow23: the shorthand is what a reader
// recognises from the preview, and the template is what an old engine
// understands. Both produce the same output.
func TestFormatShorthandFallsBackBelow23(t *testing.T) {
	if got := strings.Join(PSArgv(true), " "); got != "docker ps -a --format json" {
		t.Errorf("ps with the shorthand = %q", got)
	}
	if got := strings.Join(PSArgv(false), " "); got !=
		"docker ps -a --format {{json .}}" {
		t.Errorf("ps with the template = %q", got)
	}
	for _, argv := range [][]string{
		ImagesArgv(false), VolumesArgv(false), NetworksArgv(false),
		InfoArgv(false),
	} {
		if !strings.Contains(strings.Join(argv, " "), TemplateJSON) {
			t.Errorf("%v does not fall back to the template form", argv)
		}
	}
}

// TestReadsAreReads: nothing this package builds for a read may mutate, and
// nothing it builds for a read may run forever. --no-stream is the second half
// of that: without it `docker stats` never returns.
func TestReadsAreReads(t *testing.T) {
	stats, err := StatsArgv("3f9c1a2b7d40", true)
	if err != nil {
		t.Fatalf("StatsArgv: %v", err)
	}
	joined := strings.Join(stats, " ")
	if !strings.Contains(joined, "--no-stream") {
		t.Errorf("stats = %q, which would never return", joined)
	}
	reads := [][]string{
		PSArgv(true), ImagesArgv(true), VolumesArgv(true), NetworksArgv(true),
		InfoArgv(true), DiskArgv(), ComposeVersionArgv(), VersionArgv(), stats,
	}
	for _, argv := range reads {
		for _, word := range []string{"rm", "rmi", "prune", "kill", "stop",
			"start", "update", "pull", "-f"} {
			for _, arg := range argv[1:] {
				if arg == word {
					t.Errorf("the read %v carries %q", argv, word)
				}
			}
		}
	}
}

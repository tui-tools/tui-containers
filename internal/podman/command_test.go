package podman

import (
	"strings"
	"testing"

	"github.com/tui-tools/tui-containers/internal/container"
)

// These tests assert on the exact command line the confirm dialog will show.
// That is the whole of the family's promise — the argv previewed is the argv
// run — so an argv changing here is a change to what a user was told, and it
// has to be a change somebody made on purpose.

func running() container.Container {
	return container.Container{
		ID: "e8107b3a5cd6", Name: "registry-mirror",
		Target: TargetFor(container.ScopeUser),
		Image:  "docker.io/library/registry:2", State: container.StateRunning,
	}
}

func stopped() container.Container {
	c := running()
	c.State, c.ExitCode, c.ExitCodeKnown = container.StateExited, 0, true
	return c
}

func TestLifecycleArgv(t *testing.T) {
	want := map[string]string{
		"start":   "podman start e8107b3a5cd6",
		"stop":    "podman stop e8107b3a5cd6",
		"restart": "podman restart e8107b3a5cd6",
		"kill":    "podman kill e8107b3a5cd6",
		"pause":   "podman pause e8107b3a5cd6",
		"unpause": "podman unpause e8107b3a5cd6",
	}
	for verb, expected := range want {
		cmd, err := BuildLifecycle(running(), verb)
		if err != nil {
			t.Fatalf("BuildLifecycle(%q): %v", verb, err)
		}
		if got := cmd.String(); got != expected {
			t.Errorf("%s = %q, want %q", verb, got, expected)
		}
	}
}

func TestRemoveRefusesARunningContainerWithoutForce(t *testing.T) {
	if _, err := BuildRemove(running(), false); err == nil {
		t.Fatal("a running container was removed without force")
	}
	forced, err := BuildRemove(running(), true)
	if err != nil {
		t.Fatalf("BuildRemove(force): %v", err)
	}
	if got := forced.String(); got != "podman rm -f e8107b3a5cd6" {
		t.Errorf("forced removal = %q", got)
	}
	plain, err := BuildRemove(stopped(), false)
	if err != nil {
		t.Fatalf("BuildRemove: %v", err)
	}
	if got := plain.String(); got != "podman rm e8107b3a5cd6" {
		t.Errorf("plain removal = %q", got)
	}
}

// TestUpdateRestartAcceptsPodmansNever: Podman takes "never" as a synonym for
// "no", and people who write Quadlet files are used to writing it.
func TestUpdateRestartAcceptsPodmansNever(t *testing.T) {
	cmd, err := BuildUpdateRestart(stopped(), "never")
	if err != nil {
		t.Fatalf("BuildUpdateRestart: %v", err)
	}
	if got := cmd.String(); got != "podman update --restart=never e8107b3a5cd6" {
		t.Errorf("policy = %q", got)
	}
	for _, bad := range []string{"", "maybe", "always;rm -rf /"} {
		if _, err := BuildUpdateRestart(stopped(), bad); err == nil {
			t.Errorf("%q was accepted as a restart policy", bad)
		}
	}
}

func TestPruneArgvVariants(t *testing.T) {
	tests := []struct {
		got  container.Command
		want string
	}{
		{BuildPruneImages(false), "podman image prune -f"},
		{BuildPruneImages(true), "podman image prune -a -f"},
		{BuildPruneVolumes(), "podman volume prune -f"},
		{BuildPruneNetworks(), "podman network prune -f"},
		{BuildSystemPrune(container.PruneOptions{}), "podman system prune -f"},
		{BuildSystemPrune(container.PruneOptions{All: true, Volumes: true}),
			"podman system prune -f -a --volumes"},
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

// TestDefaultNetworkCannotBeRemoved: `podman` is the network Podman makes for
// itself, and it is not one anybody can delete.
func TestDefaultNetworkCannotBeRemoved(t *testing.T) {
	if _, err := BuildRemoveNetwork(container.Network{Name: DefaultNetwork,
		Target: TargetFor(container.ScopeUser), Builtin: true}); err == nil {
		t.Fatal("the default network was removed")
	}
}

// TestAutoUpdateIsOnlyEverADryRun: the real auto-update pulls and restarts
// several units at once, chosen by a label rather than by the person pressing
// the key, so only the dry run is built here.
func TestAutoUpdateIsOnlyEverADryRun(t *testing.T) {
	cmd := BuildAutoUpdateDryRun()
	if got := cmd.String(); got != "podman auto-update --dry-run" {
		t.Errorf("auto-update = %q", got)
	}
	if cmd.Destructive {
		t.Errorf("a dry run is marked destructive")
	}
}

// TestComposeArgvUsesTheLabels: `podman compose` hands its arguments straight
// through to whichever provider it found, so they are Docker's arguments — and
// the project, the directory and the files still come from the labels.
func TestComposeArgvUsesTheLabels(t *testing.T) {
	project := container.Project{Name: "shopfront",
		Target: TargetFor(container.ScopeUser), WorkingDir: "/srv/shopfront"}
	files := []string{"/srv/shopfront/compose.yaml"}
	cmd, err := BuildCompose(project, files, "up")
	if err != nil {
		t.Fatalf("BuildCompose: %v", err)
	}
	want := "podman compose --project-name shopfront --project-directory " +
		"/srv/shopfront -f /srv/shopfront/compose.yaml up -d"
	if got := cmd.String(); got != want {
		t.Errorf("compose up = %q, want %q", got, want)
	}
	if _, err := BuildCompose(container.Project{Name: "shopfront",
		Target: project.Target}, nil, "up"); err == nil {
		t.Fatal("a project with no working directory was driven")
	}
}

func TestReferencesAreChecked(t *testing.T) {
	for _, bad := range []string{"", "-rf", "a b", "a;b", "a\nb", "--force"} {
		c := running()
		c.ID, c.Name = bad, bad
		if _, err := BuildLifecycle(c, "stop"); err == nil {
			t.Errorf("%q was accepted as a container reference", bad)
		}
	}
}

func TestLogsArgv(t *testing.T) {
	argv, err := LogsArgv("e8107b3a5cd6",
		container.LogOptions{Tail: 200, Timestamps: true, Since: "1h"})
	if err != nil {
		t.Fatalf("LogsArgv: %v", err)
	}
	want := "podman logs --tail 200 --timestamps --since 1h e8107b3a5cd6"
	if got := strings.Join(argv, " "); got != want {
		t.Errorf("logs = %q, want %q", got, want)
	}
	for _, bad := range []string{"yesterday", "15", "-5m"} {
		if _, err := LogsArgv("e8107b3a5cd6",
			container.LogOptions{Since: bad}); err == nil {
			t.Errorf("%q was accepted as a time window", bad)
		}
	}
}

// TestInspectNamesTheType: Podman's inspect will also answer about an image or
// a volume with the same name, and a tool that let the engine choose would
// occasionally show a reader something else entirely.
func TestInspectNamesTheType(t *testing.T) {
	argv, err := InspectArgv("e8107b3a5cd6")
	if err != nil {
		t.Fatalf("InspectArgv: %v", err)
	}
	want := "podman inspect --type container e8107b3a5cd6"
	if got := strings.Join(argv, " "); got != want {
		t.Errorf("inspect = %q, want %q", got, want)
	}
}

// TestReadsAreReads: nothing this package builds for a read may mutate, and
// nothing it builds for a read may run forever.
func TestReadsAreReads(t *testing.T) {
	stats, err := StatsArgv("e8107b3a5cd6")
	if err != nil {
		t.Fatalf("StatsArgv: %v", err)
	}
	if !strings.Contains(strings.Join(stats, " "), "--no-stream") {
		t.Errorf("stats would never return: %v", stats)
	}
	inspect, _ := InspectArgv("e8107b3a5cd6")
	reads := [][]string{
		PSArgv(), ImagesArgv(), VolumesArgv(), NetworksArgv(), InfoArgv(),
		DiskArgv(), ComposeVersionArgv(), VersionArgv(), stats, inspect,
	}
	for _, argv := range reads {
		for _, word := range []string{"rm", "rmi", "prune", "kill", "stop",
			"start", "update", "pull", "auto-update", "-f"} {
			for _, arg := range argv[1:] {
				if arg == word {
					t.Errorf("the read %v carries %q", argv, word)
				}
			}
		}
	}
}

// TestBuildCreateVolumeAndNetwork: two nouns, the same three rules, and a
// refusal that names which one was broken.
func TestBuildCreateVolumeAndNetwork(t *testing.T) {
	volume, err := BuildCreateVolume(container.VolumeSpec{Name: "pgdata", Driver: "local"})
	if err != nil {
		t.Fatalf("BuildCreateVolume: %v", err)
	}
	if got, want := volume.String(), Bin+" volume create --driver local pgdata"; got != want {
		t.Errorf("volume = %q, want %q", got, want)
	}

	network, err := BuildCreateNetwork(container.NetworkSpec{Name: "edge", Driver: "bridge"})
	if err != nil {
		t.Fatalf("BuildCreateNetwork: %v", err)
	}
	if got, want := network.String(), Bin+" network create --driver bridge edge"; got != want {
		t.Errorf("network = %q, want %q", got, want)
	}

	// A driver is optional: without one the engine picks its own default, and
	// the command line says so by not carrying the flag.
	bare, err := BuildCreateVolume(container.VolumeSpec{Name: "cache"})
	if err != nil {
		t.Fatalf("BuildCreateVolume without a driver: %v", err)
	}
	if got, want := bare.String(), Bin+" volume create cache"; got != want {
		t.Errorf("bare volume = %q, want %q", got, want)
	}

	for _, name := range []string{"", "-rm", "a name", "../escape"} {
		if _, err := BuildCreateVolume(container.VolumeSpec{Name: name}); err == nil {
			t.Errorf("%q was accepted as a volume name", name)
		}
		if _, err := BuildCreateNetwork(container.NetworkSpec{Name: name}); err == nil {
			t.Errorf("%q was accepted as a network name", name)
		}
	}
}

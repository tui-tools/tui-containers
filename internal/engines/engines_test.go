package engines

import (
	"context"
	"strings"
	"testing"

	"github.com/tui-tools/tui-containers/internal/container"
	"github.com/tui-tools/tui-containers/internal/docker"
	"github.com/tui-tools/tui-containers/internal/podman"
)

// load reads the sample machine, which is what every test below is about: it
// is the one model this package can build without a real engine, and it is
// deliberately the awkward shape — two engines, three scopes, a Compose
// project, a failure of each kind.
func load(t *testing.T) (*Fake, container.Model) {
	t.Helper()
	backend := NewFake()
	model, err := backend.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return backend, model
}

// TestTheSampleMachineIsTheOneTheReadmeDescribes. The demo is documented, and
// a demo that quietly stopped matching its description would make every
// screenshot a lie.
func TestTheSampleMachineIsTheOneTheReadmeDescribes(t *testing.T) {
	_, model := load(t)

	if got := len(model.Containers); got != 6 {
		t.Errorf("%d containers, want 6", got)
	}
	if got := len(model.Images); got != 8 {
		t.Errorf("%d images, want 8", got)
	}
	if got := len(model.Dangling()); got != 2 {
		t.Errorf("%d dangling images, want 2", got)
	}
	if got := len(model.Projects); got != 1 {
		t.Fatalf("%d compose projects, want 1", got)
	}
	if project := model.Projects[0]; len(project.Containers) != 3 {
		t.Errorf("the project has %d containers, want 3", len(project.Containers))
	}

	var engines, scopes int
	for _, info := range model.Engines {
		if info.Available {
			engines++
		}
		scopes++
	}
	if scopes != 3 || engines != 2 {
		t.Errorf("%d engine scopes of which %d answered; want 3 and 2",
			scopes, engines)
	}

	// One of each of the four things a reader opens this tool to find.
	var unhealthy, restarting, oom int
	for _, c := range model.Containers {
		switch {
		case c.Unhealthy():
			unhealthy++
		case c.State == container.StateRestarting:
			restarting++
		case c.ExitCodeKnown && c.ExitCode == 137:
			oom++
		}
	}
	if unhealthy != 1 || restarting != 1 || oom != 1 {
		t.Errorf("unhealthy=%d restarting=%d exit137=%d; want one of each",
			unhealthy, restarting, oom)
	}
}

// TestSortPutsWhatIsWrongFirst is the ordering the containers screen depends
// on, and the reason the tool is worth opening on a busy machine.
func TestSortPutsWhatIsWrongFirst(t *testing.T) {
	_, model := load(t)
	var seenOK bool
	for _, c := range model.Containers {
		if !c.Failed() {
			seenOK = true
			continue
		}
		if seenOK {
			t.Errorf("%s needs attention and sorts after a container that does not",
				c.Name)
		}
	}
}

// TestCrossReferenceAgreesWithTheRowsOnScreen: an image counted as unused next
// to a container created from it, or a volume marked free next to the
// container mounting it, would be the worst kind of wrong.
func TestCrossReferenceAgreesWithTheRowsOnScreen(t *testing.T) {
	_, model := load(t)

	for _, image := range model.Images {
		var actual int
		for _, c := range model.Containers {
			if c.Target == image.Target &&
				(c.Image == image.Name() || c.Image == image.ID) {
				actual++
			}
		}
		if image.UsedBy != actual {
			t.Errorf("%s is marked used by %d containers, %d are on screen",
				image.Name(), image.UsedBy, actual)
		}
	}

	for _, volume := range model.Volumes {
		var mounted bool
		for _, c := range model.Containers {
			if c.Target != volume.Target {
				continue
			}
			for _, mount := range c.Mounts {
				if mount == volume.Name {
					mounted = true
				}
			}
		}
		if volume.InUse != mounted {
			t.Errorf("%s: in use = %v, but %v containers mount it",
				volume.Name, volume.InUse, mounted)
		}
	}
}

// TestNamesAreScopedToTheirEngine: two engines with a volume of the same name
// are two volumes, and the cross-reference must not join them.
func TestNamesAreScopedToTheirEngine(t *testing.T) {
	dockerT := docker.Target
	podmanT := podman.TargetFor(container.ScopeUser)
	model := container.Model{
		Containers: []container.Container{
			{ID: "a", Name: "a", Target: dockerT, Image: "alpine:3",
				Mounts: []string{"data"}, Networks: []string{"net"}},
		},
		Images: []container.Image{
			{ID: "i1", Repository: "alpine", Tag: "3", Target: dockerT},
			{ID: "i2", Repository: "alpine", Tag: "3", Target: podmanT},
		},
		Volumes: []container.Volume{
			{Name: "data", Target: dockerT},
			{Name: "data", Target: podmanT},
		},
		Networks: []container.Network{
			{Name: "net", Target: dockerT},
			{Name: "net", Target: podmanT},
		},
	}
	CrossReference(&model)

	if model.Images[0].UsedBy != 1 || model.Images[1].UsedBy != 0 {
		t.Errorf("image use crossed engines: %d and %d",
			model.Images[0].UsedBy, model.Images[1].UsedBy)
	}
	if !model.Volumes[0].InUse || model.Volumes[1].InUse {
		t.Errorf("volume use crossed engines")
	}
	if !model.Networks[0].InUse || model.Networks[1].InUse {
		t.Errorf("network use crossed engines")
	}
}

// TestEveryActionCarriesItsTarget is the routing guarantee: an Action built
// from a row goes to the engine and the scope that row came from, so a command
// previewed against root's Podman cannot run against yours.
func TestEveryActionCarriesItsTarget(t *testing.T) {
	backend, model := load(t)

	for _, c := range model.Containers {
		var actions []container.Action
		for _, build := range []func(container.Container) (container.Action, error){
			backend.BuildStart, backend.BuildStop, backend.BuildRestart,
			backend.BuildKill, backend.BuildPause, backend.BuildUnpause,
			backend.BuildPullImage,
		} {
			// A verb that does not apply to this container's state refuses,
			// which is the point of the guard and not a failure here.
			if action, err := build(c); err == nil {
				actions = append(actions, action)
			}
		}
		if len(actions) == 0 {
			t.Errorf("%s accepts no action at all", c.Name)
		}
		for _, action := range actions {
			if action.Target != c.Target {
				t.Errorf("%s: an action targets %v, the container is on %v",
					c.Name, action.Target, c.Target)
			}
			if len(action.Commands) == 0 {
				t.Errorf("%s: %q has no command", c.Name, action.Title)
			}
			// The engine's own binary is argv[0], which is what routes the
			// command back to the runner that previewed it.
			want := string(c.Target.Engine)
			if got := action.Commands[0].Argv[0]; got != want {
				t.Errorf("%s: argv[0] = %q, want %q", c.Name, got, want)
			}
		}
	}
}

// TestVerbsRefuseTheWrongState: the refusal names the state the container is
// actually in, which is more use than the engine's own message.
func TestVerbsRefuseTheWrongState(t *testing.T) {
	backend, model := load(t)
	byState := map[container.State]container.Container{}
	for _, c := range model.Containers {
		byState[c.State] = c
	}

	if _, err := backend.BuildStart(byState[container.StateRunning]); err == nil {
		t.Error("a running container was started")
	}
	if _, err := backend.BuildStop(byState[container.StateExited]); err == nil {
		t.Error("a stopped container was stopped")
	}
	if _, err := backend.BuildUnpause(byState[container.StateRunning]); err == nil {
		t.Error("a running container was unpaused")
	}
	err := func() error {
		_, err := backend.BuildKill(byState[container.StateExited])
		return err
	}()
	if err == nil {
		t.Error("a stopped container was killed")
	} else if !strings.Contains(err.Error(), "exited") {
		t.Errorf("the refusal does not name the state: %v", err)
	}
}

// TestKillCarriesItsWarning: SIGKILL is not a request, and the dialog has to
// say so before it is the thing that happened.
func TestKillCarriesItsWarning(t *testing.T) {
	backend, model := load(t)
	for _, c := range model.Containers {
		if !c.Running() {
			continue
		}
		action, err := backend.BuildKill(c)
		if err != nil {
			t.Fatalf("BuildKill: %v", err)
		}
		if action.Warning == "" {
			t.Errorf("killing %s carries no warning", c.Name)
		}
		if !strings.Contains(action.Warning, "SIGKILL") {
			t.Errorf("the warning does not name the signal: %q", action.Warning)
		}
		return
	}
	t.Fatal("the sample machine has no running container")
}

// TestDestructiveActionsAreMarked: the dialog paints itself from this, and an
// unmarked prune would look like a read.
func TestDestructiveActionsAreMarked(t *testing.T) {
	backend, model := load(t)
	target := model.Containers[0].Target

	actions := map[string]container.Action{}
	for name, build := range map[string]func() (container.Action, error){
		"prune images":  func() (container.Action, error) { return backend.BuildPruneImages(target, false) },
		"prune all":     func() (container.Action, error) { return backend.BuildPruneImages(target, true) },
		"prune volumes": func() (container.Action, error) { return backend.BuildPruneVolumes(target) },
		"prune nets":    func() (container.Action, error) { return backend.BuildPruneNetworks(target) },
		"system prune":  func() (container.Action, error) { return backend.BuildSystemPrune(target, container.PruneOptions{}) },
	} {
		action, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		actions[name] = action
	}
	for name, action := range actions {
		if !action.Destructive {
			t.Errorf("%s is not marked destructive", name)
		}
		if action.Target != target {
			t.Errorf("%s targets %v, want %v", name, action.Target, target)
		}
	}
	// The volume prune is the one that loses work rather than space, and its
	// warning has to say what "unused" really means.
	if !strings.Contains(actions["prune volumes"].Warning, "Unused") {
		t.Errorf("the volume prune does not explain what unused means: %q",
			actions["prune volumes"].Warning)
	}
}

// TestAutoUpdateIsPodmanOnly: Docker has nothing like it, and offering the key
// there would be offering a command that does not exist.
func TestAutoUpdateIsPodmanOnly(t *testing.T) {
	backend, _ := load(t)
	if _, err := backend.BuildAutoUpdate(docker.Target); err == nil {
		t.Error("auto-update was built for Docker")
	}
	action, err := backend.BuildAutoUpdate(podman.TargetFor(container.ScopeUser))
	if err != nil {
		t.Fatalf("BuildAutoUpdate: %v", err)
	}
	if !strings.Contains(action.Commands[0].String(), "--dry-run") {
		t.Errorf("auto-update is not a dry run: %q", action.Commands[0])
	}
}

// TestComposeUsesTheProjectsOwnLabels: the sample project's directory came
// from a label, and the command has to name it.
func TestComposeUsesTheProjectsOwnLabels(t *testing.T) {
	backend, model := load(t)
	project := model.Projects[0]
	action, err := backend.BuildCompose(project, "up")
	if err != nil {
		t.Fatalf("BuildCompose: %v", err)
	}
	line := action.Commands[0].String()
	if !strings.Contains(line, "--project-directory "+project.WorkingDir) {
		t.Errorf("the command does not name the project directory: %q", line)
	}
	if !strings.Contains(line, "--project-name "+project.Name) {
		t.Errorf("the command does not name the project: %q", line)
	}
}

// TestTheDemoRunsNothing: --demo is where a reader tries the keys, and it must
// stay a machine nothing happens to. Building every action must not run one.
func TestTheDemoRunsNothing(t *testing.T) {
	backend, model := load(t)
	for _, c := range model.Containers {
		_, _ = backend.BuildStop(c)
		_, _ = backend.BuildRemove(c, true)
		_, _ = backend.BuildPullImage(c)
	}
	_, _ = backend.BuildSystemPrune(model.Containers[0].Target,
		container.PruneOptions{All: true, Volumes: true})
	if ran := backend.Ran(); len(ran) != 0 {
		t.Errorf("building %d actions ran %d commands", len(model.Containers), len(ran))
	}
}

// TestThePreviewIsWhatRuns is the family's central promise, checked end to end
// on the demo: the string the dialog showed is built from the same value the
// runner receives.
func TestThePreviewIsWhatRuns(t *testing.T) {
	backend, model := load(t)
	var checked int
	for _, c := range model.Containers {
		action, err := backend.BuildStop(c)
		if err != nil {
			continue
		}
		previewed := backend.Preview(action.Target, action.Commands[0])
		if _, err := backend.Run(context.Background(), action.Target,
			action.Commands[0]); err != nil {
			t.Fatalf("Run: %v", err)
		}
		ran := backend.Ran()
		executed := backend.Preview(action.Target, ran[len(ran)-1])
		if previewed != executed {
			t.Errorf("%s: previewed %q, ran %q", c.Name, previewed, executed)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no container on the sample machine could be stopped")
	}
}

// TestLogsCommandNamesWhatItRead: the pane shows the command line it is
// re-reading, for the same reason every confirm dialog does.
func TestLogsCommandNamesWhatItRead(t *testing.T) {
	backend, model := load(t)
	opts := container.LogOptions{Tail: 200, Timestamps: true}
	for _, c := range model.Containers {
		line := backend.LogsCommand(c, opts)
		if !strings.Contains(line, string(c.Target.Engine)+" logs") {
			t.Errorf("%s: the log command is %q", c.Name, line)
		}
		if !strings.Contains(line, "--tail 200") {
			t.Errorf("%s: the log command does not carry the window: %q",
				c.Name, line)
		}
	}
}

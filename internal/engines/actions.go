package engines

import (
	"fmt"
	"strings"

	"github.com/tui-tools/tui-containers/internal/container"
	"github.com/tui-tools/tui-containers/internal/docker"
	"github.com/tui-tools/tui-containers/internal/podman"
)

// This file is every Action the tool can build, as plain functions.
//
// They are functions rather than methods because both backends need exactly
// these: the real one wraps each in a check that the engine is actually
// reachable, and the demo calls them directly. A demo whose confirm dialogs
// showed a different command line from the real tool's would be teaching the
// wrong thing, and the only way to be sure they cannot diverge is for there to
// be one of each.
//
// Nothing here starts a process. Each function assembles a Command from
// internal/docker or internal/podman, wraps it with the words the dialog needs,
// and returns it.

// one wraps a single command into an Action, taking the command's own
// description as the body and its own destructiveness as the dialog's colour.
func one(target container.Target, title string, cmd container.Command) container.Action {
	return container.Action{
		Title:       title,
		Target:      target,
		Body:        cmd.Description + ".",
		Destructive: cmd.Destructive,
		Commands:    []container.Command{cmd},
	}
}

// buildLifecycle dispatches one of the six state verbs to the engine that owns
// the container.
func buildLifecycle(c container.Container, verb string) (container.Command, error) {
	if c.Target.Engine == container.EnginePodman {
		return podman.BuildLifecycle(c, verb)
	}
	return docker.BuildLifecycle(c, verb)
}

// lifecycleAction is the shared guard and wrapping for the six state verbs.
func lifecycleAction(c container.Container, verb string) (container.Action, error) {
	if err := checkVerb(c, verb); err != nil {
		return container.Action{}, err
	}
	cmd, err := buildLifecycle(c, verb)
	if err != nil {
		return container.Action{}, err
	}
	action := one(c.Target, titleFor(verb, c.Name), cmd)
	switch verb {
	case "kill":
		action.Warning = "SIGKILL is not a request. The process is not asked to " +
			"stop, it is stopped: nothing is flushed, no shutdown handler runs, " +
			"and a database in the middle of a write finds out on the next boot. " +
			"Stopping it with s sends SIGTERM first and waits."
	case "pause":
		action.Body += "\n\nEvery process inside is frozen with the cgroup " +
			"freezer. Connections stay open and time keeps passing, so anything " +
			"talking to it will time out rather than see it go away."
	case "restart":
		action.Body += "\n\nWhatever it was in the middle of is not finished, " +
			"and it starts again from the same image with the same " +
			"configuration — a restart is not an upgrade."
	}
	return action, nil
}

// checkVerb refuses a verb that does not apply to the container's current
// state, in the terms the state is in. It is a better message than the
// engine's, because it can name the state the container is actually in.
func checkVerb(c container.Container, verb string) error {
	switch verb {
	case "start":
		if c.Running() {
			return fmt.Errorf("%s is already running", c.Name)
		}
		if c.State == container.StatePaused {
			return fmt.Errorf("%s is paused, not stopped: unpause it with u", c.Name)
		}
	case "stop", "kill", "pause":
		if !c.Running() {
			return fmt.Errorf("%s is not running (%s)", c.Name, stateWord(c))
		}
	case "unpause":
		if c.State != container.StatePaused {
			return fmt.Errorf("%s is not paused (%s)", c.Name, stateWord(c))
		}
	}
	return nil
}

// stateWord is the engine's own word for what a container is doing, for a
// refusal message.
func stateWord(c container.Container) string {
	if c.State == container.StateUnknown {
		return "the engine did not say what state it is in"
	}
	return string(c.State)
}

// titleFor names a lifecycle action for the dialog.
func titleFor(verb, name string) string {
	titles := map[string]string{
		"start":   "Start ",
		"stop":    "Stop ",
		"restart": "Restart ",
		"kill":    "Kill ",
		"pause":   "Pause ",
		"unpause": "Unpause ",
	}
	return titles[verb] + name
}

// removeAction deletes a container.
func removeAction(c container.Container, force bool) (container.Action, error) {
	var cmd container.Command
	var err error
	if c.Target.Engine == container.EnginePodman {
		cmd, err = podman.BuildRemove(c, force)
	} else {
		cmd, err = docker.BuildRemove(c, force)
	}
	if err != nil {
		return container.Action{}, err
	}
	action := one(c.Target, "Remove "+c.Name, cmd)
	action.Body = "The container goes. Its image stays, and so does anything it " +
		"wrote to a named volume; anything it wrote to its own writable layer " +
		"does not."
	if force {
		action.Warning = c.Name + " is running. It will be killed — SIGKILL, no " +
			"grace period — and then removed."
	}
	return action, nil
}

// updateRestartAction changes a container's restart policy in place.
//
// canUpdate and since come from the caller, because whether this is possible at
// all is a Podman version question: `podman update` arrived carrying only the
// resource limits, and the restart policy was added later. Docker has had it
// since long before the minimum this tool supports.
func updateRestartAction(c container.Container, policy string, canUpdate bool,
	since string) (container.Action, error) {
	var cmd container.Command
	var err error
	if c.Target.Engine == container.EnginePodman {
		if !canUpdate {
			return container.Action{}, fmt.Errorf(
				"this Podman cannot change a restart policy in place: "+
					"`podman update --restart` arrived in %s, and on an older one "+
					"the policy is fixed when the container is created",
				orLater(since))
		}
		cmd, err = podman.BuildUpdateRestart(c, policy)
	} else {
		cmd, err = docker.BuildUpdateRestart(c, policy)
	}
	if err != nil {
		return container.Action{}, err
	}
	action := one(c.Target, "Set "+c.Name+" to restart: "+policy, cmd)
	action.Body = "The policy is what the engine does when the container exits, " +
		"and when the machine boots. It changes now and it survives a reboot; " +
		"the container itself is not restarted by this."
	if policy == "always" || policy == "unless-stopped" {
		action.Body += "\n\nA container that fails on startup and is set to " +
			"restart will loop rather than stop, which is what the restarting " +
			"state on the list means."
	}
	return action, nil
}

// orLater renders a missing version as a phrase rather than a blank.
func orLater(version string) string {
	if version == "" {
		return "a later release"
	}
	return version
}

// pullAction fetches the image a container was created from.
func pullAction(c container.Container) (container.Action, error) {
	if strings.TrimSpace(c.Image) == "" {
		return container.Action{}, fmt.Errorf(
			"%s does not report an image reference to pull", c.Name)
	}
	var cmd container.Command
	var err error
	if c.Target.Engine == container.EnginePodman {
		cmd, err = podman.BuildPull(c.Image)
	} else {
		cmd, err = docker.BuildPull(c.Image)
	}
	if err != nil {
		return container.Action{}, err
	}
	action := one(c.Target, "Pull "+c.Image, cmd)
	action.Body = "This fetches the image into the engine's store. It does not " +
		"touch " + c.Name + ": a running container keeps the image it started " +
		"with until something recreates it."
	action.Warning = "To actually run the new image, recreate the container — " +
		"through its Compose project, its Quadlet unit, or whatever created it. " +
		"This tool does not recreate containers."
	return action, nil
}

// composeAction runs one Compose verb for a project.
func composeAction(p container.Project, verb string) (container.Action, error) {
	var cmd container.Command
	var err error
	if p.Target.Engine == container.EnginePodman {
		cmd, err = podman.BuildCompose(p, podman.ComposeFilesFor(p), verb)
	} else {
		cmd, err = docker.BuildCompose(p, docker.ComposeFilesFor(p), verb)
	}
	if err != nil {
		return container.Action{}, err
	}
	action := one(p.Target, "compose "+verb+" — "+p.Name, cmd)
	action.Body = "The project name, the working directory and the compose files " +
		"are the ones Compose itself wrote onto these containers as labels. " +
		"Nothing here is guessed."
	if verb == "down" {
		action.Warning = "Every container of " + p.Name + " is stopped and " +
			"removed, and so is the network Compose made for it. Named volumes " +
			"are left alone."
	}
	return action, nil
}

// removeImageAction deletes an image.
func removeImageAction(i container.Image, force bool) (container.Action, error) {
	var cmd container.Command
	var err error
	if i.Target.Engine == container.EnginePodman {
		cmd, err = podman.BuildRemoveImage(i, force)
	} else {
		cmd, err = docker.BuildRemoveImage(i, force)
	}
	if err != nil {
		return container.Action{}, err
	}
	action := one(i.Target, "Remove image "+i.Name(), cmd)
	action.Body = "The image goes from this engine's store. Pulling it again is a " +
		"download; building it again is a build."
	return action, nil
}

// pruneImagesAction removes the images nothing points at.
func pruneImagesAction(target container.Target, all bool) container.Action {
	cmd := docker.BuildPruneImages(all)
	if target.Engine == container.EnginePodman {
		cmd = podman.BuildPruneImages(all)
	}
	action := one(target, "Prune images on "+target.String(), cmd)
	action.Body = "Dangling images are the layers a rebuild left behind, which " +
		"nothing points at any more."
	if all {
		action.Warning = "With -a this also removes every image no *existing* " +
			"container was created from — base images a build would have reused, " +
			"and anything pulled ahead of time. Each one comes back as a download."
	}
	return action
}

// removeVolumeAction deletes one named volume.
func removeVolumeAction(v container.Volume) (container.Action, error) {
	var cmd container.Command
	var err error
	if v.Target.Engine == container.EnginePodman {
		cmd, err = podman.BuildRemoveVolume(v)
	} else {
		cmd, err = docker.BuildRemoveVolume(v)
	}
	if err != nil {
		return container.Action{}, err
	}
	action := one(v.Target, "Remove volume "+v.Name, cmd)
	action.Warning = "This deletes the data in the volume. A volume is where a " +
		"container keeps what it is meant to keep — a database's files live in " +
		"one — and nothing here backs it up first."
	return action, nil
}

// pruneVolumesAction removes every volume no container mounts.
func pruneVolumesAction(target container.Target) container.Action {
	cmd := docker.BuildPruneVolumes()
	if target.Engine == container.EnginePodman {
		cmd = podman.BuildPruneVolumes()
	}
	action := one(target, "Prune volumes on "+target.String(), cmd)
	action.Warning = "This is the destructive one. \"Unused\" means no container " +
		"exists that mounts it — not that nobody needs it. A database volume " +
		"whose container was removed an hour ago is unused, and it is the " +
		"database."
	return action
}

// removeNetworkAction deletes one network.
func removeNetworkAction(n container.Network) (container.Action, error) {
	var cmd container.Command
	var err error
	if n.Target.Engine == container.EnginePodman {
		cmd, err = podman.BuildRemoveNetwork(n)
	} else {
		cmd, err = docker.BuildRemoveNetwork(n)
	}
	if err != nil {
		return container.Action{}, err
	}
	return one(n.Target, "Remove network "+n.Name, cmd), nil
}

// pruneNetworksAction removes every network no container is on.
func pruneNetworksAction(target container.Target) container.Action {
	cmd := docker.BuildPruneNetworks()
	if target.Engine == container.EnginePodman {
		cmd = podman.BuildPruneNetworks()
	}
	action := one(target, "Prune networks on "+target.String(), cmd)
	action.Body = "A removed network is recreated by whatever declared it — a " +
		"Compose project makes its own again on the next `up`."
	return action
}

// systemPruneAction is the big one, with both of its choices explicit.
func systemPruneAction(target container.Target,
	opts container.PruneOptions) container.Action {
	cmd := docker.BuildSystemPrune(opts)
	if target.Engine == container.EnginePodman {
		cmd = podman.BuildSystemPrune(opts)
	}
	action := one(target, "System prune on "+target.String(), cmd)
	action.Body = "Every stopped container goes, along with the dangling images " +
		"and the networks nothing is on."
	if opts.All {
		action.Warning = "-a removes every image no running container uses, not " +
			"just the dangling ones."
	}
	if opts.Volumes {
		if action.Warning != "" {
			action.Warning += "\n\n"
		}
		action.Warning += "--volumes removes the unused named volumes, and the " +
			"data in them. This is the flag that loses work rather than space."
	}
	return action
}

// autoUpdateAction previews Podman's auto-update, always as a dry run.
func autoUpdateAction(target container.Target) (container.Action, error) {
	if target.Engine != container.EnginePodman {
		return container.Action{}, fmt.Errorf(
			"auto-update is Podman's; Docker has nothing like it")
	}
	action := one(target, "What auto-update would do on "+target.String(),
		podman.BuildAutoUpdateDryRun())
	action.Body = "Podman's auto-update pulls a new image for every unit labelled " +
		"io.containers.autoupdate and restarts the ones that moved. Only the dry " +
		"run is offered here: it lists what would change and touches nothing."
	action.Destructive = false
	return action, nil
}

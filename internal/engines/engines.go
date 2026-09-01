// Package engines joins the two halves of tui-containers into the one backend
// the UI talks to.
//
// It starts no process of its own. Every command it returns was built by
// internal/docker or internal/podman, and running one is handed straight back
// to whichever of them owns the target: that keeps the exec boundary at two
// packages, and it keeps this file about the only question it is really for —
// which engine, in which scope, owns the row under the cursor, and what that
// means for the key the user just pressed.
//
// The merge is the point of the tool. A machine's containers are split across
// Docker and Podman, and Podman's across two scopes, for reasons that are
// historical and administrative rather than anything to do with the containers
// themselves. Here they are one list, sorted by what a reader wants first: what
// is wrong, then what is running.
package engines

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tui-tools/tui-containers/internal/container"
	"github.com/tui-tools/tui-containers/internal/docker"
	"github.com/tui-tools/tui-containers/internal/podman"
	"github.com/tui-tools/tui-kit/compat"
)

// half is what this package needs from an engine implementation. Both
// internal/docker and internal/podman satisfy it, which is what lets the
// dispatch below be a lookup rather than a switch in every method.
type half interface {
	Describe() string
	Preview(cmd container.Command) string
	Run(ctx context.Context, cmd container.Command) (string, error)
	Probe(ctx context.Context) container.EngineInfo
	Load(ctx context.Context, info *container.EngineInfo) (
		[]container.Container, []container.Image, []container.Volume,
		[]container.Network)
	Inspect(ctx context.Context, c container.Container) (container.Detail, error)
	Logs(ctx context.Context, c container.Container,
		opts container.LogOptions) (string, error)
	LogsCommand(c container.Container, opts container.LogOptions) string
}

// Real drives the machine's real engines. It satisfies container.Backend.
type Real struct {
	// halves is one entry per engine and scope that is present, keyed by
	// target. A machine with Docker and rootless Podman has two; one where the
	// account can also escalate into root's Podman has three.
	halves map[container.Target]half
	// order is the order the targets are reported in, so the screen does not
	// reshuffle itself between reads the way a Go map would.
	order []container.Target
	// podmanHalves keeps the Podman backends under their own type, because two
	// of the capability questions — whether this Podman can update a restart
	// policy, whether it has `podman compose` — are version questions only that
	// type can answer.
	podmanHalves map[container.Target]*podman.Backend
	// engines is what the last probe found, kept so Describe and Capabilities
	// can answer without another read.
	engines []container.EngineInfo
}

// NewReal locates every engine on the machine.
//
// Neither engine is required and neither scope is. A server with Docker and no
// Podman, a workstation with rootless Podman and no daemon at all, a machine
// with both, and a machine with neither are all ordinary, and the model says
// which it found rather than refusing to start.
//
// A machine with neither engine used to be the one failure here, and that was
// wrong in the way this family keeps finding: a server that runs no containers
// is a normal server, not an unreadable one. The honest answer is an empty
// engine list with the reason, which is what every screen and --check are
// already written to render.
//
// caps come from the version probes, so no version number is written into this
// package.
func NewReal(sudoPrefix []string, dockerCaps, podmanCaps compat.Caps) *Real {
	r := &Real{
		halves:       map[container.Target]half{},
		podmanHalves: map[container.Target]*podman.Backend{},
	}

	if docker.Available() {
		if backend, err := docker.New(sudoPrefix, dockerCaps); err == nil {
			r.add(docker.Target, backend)
		}
	}
	if podman.Available() {
		if backend, err := podman.New(container.ScopeUser, sudoPrefix, podmanCaps); err == nil {
			r.add(backend.Target(), backend)
			r.podmanHalves[backend.Target()] = backend
		}
		// The system scope is only built when escalation is configured at all.
		// Without a prefix there is no way to reach root's Podman, and offering
		// a scope that cannot be read is worse than not offering it.
		if len(sudoPrefix) > 0 {
			if backend, err := podman.New(container.ScopeSystem, sudoPrefix, podmanCaps); err == nil {
				r.add(backend.Target(), backend)
				r.podmanHalves[backend.Target()] = backend
			}
		}
	}

	return r
}

// add registers one half under its target.
func (r *Real) add(target container.Target, backend half) {
	r.halves[target] = backend
	r.order = append(r.order, target)
}

// Name identifies the backend. It is the one name the manifest keys the tool
// on; the two engines are named separately in the compatibility block.
func (r *Real) Name() string { return "host" }

// Describe names every engine that answered, for the header. An engine that is
// installed and did not answer is named on the system screen instead, with the
// reason, because a header is not the place for a paragraph.
func (r *Real) Describe() string {
	var parts []string
	for _, info := range r.engines {
		if !info.Available {
			continue
		}
		if backend, ok := r.halves[info.Target]; ok {
			parts = append(parts, backend.Describe())
		}
	}
	if len(parts) == 0 {
		// Nothing installed and something installed that stayed silent are
		// different machines, and the header is the first place a reader looks
		// to tell them apart.
		if len(r.halves) == 0 {
			return "no container engine is installed"
		}
		return "no engine answered"
	}
	return strings.Join(parts, " · ")
}

// Capabilities reports what this machine supports. The answers depend on what
// is here: Compose is offered only when an engine answered `compose version`,
// and auto-update only when a Podman is present at all.
func (r *Real) Capabilities() container.Capabilities {
	caps := container.Capabilities{
		SupportsLifecycle: len(r.halves) > 0,
		SupportsRemove:    len(r.halves) > 0,
		SupportsUpdate:    len(r.halves) > 0,
		SupportsPrune:     len(r.halves) > 0,
		SupportsCreate:    len(r.halves) > 0,
		VolumeDrivers:     docker.VolumeDrivers,
		NetworkDrivers:    docker.NetworkDrivers,
		RestartPolicies:   docker.RestartPolicies,
	}
	for _, info := range r.engines {
		if info.Compose {
			caps.SupportsCompose = true
		}
		if info.Available && info.Target.Engine == container.EnginePodman {
			caps.SupportsAutoUpdate = true
		}
	}
	return caps
}

// Preview renders the exact command line Run will execute for a target.
//
// The target is carried by the Action the dialog is showing, so the preview and
// the execution reach the same engine in the same scope by construction: there
// is no path where a command previewed against root's Podman runs against
// yours.
func (r *Real) Preview(target container.Target, cmd container.Command) string {
	if backend, ok := r.halves[target]; ok {
		return backend.Preview(cmd)
	}
	return cmd.String()
}

// Run executes a previously previewed command against its target.
func (r *Real) Run(ctx context.Context, target container.Target,
	cmd container.Command) (string, error) {
	backend, ok := r.halves[target]
	if !ok {
		return "", fmt.Errorf("engines: %s is not available on this machine",
			target)
	}
	return backend.Run(ctx, cmd)
}

// Load reads every engine and folds the answers into one model.
func (r *Real) Load(ctx context.Context) (container.Model, error) {
	model := container.Model{Backend: r.Name()}

	for _, target := range r.order {
		backend := r.halves[target]
		info := backend.Probe(ctx)
		if info.Available {
			containers, images, volumes, networks := backend.Load(ctx, &info)
			model.Containers = append(model.Containers, containers...)
			model.Images = append(model.Images, images...)
			model.Volumes = append(model.Volumes, volumes...)
			model.Networks = append(model.Networks, networks...)
		}
		model.Engines = append(model.Engines, info)
	}
	r.engines = model.Engines

	// A model in which no engine answered is still the model. Returning an
	// error here threw away the very thing that explains the empty screen —
	// each engine's own reason for not answering — and left the caller with
	// nothing to show but the error text. The engines list carries those
	// reasons; the counts below are all zero, which is a fact and not a
	// failure.
	CrossReference(&model)
	container.SortContainers(model.Containers)
	container.SortImages(model.Images)
	model.Projects = container.GroupProjects(model.Containers)
	sortVolumes(model.Volumes)
	sortNetworks(model.Networks)
	return model, nil
}

// CrossReference fills in the facts that come from comparing the lists rather
// than from any single command: which image a container was created from, and
// which volumes and networks are actually in use.
//
// It is done here rather than asked of the engine because the engine cannot
// answer it cheaply — `docker ps --filter` per image is one process per image —
// and because the answer computed from the rows on screen is the one that
// agrees with the rows on screen. A volume marked unused next to a container
// that mounts it would be the worst kind of wrong.
func CrossReference(model *container.Model) {
	usedImages := map[string]int{}
	usedVolumes := map[string]bool{}
	usedNetworks := map[string]bool{}

	for _, c := range model.Containers {
		usedImages[key(c.Target, c.Image)]++
		for _, mount := range c.Mounts {
			usedVolumes[key(c.Target, mount)] = true
		}
		for _, network := range c.Networks {
			usedNetworks[key(c.Target, network)] = true
		}
	}

	for i := range model.Images {
		image := &model.Images[i]
		// A container names its image by whatever reference it was created
		// with, so both the tagged name and the short id are counted.
		image.UsedBy = usedImages[key(image.Target, image.Name())] +
			usedImages[key(image.Target, image.ID)]
	}
	for i := range model.Volumes {
		model.Volumes[i].InUse = usedVolumes[key(model.Volumes[i].Target,
			model.Volumes[i].Name)]
	}
	for i := range model.Networks {
		model.Networks[i].InUse = usedNetworks[key(model.Networks[i].Target,
			model.Networks[i].Name)]
	}
}

// key scopes a name to its engine, so two engines with a volume of the same
// name are two volumes.
func key(target container.Target, name string) string {
	return string(target.Engine) + "/" + string(target.Scope) + "/" + name
}

// sortVolumes puts the named volumes before the anonymous ones, because an
// anonymous volume is a name nobody chose and a list that opened on forty of
// them would bury the ones a reader recognises.
func sortVolumes(list []container.Volume) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Anonymous != b.Anonymous {
			return !a.Anonymous
		}
		if a.InUse != b.InUse {
			return a.InUse
		}
		return a.Name < b.Name
	})
}

// sortNetworks puts the networks somebody made before the ones the engine made
// for itself.
func sortNetworks(list []container.Network) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Builtin != b.Builtin {
			return !a.Builtin
		}
		if a.InUse != b.InUse {
			return a.InUse
		}
		return a.Name < b.Name
	})
}

// Inspect returns one container in full.
func (r *Real) Inspect(ctx context.Context, c container.Container) (
	container.Detail, error) {
	backend, err := r.half(c.Target)
	if err != nil {
		return container.Detail{}, err
	}
	return backend.Inspect(ctx, c)
}

// Logs returns the container's recent log lines.
func (r *Real) Logs(ctx context.Context, c container.Container,
	opts container.LogOptions) (string, error) {
	backend, err := r.half(c.Target)
	if err != nil {
		return "", err
	}
	return backend.Logs(ctx, c, opts)
}

// LogsCommand renders the command line the log pane is running.
func (r *Real) LogsCommand(c container.Container, opts container.LogOptions) string {
	backend, err := r.half(c.Target)
	if err != nil {
		return ""
	}
	return backend.LogsCommand(c, opts)
}

// half returns the backend that owns a target, or an error naming why none
// does.
func (r *Real) half(target container.Target) (half, error) {
	backend, ok := r.halves[target]
	if !ok {
		return nil, fmt.Errorf("%s is not available on this machine", target)
	}
	return backend, nil
}

// The build methods below are the reachability check and nothing else: the
// Actions themselves are built by the shared functions in actions.go, which the
// demo backend calls too. There is one of each, so the command line the demo
// shows and the one the real tool runs cannot drift apart.

func (r *Real) BuildStart(c container.Container) (container.Action, error) {
	return r.lifecycle(c, "start")
}

func (r *Real) BuildStop(c container.Container) (container.Action, error) {
	return r.lifecycle(c, "stop")
}

func (r *Real) BuildRestart(c container.Container) (container.Action, error) {
	return r.lifecycle(c, "restart")
}

func (r *Real) BuildKill(c container.Container) (container.Action, error) {
	return r.lifecycle(c, "kill")
}

func (r *Real) BuildPause(c container.Container) (container.Action, error) {
	return r.lifecycle(c, "pause")
}

func (r *Real) BuildUnpause(c container.Container) (container.Action, error) {
	return r.lifecycle(c, "unpause")
}

// lifecycle checks the engine is reachable, then builds the verb.
func (r *Real) lifecycle(c container.Container, verb string) (container.Action, error) {
	if err := r.reachable(c.Target); err != nil {
		return container.Action{}, err
	}
	return lifecycleAction(c, verb)
}

// BuildRemove deletes a container.
func (r *Real) BuildRemove(c container.Container, force bool) (container.Action, error) {
	if err := r.reachable(c.Target); err != nil {
		return container.Action{}, err
	}
	return removeAction(c, force)
}

// BuildUpdateRestart changes a container's restart policy in place.
func (r *Real) BuildUpdateRestart(c container.Container, policy string) (
	container.Action, error) {
	if err := r.reachable(c.Target); err != nil {
		return container.Action{}, err
	}
	// Whether this is possible at all is a Podman version question, and only
	// the Podman backend can answer it.
	canUpdate, since := true, ""
	if backend, ok := r.podmanHalves[c.Target]; ok {
		canUpdate, since = backend.CanUpdateRestart(), backend.UpdateRestartSince()
	}
	return updateRestartAction(c, policy, canUpdate, since)
}

// BuildPullImage fetches the image a container was created from.
func (r *Real) BuildPullImage(c container.Container) (container.Action, error) {
	if err := r.reachable(c.Target); err != nil {
		return container.Action{}, err
	}
	return pullAction(c)
}

// BuildPullRef fetches an image named by reference.
func (r *Real) BuildPullRef(target container.Target, ref string) (
	container.Action, error) {
	if err := r.reachable(target); err != nil {
		return container.Action{}, err
	}
	return pullRefAction(target, ref)
}

// BuildRunContainer creates and starts a new container.
func (r *Real) BuildRunContainer(spec container.RunSpec) (container.Action, error) {
	if err := r.reachable(spec.Target); err != nil {
		return container.Action{}, err
	}
	return runAction(spec)
}

// BuildCreateVolume makes a named volume.
func (r *Real) BuildCreateVolume(spec container.VolumeSpec) (container.Action, error) {
	if err := r.reachable(spec.Target); err != nil {
		return container.Action{}, err
	}
	return createVolumeAction(spec)
}

// BuildCreateNetwork makes a network.
func (r *Real) BuildCreateNetwork(spec container.NetworkSpec) (container.Action, error) {
	if err := r.reachable(spec.Target); err != nil {
		return container.Action{}, err
	}
	return createNetworkAction(spec)
}

// BuildCompose runs one Compose verb for a project.
func (r *Real) BuildCompose(p container.Project, verb string) (container.Action, error) {
	if err := r.reachable(p.Target); err != nil {
		return container.Action{}, err
	}
	// A Compose action is only offered where a compose provider actually
	// answered `compose version` on that engine. Building the command anyway
	// would produce a preview that cannot run.
	if info, ok := r.engine(p.Target); !ok || !info.Compose {
		return container.Action{}, fmt.Errorf(
			"%s has no compose command, so a project cannot be driven from here",
			p.Target)
	}
	return composeAction(p, verb)
}

// BuildRemoveImage deletes an image.
func (r *Real) BuildRemoveImage(i container.Image, force bool) (container.Action, error) {
	if err := r.reachable(i.Target); err != nil {
		return container.Action{}, err
	}
	return removeImageAction(i, force)
}

// BuildPruneImages removes the images nothing points at.
func (r *Real) BuildPruneImages(target container.Target, all bool) (
	container.Action, error) {
	if err := r.reachable(target); err != nil {
		return container.Action{}, err
	}
	return pruneImagesAction(target, all), nil
}

// BuildRemoveVolume deletes one named volume.
func (r *Real) BuildRemoveVolume(v container.Volume) (container.Action, error) {
	if err := r.reachable(v.Target); err != nil {
		return container.Action{}, err
	}
	return removeVolumeAction(v)
}

// BuildPruneVolumes removes every volume no container mounts.
func (r *Real) BuildPruneVolumes(target container.Target) (container.Action, error) {
	if err := r.reachable(target); err != nil {
		return container.Action{}, err
	}
	return pruneVolumesAction(target), nil
}

// BuildRemoveNetwork deletes one network.
func (r *Real) BuildRemoveNetwork(n container.Network) (container.Action, error) {
	if err := r.reachable(n.Target); err != nil {
		return container.Action{}, err
	}
	return removeNetworkAction(n)
}

// BuildPruneNetworks removes every network no container is on.
func (r *Real) BuildPruneNetworks(target container.Target) (container.Action, error) {
	if err := r.reachable(target); err != nil {
		return container.Action{}, err
	}
	return pruneNetworksAction(target), nil
}

// BuildSystemPrune is the big one, with both of its choices explicit.
func (r *Real) BuildSystemPrune(target container.Target,
	opts container.PruneOptions) (container.Action, error) {
	if err := r.reachable(target); err != nil {
		return container.Action{}, err
	}
	return systemPruneAction(target, opts), nil
}

// BuildAutoUpdate previews Podman's auto-update, always as a dry run.
func (r *Real) BuildAutoUpdate(target container.Target) (container.Action, error) {
	if err := r.reachable(target); err != nil {
		return container.Action{}, err
	}
	return autoUpdateAction(target)
}

// reachable reports whether a target is one this machine can be asked about.
func (r *Real) reachable(target container.Target) error {
	if _, ok := r.halves[target]; !ok {
		return fmt.Errorf("%s is not available on this machine", target)
	}
	if info, ok := r.engine(target); ok && !info.Available {
		return fmt.Errorf("%s did not answer: %s", target, orNone(info.Detail))
	}
	return nil
}

// engine returns the last probe's answer for a target.
func (r *Real) engine(target container.Target) (container.EngineInfo, bool) {
	for _, info := range r.engines {
		if info.Target == target {
			return info, true
		}
	}
	return container.EngineInfo{}, false
}

// orNone renders an empty reason as a visible placeholder.
func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "no reason was given"
	}
	return value
}

// Package container defines the engine-agnostic model tui-containers renders
// and the interface every engine implementation satisfies. The UI knows only
// these types: it never builds a docker or podman argv itself. Mutations are
// Command values produced by a backend, shown in a preview dialog and only then
// executed.
//
// The model has one shape for two engines that are deliberately alike and
// quietly different. Docker and Podman answer the same questions — what is
// running, from which image, since when, on which ports, and what happened to
// the ones that are not — and the whole point of this tool is that they are
// asked once. Where the two genuinely differ, the model says so in a field
// rather than in a second type: Target names the engine and the scope a row
// came from, Status carries the engine's own words, and the actions a row
// accepts come from the backend's Capabilities.
package container

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/tui-tools/tui-kit/runner"
)

// Command is a single invocation the user is about to run. Argv excludes any
// privilege wrapper: the backend adds it when previewing and when executing.
//
// It is an alias rather than a type of its own, so a backend hands the very
// value the confirm dialog displayed straight to the kit runner, with no
// conversion in between. That identity is what makes the preview a promise.
type Command = runner.Command

// Engine is which container engine a row came from. It is a string rather than
// an enum so `--check` reports a word a script can grep for.
type Engine string

// The two engines this tool drives.
const (
	// EngineDocker is the `docker` CLI talking to a dockerd.
	EngineDocker Engine = "docker"
	// EnginePodman is the `podman` CLI, which has no daemon at all.
	EnginePodman Engine = "podman"
)

// Scope is which set of containers a row belongs to.
//
// Docker has one: dockerd owns every container on the machine, and who may
// talk to it is a question about the socket rather than about the container.
// Podman has two that genuinely coexist — the ones in your own account, run
// rootless, and the ones root owns — and a machine can have both at once with
// different containers in each. A tool that showed one and called it "the
// containers" would be wrong on exactly the machines where it matters.
type Scope string

// The three scopes.
const (
	// ScopeNone is Docker's single scope.
	ScopeNone Scope = ""
	// ScopeUser is Podman in the calling account, rootless.
	ScopeUser Scope = "user"
	// ScopeSystem is Podman as root, reached through `sudo -n`.
	ScopeSystem Scope = "system"
)

// Target names one engine in one scope. It is what routes a command to the
// runner that will execute it, and it is carried by every row so an action
// built from a row cannot reach the wrong engine.
type Target struct {
	Engine Engine `json:"engine"`
	Scope  Scope  `json:"scope,omitempty"`
}

// String names a target the way the screen does: "docker", "podman",
// "podman (system)".
func (t Target) String() string {
	if t.Scope == ScopeNone || t.Scope == ScopeUser {
		return string(t.Engine)
	}
	return string(t.Engine) + " (" + string(t.Scope) + ")"
}

// State is what an engine says a container is doing. The words are the ones
// both engines print, so the column needs no translation table.
type State string

// The container states. StateUnknown is the zero value and means the engine
// said something this tool does not recognise, which is shown as it came rather
// than mapped onto a guess.
const (
	StateUnknown    State = ""
	StateCreated    State = "created"
	StateRunning    State = "running"
	StateRestarting State = "restarting"
	StatePaused     State = "paused"
	StateExited     State = "exited"
	StateDead       State = "dead"
	StateRemoving   State = "removing"
)

// Health is a container's health-check verdict, empty when it has no check.
//
// The words are the ones both engines print, so nothing is translated. The
// distinction that matters is between "unhealthy" and "no check at all": a
// container with no HEALTHCHECK is not healthy, it is unjudged, and a screen
// that showed those the same way would be inventing a verdict.
type Health string

// The health verdicts.
const (
	// HealthNone is the zero value: the container declares no health check.
	HealthNone Health = ""
	// HealthStarting is a check inside its start period, not yet counted.
	HealthStarting Health = "starting"
	// HealthHealthy is a check that passed.
	HealthHealthy Health = "healthy"
	// HealthUnhealthy is a check that has failed enough times in a row for the
	// engine to say so, which is the one worth a colour.
	HealthUnhealthy Health = "unhealthy"
)

// Port is one published port mapping.
type Port struct {
	// HostIP and HostPort are the machine side, empty when the port is exposed
	// but not published.
	HostIP   string `json:"hostIp,omitempty"`
	HostPort int    `json:"hostPort,omitempty"`
	// ContainerPort and Protocol are the container side.
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol,omitempty"`
}

// String renders a mapping the way both engines print it.
func (p Port) String() string {
	protocol := p.Protocol
	if protocol == "" {
		protocol = "tcp"
	}
	target := itoa(p.ContainerPort) + "/" + protocol
	if p.HostPort == 0 {
		return target
	}
	host := itoa(p.HostPort)
	if p.HostIP != "" && p.HostIP != "0.0.0.0" && p.HostIP != "::" {
		host = p.HostIP + ":" + host
	}
	return host + "->" + target
}

// Container is one container, whichever engine owns it.
type Container struct {
	// ID identifies the container across a reload, so the cursor stays on the
	// row it was on. It is the engine's own id, truncated to what the engine
	// prints.
	ID string `json:"id"`
	// Name is what the list shows.
	Name string `json:"name"`
	// Target is the engine and scope this container lives in.
	Target Target `json:"target"`
	// Image is the image reference the container was created from.
	Image string `json:"image"`
	// Command is the entrypoint and arguments, as the engine prints them.
	Command string `json:"command,omitempty"`

	// State is the engine's state word, and Status the engine's own sentence
	// for it: "Up 3 hours (healthy)", "Exited (137) 17 hours ago". The sentence
	// is never rewritten — the whole point of the column is that it is what the
	// engine said.
	State  State  `json:"state"`
	Status string `json:"status,omitempty"`
	// Health is the health-check verdict, empty when there is no check.
	Health Health `json:"health,omitempty"`

	// Created is when the container was created, and Started when it last
	// started. Either is the zero time when the engine did not say.
	Created time.Time `json:"created,omitzero"`
	Started time.Time `json:"started,omitzero"`
	// Uptime is how long a running container has been up, as a phrase for the
	// column: "3 hours", "6 days". It is empty for anything not running.
	//
	// It is text rather than a duration because the two engines disagree about
	// what they will tell you. Podman prints the second the container started
	// and the phrase is computed from it; Docker prints only its own words
	// ("Up 3 hours") in the list, and turning that back into a number would be
	// this tool inventing a precision the engine did not report.
	Uptime string `json:"uptime,omitempty"`

	// ExitCode is how the last run ended, and ExitCodeKnown reports whether it
	// was read at all. The pair matters because 0 and unknown mean different
	// things: a container that exited 0 finished its work, one whose code
	// nobody could read has not been judged.
	ExitCode      int  `json:"exitCode,omitempty"`
	ExitCodeKnown bool `json:"exitCodeKnown,omitempty"`
	// RestartCount is how many times the engine has restarted it.
	RestartCount int `json:"restartCount,omitempty"`
	// RestartPolicy is the policy as written ("no", "always",
	// "unless-stopped", "on-failure[:n]"), empty when the engine did not say.
	RestartPolicy string `json:"restartPolicy,omitempty"`

	// Ports are the published mappings, Networks the networks it is attached
	// to and Mounts the mount sources, as the list command reports them.
	Ports    []Port   `json:"ports,omitempty"`
	Networks []string `json:"networks,omitempty"`
	Mounts   []string `json:"mounts,omitempty"`

	// Project, Service and WorkingDir come from the Compose labels. WorkingDir
	// is what makes a Compose action possible at all: without the directory the
	// project was brought up from there is no `docker compose` to run.
	Project    string `json:"project,omitempty"`
	Service    string `json:"service,omitempty"`
	WorkingDir string `json:"workingDir,omitempty"`

	// Labels are the container's labels, kept whole because the detail screen
	// shows them and the Compose fields above are read back out of them.
	Labels map[string]string `json:"-"`

	// Raw is the line the container was parsed from, shown on the detail screen
	// so a reader can see what was actually read.
	Raw string `json:"-"`
}

// Running reports that the container is doing work right now.
func (c Container) Running() bool { return c.State == StateRunning }

// Unhealthy reports a running container whose own health check says it is not
// well.
//
// It is deliberately restricted to a running container. Both engines keep the
// last health verdict on a container after it stops, so a container that was
// unhealthy when it was killed three weeks ago still reports "unhealthy"
// today — a fact about the past, not about the machine. Counting those would
// fill the attention list with containers nothing is wrong with.
func (c Container) Unhealthy() bool {
	return c.Health == HealthUnhealthy && c.State == StateRunning
}

// Failed reports a container worth looking at before the others: one that
// exited with a non-zero status, one stuck restarting, one its own health check
// calls unhealthy, or one the engine gave up on.
//
// A container that exited 0 is not a failure. A one-shot job that ran and
// finished is the most ordinary thing on a machine, and colouring it red would
// train a reader to ignore the colour.
func (c Container) Failed() bool {
	switch {
	case c.State == StateRestarting || c.State == StateDead:
		return true
	case c.Unhealthy():
		return true
	case c.State == StateExited && c.ExitCodeKnown && c.ExitCode != 0:
		return true
	default:
		return false
	}
}

// Ref is what an action names the container by. The id is preferred over the
// name because it is what the engine printed and it cannot collide across two
// scopes of the same engine.
func (c Container) Ref() string {
	if c.ID != "" {
		return c.ID
	}
	return c.Name
}

// PortsText renders the published ports for a column.
func (c Container) PortsText() string {
	if len(c.Ports) == 0 {
		return ""
	}
	parts := make([]string, 0, len(c.Ports))
	for _, port := range c.Ports {
		parts = append(parts, port.String())
	}
	return strings.Join(parts, ", ")
}

// Haystack is the text the filter matches a container against.
func (c Container) Haystack() string {
	return strings.Join([]string{
		c.Name, c.Image, string(c.State), c.Status, string(c.Health),
		c.Project, c.Service, c.Command, c.PortsText(), c.RestartPolicy,
		c.Target.String(), strings.Join(c.Networks, " "),
	}, " ")
}

// EnvVar is one environment variable of a container.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// Masked reports that the value was replaced before it ever reached the
	// screen, because the name says it carries a secret. The name is always
	// shown: knowing that DATABASE_PASSWORD is set, and that it is not what is
	// on screen, is the whole of what a reader needs here.
	Masked bool `json:"masked"`
}

// Mount is one mount of a container.
type Mount struct {
	Type        string `json:"type"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Mode        string `json:"mode,omitempty"`
	RW          bool   `json:"rw"`
}

// NetworkAttachment is one network a container is on.
type NetworkAttachment struct {
	Name       string `json:"name"`
	IPAddress  string `json:"ip,omitempty"`
	Gateway    string `json:"gateway,omitempty"`
	MacAddress string `json:"mac,omitempty"`
}

// HealthEntry is one recorded run of a container's health check.
type HealthEntry struct {
	Start    time.Time `json:"start,omitzero"`
	End      time.Time `json:"end,omitzero"`
	ExitCode int       `json:"exitCode"`
	Output   string    `json:"output,omitempty"`
}

// HealthReport is what the engine recorded about a container's health check.
type HealthReport struct {
	Status Health `json:"status,omitempty"`
	// FailingStreak is how many checks in a row have failed.
	FailingStreak int `json:"failingStreak,omitempty"`
	// Log is the recorded runs, most recent last, as the engine keeps them.
	Log []HealthEntry `json:"log,omitempty"`
}

// Limits are the resource limits a container was created with. A zero field is
// "no limit", which is what both engines report for an unlimited container and
// is worth showing as such.
type Limits struct {
	// MemoryBytes is the hard memory limit.
	MemoryBytes int64 `json:"memoryBytes,omitempty"`
	// MemoryReservationBytes is the soft limit.
	MemoryReservationBytes int64 `json:"memoryReservationBytes,omitempty"`
	// NanoCPUs is the CPU limit in billionths of a core, so 1500000000 is one
	// and a half cores.
	NanoCPUs int64 `json:"nanoCpus,omitempty"`
	// CPUShares is the relative weight, meaningful only against other
	// containers on the same machine.
	CPUShares int64 `json:"cpuShares,omitempty"`
	// PidsLimit caps the processes inside.
	PidsLimit int64 `json:"pidsLimit,omitempty"`
}

// Stats is one sample of what a container is using right now, from
// `stats --no-stream`. Every field is the engine's own text: the numbers are
// formatted differently by the two engines and reformatting them would be this
// tool claiming a precision neither reported.
type Stats struct {
	CPUPercent string `json:"cpuPercent,omitempty"`
	MemUsage   string `json:"memUsage,omitempty"`
	MemPercent string `json:"memPercent,omitempty"`
	NetIO      string `json:"netIo,omitempty"`
	BlockIO    string `json:"blockIo,omitempty"`
	PIDs       string `json:"pids,omitempty"`
}

// Empty reports that no sample was read.
func (s Stats) Empty() bool {
	return s.CPUPercent == "" && s.MemUsage == "" && s.NetIO == "" && s.PIDs == ""
}

// Detail is one container in full, as the detail screen shows it. Each part
// carries the reason it is missing rather than being silently empty: "this
// container has no health check" is an answer, and an empty section is not.
type Detail struct {
	Container Container           `json:"container"`
	Env       []EnvVar            `json:"env,omitempty"`
	Mounts    []Mount             `json:"mounts,omitempty"`
	Networks  []NetworkAttachment `json:"networks,omitempty"`
	Health    HealthReport        `json:"health,omitzero"`
	Limits    Limits              `json:"limits,omitzero"`
	Stats     Stats               `json:"stats,omitzero"`
	// Entrypoint and Args are what the container runs, split as the engine
	// stores them rather than as the list command abbreviates them.
	Entrypoint []string `json:"entrypoint,omitempty"`
	Args       []string `json:"args,omitempty"`
	// StatsErr is why there is no sample, which for a stopped container is
	// simply that it is not running.
	StatsErr string `json:"statsErr,omitempty"`
	// Raw is the inspect output, so a reader can see what was read.
	Raw string `json:"-"`
}

// Image is one image in one engine's store.
type Image struct {
	ID         string `json:"id"`
	Repository string `json:"repository,omitempty"`
	Tag        string `json:"tag,omitempty"`
	Target     Target `json:"target"`
	// Created is when the image was built.
	Created time.Time `json:"created,omitzero"`
	// SizeBytes is the size on disk, and SizeText the engine's own rendering of
	// it when there is one.
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	SizeText  string `json:"sizeText,omitempty"`
	// Dangling reports an image with no tag left pointing at it: the leftover
	// of a rebuild, which is what `image prune` removes.
	Dangling bool `json:"dangling"`
	// UsedBy is how many containers on this machine were created from it,
	// counted from the container list rather than asked of the engine, so it
	// agrees with the rows above it.
	UsedBy int `json:"usedBy"`
}

// Name is the image as a reader knows it: "postgres:17", or the id for a
// dangling one.
func (i Image) Name() string {
	if i.Repository == "" || i.Repository == "<none>" {
		return i.ID
	}
	if i.Tag == "" || i.Tag == "<none>" {
		return i.Repository
	}
	return i.Repository + ":" + i.Tag
}

// Ref is what an action names the image by.
func (i Image) Ref() string {
	if i.Dangling || i.Repository == "" || i.Repository == "<none>" {
		return i.ID
	}
	return i.Name()
}

// Haystack is the text the filter matches an image against.
func (i Image) Haystack() string {
	return strings.Join([]string{
		i.Name(), i.ID, i.Repository, i.Tag, i.SizeText, i.Target.String(),
	}, " ")
}

// Volume is one named volume.
type Volume struct {
	Name       string `json:"name"`
	Driver     string `json:"driver,omitempty"`
	Mountpoint string `json:"mountpoint,omitempty"`
	Target     Target `json:"target"`
	// InUse reports that a container on this machine mounts it, counted from
	// the container list.
	InUse bool `json:"inUse"`
	// Anonymous reports a volume the engine named itself, which is the kind a
	// prune is really about.
	Anonymous bool `json:"anonymous"`
}

// Haystack is the text the filter matches a volume against.
func (v Volume) Haystack() string {
	return strings.Join([]string{v.Name, v.Driver, v.Mountpoint,
		v.Target.String()}, " ")
}

// Network is one container network.
type Network struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	Driver string `json:"driver,omitempty"`
	Target Target `json:"target"`
	// Internal reports a network with no route out of the machine.
	Internal bool `json:"internal"`
	// Builtin reports one of the networks the engine creates for itself and
	// will not let anyone remove: bridge, host and none for Docker, podman for
	// Podman.
	Builtin bool `json:"builtin"`
	// InUse reports that a container on this machine is attached to it.
	InUse bool `json:"inUse"`
}

// Haystack is the text the filter matches a network against.
func (n Network) Haystack() string {
	return strings.Join([]string{n.Name, n.Driver, n.ID, n.Target.String()}, " ")
}

// DiskRow is one line of `system df`: what a class of object costs and how much
// of that could be reclaimed.
type DiskRow struct {
	Type        string `json:"type"`
	Total       string `json:"total,omitempty"`
	Active      string `json:"active,omitempty"`
	Size        string `json:"size,omitempty"`
	Reclaimable string `json:"reclaimable,omitempty"`
}

// QuadletUnit is one Podman Quadlet file: a container declared as a systemd
// unit, generated into a service at boot.
//
// It is listed and read, and in v0.1 not edited. A Quadlet file is the source
// of a container rather than a container, and changing one means regenerating
// and restarting a unit — which is tui-systemd's job, and doing half of it here
// would be worse than saying where the file is.
type QuadletUnit struct {
	// Path is the file, Name the unit it generates.
	Path  string `json:"path"`
	Name  string `json:"name"`
	Scope Scope  `json:"scope"`
}

// EngineInfo is what one engine in one scope says about itself.
type EngineInfo struct {
	Target Target `json:"target"`
	// Available reports that the engine answered at all.
	Available bool `json:"available"`
	// Installed reports that the binary is on this machine, which is a
	// different fact: a Docker CLI with no daemon behind it is installed and
	// not available, and that distinction is the whole first paragraph of the
	// error a reader would otherwise have to interpret.
	Installed bool `json:"installed"`
	// Version is the client version from `--version`.
	Version string `json:"version,omitempty"`
	// ServerVersion is the daemon's, for Docker. Podman has no daemon and
	// reports its own version here too, which is the honest answer.
	ServerVersion string `json:"serverVersion,omitempty"`
	// StorageDriver, CgroupVersion and Rootless are the three facts that
	// explain most of what is surprising about a machine's containers.
	StorageDriver string `json:"storageDriver,omitempty"`
	CgroupVersion string `json:"cgroupVersion,omitempty"`
	Rootless      bool   `json:"rootless"`
	// Root is where the engine keeps its data.
	Root string `json:"root,omitempty"`
	// RegistryMirrors and SearchRegistries are what a bare image name resolves
	// through, which is the other half of "why did it pull that".
	RegistryMirrors  []string `json:"registryMirrors,omitempty"`
	SearchRegistries []string `json:"searchRegistries,omitempty"`
	// Counts as the engine reports them.
	Containers int `json:"containers"`
	Running    int `json:"running"`
	Paused     int `json:"paused"`
	Stopped    int `json:"stopped"`
	Images     int `json:"images"`
	// Compose reports that `docker compose` / `podman compose` answered, which
	// is what decides whether the Compose actions are offered at all.
	Compose bool `json:"compose"`
	// ComposeVersion is what it answered with.
	ComposeVersion string `json:"composeVersion,omitempty"`
	// Escalated reports that this engine is reached through the privilege
	// prefix, so the header can say so before a command shows it.
	Escalated bool `json:"escalated"`
	// Disk is `system df`, one row per class.
	Disk []DiskRow `json:"disk,omitempty"`
	// Quadlets are the Podman Quadlet files found, empty for Docker.
	Quadlets []QuadletUnit `json:"quadlets,omitempty"`
	// Detail is why the engine is not available, when it is not. It is shown
	// rather than swallowed: "the docker socket refused this account" is the
	// answer to the question, not an error.
	Detail string `json:"detail,omitempty"`
}

// Project is one Compose project: the containers that carry the same
// com.docker.compose.project label, and the directory they were brought up
// from.
type Project struct {
	Name   string `json:"name"`
	Target Target `json:"target"`
	// WorkingDir is com.docker.compose.project.working_dir, empty when the
	// label is absent — which is what decides whether a Compose action can be
	// built at all.
	WorkingDir string `json:"workingDir,omitempty"`
	// Containers are the members, in display order.
	Containers []Container `json:"-"`
	// Running is how many of them are running.
	Running int `json:"running"`
}

// Model is the whole picture tui-containers renders.
type Model struct {
	// Backend names the implementation that produced this model.
	Backend string `json:"backend"`
	// Engines is one entry per engine and scope that was looked for, including
	// the ones that are not here: an engine reporting itself absent with a
	// reason is a fact about the machine worth showing.
	Engines []EngineInfo `json:"engines"`
	// Containers, Images, Volumes and Networks are every row found, across
	// every engine, in display order.
	Containers []Container `json:"containers"`
	Images     []Image     `json:"images"`
	Volumes    []Volume    `json:"volumes"`
	Networks   []Network   `json:"networks"`
	// Projects are the Compose projects the container labels describe.
	Projects []Project `json:"projects"`
}

// Container returns one container by id.
func (m Model) Container(id string) (Container, bool) {
	for _, c := range m.Containers {
		if c.ID == id {
			return c, true
		}
	}
	return Container{}, false
}

// Engine returns the engine entry for a target.
func (m Model) Engine(target Target) (EngineInfo, bool) {
	for _, e := range m.Engines {
		if e.Target == target {
			return e, true
		}
	}
	return EngineInfo{}, false
}

// Project returns one Compose project by name and target.
func (m Model) Project(name string, target Target) (Project, bool) {
	for _, p := range m.Projects {
		if p.Name == name && p.Target == target {
			return p, true
		}
	}
	return Project{}, false
}

// Available are the engines that answered, which is what the header counts.
func (m Model) Available() []EngineInfo {
	var out []EngineInfo
	for _, e := range m.Engines {
		if e.Available {
			out = append(out, e)
		}
	}
	return out
}

// Counts is how many containers are in each state.
func (m Model) Counts() map[State]int {
	counts := map[State]int{}
	for _, c := range m.Containers {
		counts[c.State]++
	}
	return counts
}

// States is the order `--check` reports the state counts in, so a script can
// assert on a zero rather than on a key that may or may not be there.
var States = []State{StateRunning, StateExited, StateRestarting, StatePaused,
	StateCreated, StateDead, StateRemoving}

// Running are the containers doing work.
func (m Model) Running() []Container {
	var out []Container
	for _, c := range m.Containers {
		if c.Running() {
			out = append(out, c)
		}
	}
	return out
}

// Attention are the containers worth looking at before the others.
func (m Model) Attention() []Container {
	var out []Container
	for _, c := range m.Containers {
		if c.Failed() {
			out = append(out, c)
		}
	}
	return out
}

// Unhealthy are the running containers whose own health check disagrees.
func (m Model) Unhealthy() []Container {
	var out []Container
	for _, c := range m.Containers {
		if c.Unhealthy() {
			out = append(out, c)
		}
	}
	return out
}

// Dangling are the images no tag points at any more.
func (m Model) Dangling() []Image {
	var out []Image
	for _, i := range m.Images {
		if i.Dangling {
			out = append(out, i)
		}
	}
	return out
}

// SortContainers orders the merged list the way a reader wants to read it:
// what is wrong first, because that is why anyone opened the tool; then what is
// running, because that is the machine as it stands; then the rest, newest
// first, because a container that stopped an hour ago is more interesting than
// one that stopped in March.
//
// Compose members are kept together within each band: a project is one thing to
// a reader, and three rows of it scattered through the list is three questions
// instead of one.
func SortContainers(list []Container) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if rank := band(a) - band(b); rank != 0 {
			return rank < 0
		}
		if a.Project != b.Project {
			return a.Project > b.Project
		}
		if a.Project != "" && a.Service != b.Service {
			return a.Service < b.Service
		}
		if !a.Created.Equal(b.Created) {
			return a.Created.After(b.Created)
		}
		return a.Name < b.Name
	})
}

// band is the sort band a container falls in: 0 needs attention, 1 is running,
// 2 is everything else.
func band(c Container) int {
	switch {
	case c.Failed():
		return 0
	case c.Running():
		return 1
	default:
		return 2
	}
}

// SortImages orders images largest first, which is the question anyone opens
// the images screen with, and keeps the dangling ones at the top of their size
// band because they are the ones that can go.
func SortImages(list []Image) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Dangling != b.Dangling {
			return a.Dangling
		}
		if a.SizeBytes != b.SizeBytes {
			return a.SizeBytes > b.SizeBytes
		}
		return a.Name() < b.Name()
	})
}

// GroupProjects folds the containers of a model into Compose projects.
//
// The grouping is by label, not by name: two projects called "web" in two
// engines are two projects, and a container with no project label belongs to
// none rather than to an invented one.
func GroupProjects(containers []Container) []Project {
	index := map[string]int{}
	var projects []Project
	for _, c := range containers {
		if c.Project == "" {
			continue
		}
		key := string(c.Target.Engine) + "/" + string(c.Target.Scope) + "/" + c.Project
		position, ok := index[key]
		if !ok {
			projects = append(projects, Project{Name: c.Project, Target: c.Target})
			position = len(projects) - 1
			index[key] = position
		}
		project := &projects[position]
		project.Containers = append(project.Containers, c)
		if project.WorkingDir == "" {
			project.WorkingDir = c.WorkingDir
		}
		if c.Running() {
			project.Running++
		}
	}
	sort.SliceStable(projects, func(i, j int) bool {
		return projects[i].Name < projects[j].Name
	})
	return projects
}

// Capabilities tells the UI what a backend supports, so the key map is built
// from the backend rather than hardcoded.
type Capabilities struct {
	// SupportsLifecycle reports that a container can be started, stopped,
	// restarted, killed, paused and unpaused.
	SupportsLifecycle bool
	// SupportsRemove reports that a container can be removed.
	SupportsRemove bool
	// SupportsUpdate reports that a container's restart policy can be changed
	// in place.
	SupportsUpdate bool
	// SupportsCompose reports that at least one engine answered
	// `compose version`, so the project actions are worth offering.
	SupportsCompose bool
	// SupportsPrune reports that the prune commands can be built.
	SupportsPrune bool
	// SupportsAutoUpdate reports that a Podman is here, which is the only
	// engine with `auto-update`.
	SupportsAutoUpdate bool
	// SupportsCreate reports that something new can be made: a container from
	// an image, a named volume, a network, or an image pulled by reference.
	SupportsCreate bool
	// RestartPolicies is the closed set of policies the update form offers.
	RestartPolicies []string
	// VolumeDrivers and NetworkDrivers are the closed sets the create form
	// offers. They are the drivers every engine has: a driver from a plugin is
	// a machine-specific thing nothing here can discover, and a picker that
	// listed one it had guessed at would be guessing.
	VolumeDrivers  []string
	NetworkDrivers []string
}

// RunSpec is what the run form collected: one new container, described in the
// terms the form asked for.
//
// The list fields are the strings the user typed, split but not parsed. What a
// port mapping or a mount may be is the engine package's rule, checked once
// where the argv is built, so the UI never has to know either engine's grammar.
type RunSpec struct {
	// Target is the engine and scope the container is created in.
	Target Target
	// Image is the reference the container is created from.
	Image string
	// Name is what the container is called, empty for an engine-chosen one.
	Name string
	// Ports are `host:container[/proto]` entries.
	Ports []string
	// Volumes are `host:container[:ro]` entries.
	Volumes []string
	// EnvFile is an absolute path to a file of NAME=value lines.
	//
	// There is deliberately no field for an inline value. An environment
	// variable typed into a form is a secret typed into a form: it would be on
	// screen, in the preview, in the shell history of anyone who copied the
	// preview, and in the container's own inspect output. A file the engine
	// reads itself is the same feature without any of that.
	EnvFile string
	// RestartPolicy is one of Capabilities.RestartPolicies, empty for the
	// engine's default.
	RestartPolicy string
}

// VolumeSpec is a named volume about to be created.
type VolumeSpec struct {
	Target Target
	Name   string
	Driver string
}

// NetworkSpec is a network about to be created.
type NetworkSpec struct {
	Target Target
	Name   string
	Driver string
}

// LogOptions is what the log pane asks for.
type LogOptions struct {
	// Tail is how many lines from the end, always set: a container that has
	// been logging for a month must not be read whole into a pane.
	Tail int
	// Since bounds the window, in the engines' own duration form ("30m",
	// "24h"). Empty asks for whatever Tail covers.
	Since string
	// Timestamps prefixes each line with the time the engine recorded, which is
	// the only way to tell a log that stopped from one that is quiet.
	Timestamps bool
}

// Action is a change the user is about to make: what it is called, which engine
// it goes to, and the exact commands that apply it.
//
// The commands are runner.Command values and the target routes them, so the
// value the confirm dialog rendered is the value the runner executes and the
// engine it reaches is fixed before the dialog opens.
type Action struct {
	// Title is what the dialog is called.
	Title string
	// Target is the engine and scope every command in this action goes to.
	Target Target
	// Body explains what will happen, beyond the command line.
	Body string
	// Warning is a caveat the dialog must show in the danger colour.
	Warning string
	// Destructive paints the dialog in the danger colour.
	Destructive bool
	// Commands are run in order, and are what the dialog shows.
	Commands []Command
}

// PruneOptions is what a system prune was asked for. Both flags are choices the
// user made explicitly in the picker, because they are the difference between
// removing what is unused and removing what is merely not running.
type PruneOptions struct {
	// All adds -a: every image not used by an existing container goes, not just
	// the dangling ones.
	All bool
	// Volumes adds --volumes: the unused named volumes go too, and that is the
	// one that deletes data.
	Volumes bool
}

// Backend is the boundary between the UI and the machine. Load reads state; the
// Build* methods turn user intent into previewable Actions; Run executes a
// Command the user confirmed, against the target the Action named. Nothing else
// may mutate the system.
type Backend interface {
	// Name is the backend identifier ("host").
	Name() string
	// Describe is the one-line summary shown in the header.
	Describe() string
	// Capabilities reports what this backend supports.
	Capabilities() Capabilities

	// Preview renders the exact command line Run will execute for a target,
	// privilege wrapper included. This is the text shown in the confirm dialog.
	Preview(target Target, cmd Command) string

	// Load reads every engine on the machine.
	Load(ctx context.Context) (Model, error)
	// Run executes a previously previewed command against a target.
	Run(ctx context.Context, target Target, cmd Command) (string, error)

	// Inspect returns one container in full.
	Inspect(ctx context.Context, c Container) (Detail, error)
	// Logs returns the container's recent log lines.
	Logs(ctx context.Context, c Container, opts LogOptions) (string, error)
	// LogsCommand renders the command line Logs is running, so the pane can
	// show it. A pane that re-reads on a timer has to be able to say what it
	// is re-reading, which is the same promise every confirm dialog makes.
	LogsCommand(c Container, opts LogOptions) string

	// The lifecycle actions. Each returns an error naming the reason when the
	// container is not one it applies to, which the UI turns into a hint.
	BuildStart(c Container) (Action, error)
	BuildStop(c Container) (Action, error)
	BuildRestart(c Container) (Action, error)
	BuildKill(c Container) (Action, error)
	BuildPause(c Container) (Action, error)
	BuildUnpause(c Container) (Action, error)
	// BuildRemove deletes a container. It refuses a running one unless force
	// was chosen explicitly, because removing a running container is killing
	// it and the dialog has to say so.
	BuildRemove(c Container, force bool) (Action, error)
	// BuildUpdateRestart changes a container's restart policy in place.
	BuildUpdateRestart(c Container, policy string) (Action, error)
	// BuildPullImage pulls the image a container was created from. It does not
	// recreate the container, and says so: the running container keeps the
	// image it started with until something recreates it.
	BuildPullImage(c Container) (Action, error)
	// BuildPullRef pulls an image named by reference rather than by a
	// container that already exists, which is the only way to fetch something
	// this machine has never run.
	BuildPullRef(target Target, ref string) (Action, error)

	// BuildRunContainer creates and starts a new container from an image. It
	// is always detached: this tool starts no process it does not wait for,
	// and a foreground container would be exactly that.
	BuildRunContainer(spec RunSpec) (Action, error)
	// BuildCreateVolume and BuildCreateNetwork make the two things a container
	// needs that nothing on the machine creates on its own.
	BuildCreateVolume(spec VolumeSpec) (Action, error)
	BuildCreateNetwork(spec NetworkSpec) (Action, error)

	// BuildCompose runs one Compose verb for a project.
	BuildCompose(p Project, verb string) (Action, error)

	// BuildRemoveImage deletes an image, refusing one a container still uses.
	BuildRemoveImage(i Image, force bool) (Action, error)
	// BuildPruneImages removes the dangling images, or with all every image no
	// container uses.
	BuildPruneImages(target Target, all bool) (Action, error)
	// BuildRemoveVolume and BuildPruneVolumes remove storage, which is the one
	// class of action here that destroys data.
	BuildRemoveVolume(v Volume) (Action, error)
	BuildPruneVolumes(target Target) (Action, error)
	// BuildRemoveNetwork and BuildPruneNetworks remove networks.
	BuildRemoveNetwork(n Network) (Action, error)
	BuildPruneNetworks(target Target) (Action, error)
	// BuildSystemPrune is the big one, with both of its choices explicit.
	BuildSystemPrune(target Target, opts PruneOptions) (Action, error)
	// BuildAutoUpdate previews Podman's auto-update, always as a dry run: the
	// real one restarts every unit it decides is out of date, and that is not
	// something to offer behind one keystroke.
	BuildAutoUpdate(target Target) (Action, error)
}

// itoa is strconv.Itoa without the import, for the one place this file needs
// it.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

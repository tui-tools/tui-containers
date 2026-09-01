package podman

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tui-tools/tui-containers/internal/container"
)

// This file builds every argv the Podman half of the tool can produce. They are
// functions of their arguments and nothing else — no clock, no filesystem, no
// process — so a test can assert on the exact command line the confirm dialog
// will show, and the dialog and the execution consume the same value.

// Bin is the binary this package drives.
const Bin = "podman"

// The manifest features that gate what this package will offer.
//
// Podman implements Docker's command surface and has added to it over time, so
// three things here are version questions rather than always-true ones. Each is
// asked by name through the capability set rather than by comparing numbers in
// code.
const (
	// FeatureCompose gates `podman compose`, the thin wrapper that hands a
	// project to docker-compose or podman-compose with the Podman socket wired
	// up. It arrived in Podman 4.7; before it, a project was driven by calling
	// podman-compose directly, which is a different program with its own
	// arguments and not one this tool will guess at.
	FeatureCompose = "compose"
	// FeatureQuadlet gates reading the Quadlet unit directories, which did not
	// exist before Podman 4.4.
	FeatureQuadlet = "quadlet"
	// FeatureUpdateRestart gates `podman update --restart`. `podman update`
	// arrived in 4.3 for resource limits only; the restart policy was added in
	// 5.0, and on an older Podman the policy can only be changed by recreating
	// the container.
	FeatureUpdateRestart = "update-restart"
)

// RestartPolicies is the closed set the update form offers. It is Docker's set
// plus "never", which Podman accepts as a synonym for "no" and which people
// who write Quadlet files are used to.
var RestartPolicies = []string{"no", "on-failure", "unless-stopped", "always"}

// refRe bounds what may be passed to Podman as a container, image, volume or
// network reference. Everything that reaches it comes from the engine's own
// output, and it is checked anyway: a value that goes into an argv is a value
// that has to be checked at the boundary, not where it was read.
var refRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,255}$`)

// pathRe bounds a Compose project directory and compose file path. Both come
// from container labels, which are set by whoever created the container, so
// they are treated as input rather than as fact.
var pathRe = regexp.MustCompile(`^/[^\x00\n\r]{0,1000}$`)

// policyRe bounds a restart policy, including Podman's "never" and the
// on-failure form with a count.
var policyRe = regexp.MustCompile(
	`^(no|never|always|unless-stopped|on-failure(:[0-9]{1,4})?)$`)

// sinceRe bounds a --since window in the form both engines accept.
var sinceRe = regexp.MustCompile(`^[0-9]{1,6}[smhd]$`)

// checkRef rejects a reference this package will not pass on.
func checkRef(kind, ref string) error {
	if strings.TrimSpace(ref) == "" {
		return fmt.Errorf("podman: this %s has no reference to name it by", kind)
	}
	if !refRe.MatchString(ref) {
		return fmt.Errorf("podman: %q is not a %s reference", ref, kind)
	}
	return nil
}

// PSArgv lists every container, running or not. Podman has answered
// `--format json` since long before the minimum this tool supports, and it
// answers with an array rather than with one object per line.
func PSArgv() []string { return []string{Bin, "ps", "-a", "--format", "json"} }

// ImagesArgv lists the images, dangling ones included.
func ImagesArgv() []string { return []string{Bin, "images", "--format", "json"} }

// VolumesArgv lists the named volumes.
func VolumesArgv() []string {
	return []string{Bin, "volume", "ls", "--format", "json"}
}

// NetworksArgv lists the networks.
func NetworksArgv() []string {
	return []string{Bin, "network", "ls", "--format", "json"}
}

// InfoArgv asks Podman about itself. It is also the probe that decides whether
// this scope is usable at all: a user with no subuid range, or a system scope
// this account cannot escalate into, both answer here and nowhere else.
func InfoArgv() []string { return []string{Bin, "info", "--format", "json"} }

// DiskArgv asks what this scope is using on disk, as the same five-column
// table Docker prints.
func DiskArgv() []string { return []string{Bin, "system", "df"} }

// InspectArgv reads one container in full. `--type container` is explicit
// because Podman's inspect will also answer about an image or a volume with the
// same name, and a tool that let the engine choose would occasionally show a
// reader something else entirely.
func InspectArgv(ref string) ([]string, error) {
	if err := checkRef("container", ref); err != nil {
		return nil, err
	}
	return []string{Bin, "inspect", "--type", "container", ref}, nil
}

// StatsArgv takes one sample of what a container is using. --no-stream is what
// makes it a read that returns: without it the command runs until it is
// killed, and this tool starts no process it does not wait for.
func StatsArgv(ref string) ([]string, error) {
	if err := checkRef("container", ref); err != nil {
		return nil, err
	}
	return []string{Bin, "stats", "--no-stream", "--format", "json", ref}, nil
}

// LogsArgv reads the end of a container's log.
//
// The log pane re-reads this on a timer rather than following the stream,
// because `podman logs -f` is a process that never returns and this tool
// starts none. A re-read costs one invocation and gives the same picture.
func LogsArgv(ref string, opts container.LogOptions) ([]string, error) {
	if err := checkRef("container", ref); err != nil {
		return nil, err
	}
	tail := opts.Tail
	if tail < 1 || tail > 10000 {
		tail = 200
	}
	argv := []string{Bin, "logs", "--tail", strconv.Itoa(tail)}
	if opts.Timestamps {
		argv = append(argv, "--timestamps")
	}
	if since := strings.TrimSpace(opts.Since); since != "" {
		if !sinceRe.MatchString(since) {
			return nil, fmt.Errorf(
				"podman: %q is not a time window — use a number and one of "+
					"s, m, h or d, as in 30m or 24h", since)
		}
		argv = append(argv, "--since", since)
	}
	return append(argv, ref), nil
}

// ComposeVersionArgv asks whether a compose provider is installed behind
// `podman compose`, which is what decides whether the project actions are
// offered at all.
func ComposeVersionArgv() []string { return []string{Bin, "compose", "version"} }

// VersionArgv is the client version, which is also the manifest's version
// command.
func VersionArgv() []string { return []string{Bin, "--version"} }

// lifecycle is the shared shape of the six verbs that move a container between
// states: one verb, one container.
var lifecycle = map[string]struct {
	description string
	destructive bool
}{
	"start":   {"Start %s", false},
	"stop":    {"Stop %s, giving it the grace period it was created with", true},
	"restart": {"Stop %s and start it again", true},
	"kill":    {"Send SIGKILL to %s", true},
	"pause":   {"Freeze every process in %s", true},
	"unpause": {"Thaw the processes in %s", false},
}

// BuildLifecycle builds one of the six state verbs for a container.
func BuildLifecycle(c container.Container, verb string) (container.Command, error) {
	spec, ok := lifecycle[verb]
	if !ok {
		return container.Command{}, fmt.Errorf("podman: %q is not a lifecycle verb", verb)
	}
	if err := checkRef("container", c.Ref()); err != nil {
		return container.Command{}, err
	}
	return container.Command{
		Argv:        []string{Bin, verb, c.Ref()},
		Description: fmt.Sprintf(spec.description, c.Name),
		Destructive: spec.destructive,
	}, nil
}

// BuildRemove deletes a container.
//
// A running container is refused unless force was chosen: `podman rm -f` kills
// it first, and that is a different act from removing something that already
// stopped. The refusal names the two ways forward rather than quietly adding
// the flag.
func BuildRemove(c container.Container, force bool) (container.Command, error) {
	if err := checkRef("container", c.Ref()); err != nil {
		return container.Command{}, err
	}
	if c.Running() && !force {
		return container.Command{}, fmt.Errorf(
			"%s is running: stop it first, or choose the forced removal, which "+
				"kills it and then removes it", c.Name)
	}
	argv := []string{Bin, "rm"}
	description := "Remove " + c.Name + ", which has stopped"
	if force {
		argv = append(argv, "-f")
		description = "Kill " + c.Name + " and remove it"
	}
	return container.Command{
		Argv:        append(argv, c.Ref()),
		Description: description,
		Destructive: true,
	}, nil
}

// BuildUpdateRestart changes a container's restart policy in place.
//
// It needs Podman 5.0: `podman update` arrived in 4.3 and only carried the
// resource limits, and the restart policy was added later. On an older Podman
// the caller refuses with that reason rather than running a command that would
// be rejected with a flag error nobody can act on.
func BuildUpdateRestart(c container.Container, policy string) (container.Command, error) {
	if err := checkRef("container", c.Ref()); err != nil {
		return container.Command{}, err
	}
	policy = strings.TrimSpace(policy)
	if !policyRe.MatchString(policy) {
		return container.Command{}, fmt.Errorf(
			"podman: %q is not a restart policy — it is one of no, never, "+
				"always, unless-stopped or on-failure[:retries]", policy)
	}
	return container.Command{
		Argv:        []string{Bin, "update", "--restart=" + policy, c.Ref()},
		Description: "Set " + c.Name + "'s restart policy to " + policy,
		Destructive: true,
	}, nil
}

// BuildPull fetches the image a container was created from.
//
// It changes nothing about the running container, and the dialog says so: the
// container keeps the image it started with until something recreates it, and
// pulling a newer tag under a running container is how people end up believing
// they upgraded something they did not.
func BuildPull(image string) (container.Command, error) {
	if err := checkRef("image", image); err != nil {
		return container.Command{}, err
	}
	return container.Command{
		Argv:        []string{Bin, "pull", image},
		Description: "Fetch the newest " + image + " into this engine's store",
		Destructive: true,
	}, nil
}

// The rules a value typed into the run form has to satisfy before it can reach
// an argv. Everything above this point is checked too, but everything above it
// came out of the engine's own output; these come from a keyboard, which is the
// difference between a guard and a formality.
var (
	// nameRe is what Podman itself accepts as a container, volume or network
	// name.
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	// publishRe is one `host:container[/proto]` mapping. A bare container port is
	// deliberately not accepted: `-p 80` publishes to a random host port, and a
	// form that quietly did that would be a form whose preview does not say
	// what will happen.
	publishRe = regexp.MustCompile(`^([0-9]{1,5}):([0-9]{1,5})(/(tcp|udp|sctp))?$`)
	// absPathRe is an absolute path this tool will hand to an engine: a mount
	// source, a mount destination or an --env-file.
	absPathRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]{0,255}$`)
	// driverRe is a volume or network driver name.
	driverRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
)

// VolumeDrivers and NetworkDrivers are the closed sets the create form offers.
// Both engines ship more, and a driver from a plugin is a machine-specific
// thing this tool has no way to discover — so the picker offers what is always
// there and refuses the rest by name rather than pretending to know.
var (
	VolumeDrivers  = []string{"local"}
	NetworkDrivers = []string{"bridge", "macvlan", "ipvlan"}
)

// checkName rejects a name Podman would reject, in words that say which rule
// was broken.
func checkName(kind, name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf(
			"podman: %q is not a %s name — it starts with a letter or a digit "+
				"and carries only letters, digits, and _ . -", name, kind)
	}
	return nil
}

// checkAbsPath rejects a path that is not absolute, or that walks out of
// itself.
func checkAbsPath(kind, path string) error {
	if !absPathRe.MatchString(path) {
		return fmt.Errorf("podman: %q is not an absolute path for a %s", path, kind)
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("podman: %q walks out of itself with a `..`", path)
	}
	return nil
}

// portArgs renders the `-p` pairs of a run, checking each mapping.
//
// Rootless Podman cannot publish below port 1024 unless the machine's
// `net.ipv4.ip_unprivileged_port_start` was lowered, and that is a sysctl
// rather than something this tool can see: the mapping is passed on as typed
// and Podman's own refusal is the honest answer.
func portArgs(ports []string) ([]string, error) {
	var argv []string
	for _, port := range ports {
		port = strings.TrimSpace(port)
		if port == "" {
			continue
		}
		match := publishRe.FindStringSubmatch(port)
		if match == nil {
			return nil, fmt.Errorf(
				"podman: %q is not a port mapping — it is host:container, with an "+
					"optional /tcp, /udp or /sctp, as in 8080:80 or 5353:53/udp",
				port)
		}
		for _, side := range []string{match[1], match[2]} {
			number, err := strconv.Atoi(side)
			if err != nil || number < 1 || number > 65535 {
				return nil, fmt.Errorf(
					"podman: %s is not a port number, which is 1 to 65535", side)
			}
		}
		argv = append(argv, "-p", port)
	}
	return argv, nil
}

// mountArgs renders the `-v` pairs of a run, checking each mount.
//
// The source may be an absolute path or the name of a volume, because those are
// the two things the engine itself accepts and a volume made a keystroke ago on
// the storage screen is the commonest reason to open this form at all. A
// relative path is refused: Podman reads one as a volume name, so `./data`
// would silently create a volume rather than mount the directory meant.
func mountArgs(mounts []string) ([]string, error) {
	var argv []string
	for _, mount := range mounts {
		mount = strings.TrimSpace(mount)
		if mount == "" {
			continue
		}
		parts := strings.Split(mount, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return nil, fmt.Errorf(
				"podman: %q is not a mount — it is source:destination, with an "+
					"optional :ro, as in /srv/data:/data or pgdata:/var/lib/postgresql/data",
				mount)
		}
		source, destination := parts[0], parts[1]
		if strings.HasPrefix(source, "/") {
			if err := checkAbsPath("mount source", source); err != nil {
				return nil, err
			}
		} else if err := checkName("volume", source); err != nil {
			return nil, fmt.Errorf(
				"podman: %q is neither an absolute path nor a volume name; a "+
					"relative path is read as a volume name and would create one",
				source)
		}
		if err := checkAbsPath("mount destination", destination); err != nil {
			return nil, err
		}
		if len(parts) == 3 && parts[2] != "ro" && parts[2] != "rw" {
			return nil, fmt.Errorf(
				"podman: %q is not a mount option this tool passes on — it is ro "+
					"or rw", parts[2])
		}
		argv = append(argv, "-v", mount)
	}
	return argv, nil
}

// BuildRun creates and starts a new container from an image.
//
// It is always `-d`. A container in the foreground is a process that does not
// return, and this tool starts none: the promise every preview makes is that
// the command shown is the command that ran and that it finished.
//
// There is no field for an inline environment value, and that is the one
// deliberate omission here. `-e DATABASE_PASSWORD=…` would put the secret in
// the preview, in the confirm dialog, and in `podman inspect` afterwards.
// `--env-file` is the same feature with the value read by the engine from a
// file whose mode the user already controls.
func BuildRun(spec container.RunSpec) (container.Command, error) {
	image := strings.TrimSpace(spec.Image)
	if err := checkRef("image", image); err != nil {
		return container.Command{}, err
	}
	argv := []string{Bin, "run", "-d"}

	name := strings.TrimSpace(spec.Name)
	if name != "" {
		if err := checkName("container", name); err != nil {
			return container.Command{}, err
		}
		argv = append(argv, "--name", name)
	}

	ports, err := portArgs(spec.Ports)
	if err != nil {
		return container.Command{}, err
	}
	argv = append(argv, ports...)

	mounts, err := mountArgs(spec.Volumes)
	if err != nil {
		return container.Command{}, err
	}
	argv = append(argv, mounts...)

	if envFile := strings.TrimSpace(spec.EnvFile); envFile != "" {
		if err := checkAbsPath("env file", envFile); err != nil {
			return container.Command{}, err
		}
		argv = append(argv, "--env-file", envFile)
	}

	if policy := strings.TrimSpace(spec.RestartPolicy); policy != "" {
		if !policyRe.MatchString(policy) {
			return container.Command{}, fmt.Errorf(
				"podman: %q is not a restart policy — it is one of no, never, "+
					"always, unless-stopped or on-failure[:retries]", policy)
		}
		argv = append(argv, "--restart", policy)
	}

	called := image
	if name != "" {
		called = name
	}
	return container.Command{
		Argv:        append(argv, image),
		Description: "Create " + called + " from " + image + " and start it in the background",
		Destructive: true,
	}, nil
}

// BuildCreateVolume makes a named volume.
func BuildCreateVolume(spec container.VolumeSpec) (container.Command, error) {
	name := strings.TrimSpace(spec.Name)
	if err := checkName("volume", name); err != nil {
		return container.Command{}, err
	}
	argv := []string{Bin, "volume", "create"}
	if driver := strings.TrimSpace(spec.Driver); driver != "" {
		if !driverRe.MatchString(driver) {
			return container.Command{}, fmt.Errorf(
				"podman: %q is not a volume driver name", driver)
		}
		argv = append(argv, "--driver", driver)
	}
	return container.Command{
		Argv:        append(argv, name),
		Description: "Create the named volume " + name,
		Destructive: true,
	}, nil
}

// BuildCreateNetwork makes a network.
func BuildCreateNetwork(spec container.NetworkSpec) (container.Command, error) {
	name := strings.TrimSpace(spec.Name)
	if err := checkName("network", name); err != nil {
		return container.Command{}, err
	}
	argv := []string{Bin, "network", "create"}
	if driver := strings.TrimSpace(spec.Driver); driver != "" {
		if !driverRe.MatchString(driver) {
			return container.Command{}, fmt.Errorf(
				"podman: %q is not a network driver name", driver)
		}
		argv = append(argv, "--driver", driver)
	}
	return container.Command{
		Argv:        append(argv, name),
		Description: "Create the network " + name,
		Destructive: true,
	}, nil
}

// composeVerbs is the closed set of project actions, and what each one does.
var composeVerbs = map[string]struct {
	args        []string
	description string
	destructive bool
}{
	"up":   {[]string{"up", "-d"}, "Bring every service of %s up, in the background", true},
	"down": {[]string{"down"}, "Stop and remove every container of %s, and the network it made", true},
	"pull": {[]string{"pull"}, "Fetch the newest image of every service of %s", true},
}

// ComposeVerbs is the order the project picker offers them in.
var ComposeVerbs = []string{"up", "down", "pull"}

// BuildCompose runs one Compose verb for a project.
//
// `podman compose` is a wrapper: it finds docker-compose or podman-compose,
// points it at this scope's Podman socket and hands the arguments straight
// through. That is why the arguments below are Docker's — they are going to
// Docker's Compose — and it is why the preview is worth reading twice: the
// program that ends up running is chosen by Podman, not by this tool, and the
// engine prints which one it picked.
func BuildCompose(p container.Project, files []string, verb string) (container.Command, error) {
	spec, ok := composeVerbs[verb]
	if !ok {
		return container.Command{}, fmt.Errorf("podman: %q is not a compose verb", verb)
	}
	if err := checkRef("compose project", p.Name); err != nil {
		return container.Command{}, err
	}
	if !pathRe.MatchString(p.WorkingDir) {
		return container.Command{}, fmt.Errorf(
			"the containers of %s carry no com.docker.compose.project.working_dir "+
				"label, so there is no directory to run compose in", p.Name)
	}
	argv := []string{Bin, "compose", "--project-name", p.Name,
		"--project-directory", p.WorkingDir}
	for _, file := range files {
		if !pathRe.MatchString(file) {
			return container.Command{}, fmt.Errorf(
				"podman: %q is not a compose file path", file)
		}
		argv = append(argv, "-f", file)
	}
	return container.Command{
		Argv:        append(argv, spec.args...),
		Description: fmt.Sprintf(spec.description, p.Name),
		Destructive: spec.destructive,
	}, nil
}

// BuildRemoveImage deletes an image.
func BuildRemoveImage(i container.Image, force bool) (container.Command, error) {
	if err := checkRef("image", i.Ref()); err != nil {
		return container.Command{}, err
	}
	if i.UsedBy > 0 && !force {
		return container.Command{}, fmt.Errorf(
			"%s is what %d container(s) in this scope were created from: remove "+
				"those first, or the image goes and they cannot be started again",
			i.Name(), i.UsedBy)
	}
	argv := []string{Bin, "rmi"}
	if force {
		argv = append(argv, "-f")
	}
	return container.Command{
		Argv:        append(argv, i.Ref()),
		Description: "Remove the image " + i.Name(),
		Destructive: true,
	}, nil
}

// BuildPruneImages removes the images nothing points at.
//
// The two forms are genuinely different and both are offered by name. Without
// -a it removes the dangling images: the layers a rebuild left behind, which
// nothing can refer to. With -a it removes every image no *existing* container
// was created from — including the base images a build would otherwise reuse,
// and including anything pulled for later.
func BuildPruneImages(all bool) container.Command {
	argv := []string{Bin, "image", "prune", "-f"}
	description := "Remove the dangling images: the layers left behind by a rebuild"
	if all {
		argv = []string{Bin, "image", "prune", "-a", "-f"}
		description = "Remove every image no existing container was created from"
	}
	return container.Command{
		Argv:        argv,
		Description: description,
		Destructive: true,
	}
}

// BuildRemoveVolume deletes one named volume, and with it whatever was in it.
func BuildRemoveVolume(v container.Volume) (container.Command, error) {
	if err := checkRef("volume", v.Name); err != nil {
		return container.Command{}, err
	}
	if v.InUse {
		return container.Command{}, fmt.Errorf(
			"%s is mounted by a container in this scope; Podman will refuse to "+
				"remove it, and so does this tool", v.Name)
	}
	return container.Command{
		Argv:        []string{Bin, "volume", "rm", v.Name},
		Description: "Remove the volume " + v.Name + " and everything stored in it",
		Destructive: true,
	}, nil
}

// BuildPruneVolumes removes every volume no container mounts.
func BuildPruneVolumes() container.Command {
	return container.Command{
		Argv:        []string{Bin, "volume", "prune", "-f"},
		Description: "Remove every volume no container mounts, and the data in them",
		Destructive: true,
	}
}

// BuildRemoveNetwork deletes one network.
func BuildRemoveNetwork(n container.Network) (container.Command, error) {
	if err := checkRef("network", n.Name); err != nil {
		return container.Command{}, err
	}
	if n.Builtin {
		return container.Command{}, fmt.Errorf(
			"%s is the default network Podman creates for itself, and it cannot "+
				"be removed", n.Name)
	}
	if n.InUse {
		return container.Command{}, fmt.Errorf(
			"a container in this scope is attached to %s; disconnect or remove "+
				"it first", n.Name)
	}
	return container.Command{
		Argv:        []string{Bin, "network", "rm", n.Name},
		Description: "Remove the network " + n.Name,
		Destructive: true,
	}, nil
}

// BuildPruneNetworks removes every network no container is on.
func BuildPruneNetworks() container.Command {
	return container.Command{
		Argv:        []string{Bin, "network", "prune", "-f"},
		Description: "Remove every network no container is attached to",
		Destructive: true,
	}
}

// BuildSystemPrune is the big one, and both of its choices are made explicitly.
//
// The bare form removes the stopped containers, the dangling images and the
// unused networks. -a adds every image no running container uses; --volumes
// adds the unused named volumes, and that is the flag that deletes data rather
// than space. Neither is ever added for the user: the picker asks, and the
// command line shows what was chosen.
func BuildSystemPrune(opts container.PruneOptions) container.Command {
	argv := []string{Bin, "system", "prune", "-f"}
	parts := []string{"the stopped containers, the dangling images and the " +
		"unused networks"}
	if opts.All {
		argv = append(argv, "-a")
		parts = append(parts, "every image no running container uses")
	}
	if opts.Volumes {
		argv = append(argv, "--volumes")
		parts = append(parts, "the unused named volumes, with the data in them")
	}
	return container.Command{
		Argv:        argv,
		Description: "Remove " + strings.Join(parts, ", plus "),
		Destructive: true,
	}
}

// BuildAutoUpdateDryRun asks Podman what its auto-update would do.
//
// Only the dry run is offered. The real `podman auto-update` pulls a new image
// for every unit labelled io.containers.autoupdate and restarts the ones that
// changed — several services at once, chosen by a label rather than by the
// person pressing the key. Seeing the list first and then restarting what you
// meant to restart is the same work with the surprise taken out.
func BuildAutoUpdateDryRun() container.Command {
	return container.Command{
		Argv: []string{Bin, "auto-update", "--dry-run"},
		Description: "List the units auto-update would refresh, and whether each " +
			"image has moved. Nothing is pulled and nothing is restarted",
	}
}

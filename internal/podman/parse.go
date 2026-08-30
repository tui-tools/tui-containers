package podman

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tui-tools/tui-containers/internal/container"
)

// This file turns what the Podman CLI prints into the neutral model. It starts
// no process: everything here takes the text a command returned and gives back
// values, so every parser is a table test against captured output.
//
// The difference from Docker that shapes this whole file is that Podman answers
// in an array of typed objects where Docker answers in a stream of lines of
// flattened strings. Podman's ports are objects, its labels are a map and its
// times are seconds since the epoch — so nothing here has to be split on commas
// and guessed at, and the parsers are correspondingly shorter and stricter.

// TargetFor is the target for one scope of Podman.
func TargetFor(scope container.Scope) container.Target {
	return container.Target{Engine: container.EnginePodman, Scope: scope}
}

// Masked is the placeholder shown in place of a secret.
const Masked = "••••••••"

// The Compose labels this tool reads. They are Docker's names on both engines:
// Compose writes them whichever engine it is talking to, and podman-compose
// writes the same ones.
const (
	LabelProject     = "com.docker.compose.project"
	LabelService     = "com.docker.compose.service"
	LabelWorkingDir  = "com.docker.compose.project.working_dir"
	LabelConfigFiles = "com.docker.compose.project.config_files"
)

// flexString reads a JSON field that one Podman version prints as a string and
// another as a number. Podman's stats output has moved on both counts between
// releases, and a parser that insisted on one of them would fail on half the
// machines this tool is meant to run on.
type flexString string

// UnmarshalJSON accepts a string, a number or null.
func (f *flexString) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	switch {
	case text == "null":
		*f = ""
		return nil
	case strings.HasPrefix(text, `"`):
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*f = flexString(value)
		return nil
	default:
		*f = flexString(text)
		return nil
	}
}

// flexHealth reads the health field, which Podman prints as a plain word on
// some versions and as the whole health object on others.
type flexHealth struct{ Status string }

// UnmarshalJSON accepts either shape, and null for a container with no check.
func (h *flexHealth) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "null" {
		h.Status = ""
		return nil
	}
	if strings.HasPrefix(text, `"`) {
		return json.Unmarshal(data, &h.Status)
	}
	var object struct {
		Status string `json:"Status"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	h.Status = object.Status
	return nil
}

// psRow is one element of `podman ps --format json`.
type psRow struct {
	ID        string            `json:"Id"`
	Names     []string          `json:"Names"`
	Image     string            `json:"Image"`
	Command   []string          `json:"Command"`
	Created   int64             `json:"Created"`
	StartedAt int64             `json:"StartedAt"`
	State     string            `json:"State"`
	Status    string            `json:"Status"`
	Exited    bool              `json:"Exited"`
	ExitCode  int               `json:"ExitCode"`
	Restarts  int               `json:"Restarts"`
	IsInfra   bool              `json:"IsInfra"`
	Pod       string            `json:"PodName"`
	Labels    map[string]string `json:"Labels"`
	Mounts    []string          `json:"Mounts"`
	Networks  []string          `json:"Networks"`
	Health    flexHealth        `json:"Health"`
	Ports     []struct {
		HostIP        string `json:"host_ip"`
		HostPort      int    `json:"host_port"`
		ContainerPort int    `json:"container_port"`
		Range         int    `json:"range"`
		Protocol      string `json:"protocol"`
	} `json:"Ports"`
}

// ParseContainers reads `podman ps -a --format json`, which answers with an
// array. An empty store answers `[]`, which is a valid answer and not an error:
// a scope with no containers is an ordinary scope.
func ParseContainers(output string, scope container.Scope, now time.Time) (
	[]container.Container, error) {
	rows, err := decodeArray[psRow](output, "podman ps")
	if err != nil {
		return nil, err
	}
	out := make([]container.Container, 0, len(rows))
	for _, row := range rows {
		// An infra container is the pause process that holds a pod's
		// namespaces open. It runs nothing anyone wrote and it is not a
		// container in the sense this screen is about, so it is dropped rather
		// than shown as an unexplained extra row per pod.
		if row.IsInfra {
			continue
		}
		out = append(out, row.toContainer(scope, now))
	}
	return out, nil
}

// toContainer folds one row into the model.
func (r psRow) toContainer(scope container.Scope, now time.Time) container.Container {
	c := container.Container{
		ID:           r.ID,
		Name:         firstOf(r.Names),
		Target:       TargetFor(scope),
		Image:        r.Image,
		Command:      strings.Join(r.Command, " "),
		State:        container.State(strings.ToLower(strings.TrimSpace(r.State))),
		Status:       r.Status,
		Health:       parseHealth(r.Health.Status),
		RestartCount: r.Restarts,
		Labels:       r.Labels,
		Mounts:       r.Mounts,
		Networks:     r.Networks,
	}
	if r.Created > 0 {
		c.Created = time.Unix(r.Created, 0)
	}
	if r.StartedAt > 0 {
		c.Started = time.Unix(r.StartedAt, 0)
	}
	// Podman prints the second the container started, so the uptime is
	// computed rather than read out of a sentence. It is still a phrase, so
	// that the column reads the same whichever engine filled it.
	if c.State == container.StateRunning && !c.Started.IsZero() {
		c.Uptime = HumanDuration(now.Sub(c.Started))
	}
	// Podman reports an exit code for everything, including a container that
	// has never run, where it is a zero that means nothing. Only a container
	// that has actually exited has a code worth believing.
	if r.Exited || c.State == container.StateExited {
		c.ExitCode, c.ExitCodeKnown = r.ExitCode, true
	}
	if r.Pod != "" {
		c.Name += " (pod " + r.Pod + ")"
	}
	for _, port := range r.Ports {
		c.Ports = append(c.Ports, container.Port{
			HostIP:        publicIP(port.HostIP),
			HostPort:      port.HostPort,
			ContainerPort: port.ContainerPort,
			Protocol:      port.Protocol,
		})
	}
	applyComposeLabels(&c)
	if raw, err := json.Marshal(r); err == nil {
		c.Raw = string(raw)
	}
	return c
}

// publicIP drops the wildcard addresses, which say nothing a reader wants in a
// narrow column.
func publicIP(ip string) string {
	if ip == "0.0.0.0" || ip == "::" {
		return ""
	}
	return ip
}

// applyComposeLabels reads the Compose fields back out of the labels.
func applyComposeLabels(c *container.Container) {
	c.Project = c.Labels[LabelProject]
	c.Service = c.Labels[LabelService]
	c.WorkingDir = c.Labels[LabelWorkingDir]
}

// imageRow is one element of `podman images --format json`.
type imageRow struct {
	ID         string   `json:"Id"`
	Names      []string `json:"Names"`
	Size       int64    `json:"Size"`
	Created    int64    `json:"Created"`
	Containers int      `json:"Containers"`
}

// ParseImages reads `podman images --format json`.
func ParseImages(output string, scope container.Scope) ([]container.Image, error) {
	rows, err := decodeArray[imageRow](output, "podman images")
	if err != nil {
		return nil, err
	}
	out := make([]container.Image, 0, len(rows))
	for _, row := range rows {
		image := container.Image{
			ID:        short(row.ID),
			Target:    TargetFor(scope),
			SizeBytes: row.Size,
			SizeText:  HumanSize(row.Size),
			// Podman lists an image by every name that points at it, and an
			// image with no names left is exactly what dangling means.
			Dangling: len(row.Names) == 0,
		}
		if row.Created > 0 {
			image.Created = time.Unix(row.Created, 0)
		}
		if name := firstOf(row.Names); name != "" {
			image.Repository, image.Tag = SplitReference(name)
		}
		out = append(out, image)
	}
	return out, nil
}

// SplitReference separates an image reference into repository and tag. The
// colon of a registry port is not a tag separator, which is why this cannot be
// a plain LastIndex on the whole string.
func SplitReference(reference string) (repository, tag string) {
	slash := strings.LastIndexByte(reference, '/')
	colon := strings.LastIndexByte(reference, ':')
	if colon < 0 || colon < slash {
		return reference, ""
	}
	return reference[:colon], reference[colon+1:]
}

// volumeRow is one element of `podman volume ls --format json`.
type volumeRow struct {
	Name       string `json:"Name"`
	Driver     string `json:"Driver"`
	Mountpoint string `json:"Mountpoint"`
	Anonymous  bool   `json:"Anonymous"`
}

// ParseVolumes reads `podman volume ls --format json`.
func ParseVolumes(output string, scope container.Scope) ([]container.Volume, error) {
	rows, err := decodeArray[volumeRow](output, "podman volume ls")
	if err != nil {
		return nil, err
	}
	out := make([]container.Volume, 0, len(rows))
	for _, row := range rows {
		out = append(out, container.Volume{
			Name:       row.Name,
			Driver:     row.Driver,
			Mountpoint: row.Mountpoint,
			Target:     TargetFor(scope),
			Anonymous:  row.Anonymous,
		})
	}
	return out, nil
}

// networkRow is one element of `podman network ls --format json`, whose keys
// are lower-case where every other Podman command's are capitalised.
type networkRow struct {
	Name     string `json:"name"`
	ID       string `json:"id"`
	Driver   string `json:"driver"`
	Internal bool   `json:"internal"`
}

// DefaultNetwork is the network Podman creates for itself and will not remove.
const DefaultNetwork = "podman"

// ParseNetworks reads `podman network ls --format json`.
func ParseNetworks(output string, scope container.Scope) ([]container.Network, error) {
	rows, err := decodeArray[networkRow](output, "podman network ls")
	if err != nil {
		return nil, err
	}
	out := make([]container.Network, 0, len(rows))
	for _, row := range rows {
		out = append(out, container.Network{
			ID:       short(row.ID),
			Name:     row.Name,
			Driver:   row.Driver,
			Target:   TargetFor(scope),
			Internal: row.Internal,
			Builtin:  row.Name == DefaultNetwork,
		})
	}
	return out, nil
}

// infoDoc is the part of `podman info --format json` this tool reads.
type infoDoc struct {
	Host struct {
		CgroupVersion string `json:"cgroupVersion"`
		Security      struct {
			Rootless bool `json:"rootless"`
		} `json:"security"`
	} `json:"host"`
	Store struct {
		GraphDriverName string `json:"graphDriverName"`
		GraphRoot       string `json:"graphRoot"`
		ContainerStore  struct {
			Number  int `json:"number"`
			Paused  int `json:"paused"`
			Running int `json:"running"`
			Stopped int `json:"stopped"`
		} `json:"containerStore"`
		ImageStore struct {
			Number int `json:"number"`
		} `json:"imageStore"`
	} `json:"store"`
	Registries struct {
		Search []string `json:"search"`
	} `json:"registries"`
	Version struct {
		Version string `json:"Version"`
	} `json:"version"`
}

// ParseInfo reads `podman info --format json` into the engine summary.
func ParseInfo(output string, scope container.Scope) (container.EngineInfo, error) {
	var doc infoDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &doc); err != nil {
		return container.EngineInfo{}, fmt.Errorf(
			"podman: `podman info` did not parse: %w", err)
	}
	return container.EngineInfo{
		Target:    TargetFor(scope),
		Available: true,
		Installed: true,
		// Podman has no daemon, so the client is the server. Reporting the
		// same version in both places is the honest answer rather than a blank
		// column that invites the question.
		ServerVersion:    doc.Version.Version,
		StorageDriver:    doc.Store.GraphDriverName,
		CgroupVersion:    strings.TrimPrefix(doc.Host.CgroupVersion, "v"),
		Rootless:         doc.Host.Security.Rootless,
		Root:             doc.Store.GraphRoot,
		SearchRegistries: doc.Registries.Search,
		Containers:       doc.Store.ContainerStore.Number,
		Running:          doc.Store.ContainerStore.Running,
		Paused:           doc.Store.ContainerStore.Paused,
		Stopped:          doc.Store.ContainerStore.Stopped,
		Images:           doc.Store.ImageStore.Number,
	}, nil
}

// percentRe matches the parenthesised percentage `system df` appends to the
// reclaimable column.
var percentRe = regexp.MustCompile(`^\([0-9]+%\)$`)

// ParseDisk reads the table `podman system df` prints, which is the same five
// columns Docker prints.
func ParseDisk(output string) []container.DiskRow {
	var out []container.DiskRow
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		// The header, a blank line, and anything narrower than the five
		// columns are skipped. The type is one or two words ("Local Volumes"),
		// so the row is read from the right.
		if len(fields) < 5 || strings.EqualFold(fields[0], "TYPE") {
			continue
		}
		// The reclaimable column is two whitespace-separated tokens when the
		// engine appends a percentage — "5.289GB (96%)" — and one when it does
		// not. Rejoining them first is what lets the rest of the row be read
		// from the right.
		if last := fields[len(fields)-1]; percentRe.MatchString(last) {
			fields = append(fields[:len(fields)-2],
				fields[len(fields)-2]+" "+last)
		}
		if len(fields) < 5 {
			continue
		}
		tail := fields[len(fields)-4:]
		out = append(out, container.DiskRow{
			Type:        strings.Join(fields[:len(fields)-4], " "),
			Total:       tail[0],
			Active:      tail[1],
			Size:        tail[2],
			Reclaimable: tail[3],
		})
	}
	return out
}

// inspectDoc is the part of `podman inspect` this tool reads. Podman implements
// Docker's inspect schema, so the shape is the same one — which is what lets
// the detail screen be written once.
type inspectDoc struct {
	ID      string `json:"Id"`
	Created string `json:"Created"`
	State   struct {
		Status     string `json:"Status"`
		ExitCode   int    `json:"ExitCode"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		Health     *struct {
			Status        string `json:"Status"`
			FailingStreak int    `json:"FailingStreak"`
			Log           []struct {
				Start    string `json:"Start"`
				End      string `json:"End"`
				ExitCode int    `json:"ExitCode"`
				Output   string `json:"Output"`
			} `json:"Log"`
		} `json:"Health"`
	} `json:"State"`
	RestartCount int `json:"RestartCount"`
	Config       struct {
		Env        []string          `json:"Env"`
		Labels     map[string]string `json:"Labels"`
		Entrypoint flexList          `json:"Entrypoint"`
		Cmd        []string          `json:"Cmd"`
	} `json:"Config"`
	HostConfig struct {
		RestartPolicy struct {
			Name              string `json:"Name"`
			MaximumRetryCount int    `json:"MaximumRetryCount"`
		} `json:"RestartPolicy"`
		Memory            int64 `json:"Memory"`
		MemoryReservation int64 `json:"MemoryReservation"`
		NanoCpus          int64 `json:"NanoCpus"`
		CpuShares         int64 `json:"CpuShares"`
		PidsLimit         int64 `json:"PidsLimit"`
	} `json:"HostConfig"`
	Mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		Mode        string `json:"Mode"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress  string `json:"IPAddress"`
			Gateway    string `json:"Gateway"`
			MacAddress string `json:"MacAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

// flexList reads a field Podman prints as a list on one version and as a single
// string on another, which is what it does with Entrypoint.
type flexList []string

// UnmarshalJSON accepts a list, a string or null.
func (l *flexList) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	switch {
	case text == "null":
		*l = nil
		return nil
	case strings.HasPrefix(text, `"`):
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*l = strings.Fields(value)
		return nil
	default:
		var values []string
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		*l = values
		return nil
	}
}

// ParseInspect reads `podman inspect`, which answers with an array of one.
func ParseInspect(output string) (container.Detail, error) {
	docs, err := decodeArray[inspectDoc](output, "podman inspect")
	if err != nil {
		return container.Detail{}, err
	}
	if len(docs) == 0 {
		return container.Detail{}, fmt.Errorf(
			"podman: `podman inspect` returned nothing for this container")
	}
	doc := docs[0]

	detail := container.Detail{
		Raw:        strings.TrimSpace(output),
		Entrypoint: doc.Config.Entrypoint,
		Args:       doc.Config.Cmd,
		Env:        ParseEnv(doc.Config.Env),
		Limits: container.Limits{
			MemoryBytes:            doc.HostConfig.Memory,
			MemoryReservationBytes: doc.HostConfig.MemoryReservation,
			NanoCPUs:               doc.HostConfig.NanoCpus,
			CPUShares:              doc.HostConfig.CpuShares,
			PidsLimit:              doc.HostConfig.PidsLimit,
		},
	}
	for _, mount := range doc.Mounts {
		detail.Mounts = append(detail.Mounts, container.Mount{
			Type:        mount.Type,
			Source:      mount.Source,
			Destination: mount.Destination,
			Mode:        mount.Mode,
			RW:          mount.RW,
		})
	}
	for name, network := range doc.NetworkSettings.Networks {
		detail.Networks = append(detail.Networks, container.NetworkAttachment{
			Name:       name,
			IPAddress:  network.IPAddress,
			Gateway:    network.Gateway,
			MacAddress: network.MacAddress,
		})
	}
	sort.SliceStable(detail.Networks, func(i, j int) bool {
		return detail.Networks[i].Name < detail.Networks[j].Name
	})

	if health := doc.State.Health; health != nil {
		detail.Health.Status = parseHealth(health.Status)
		detail.Health.FailingStreak = health.FailingStreak
		for _, entry := range health.Log {
			detail.Health.Log = append(detail.Health.Log, container.HealthEntry{
				Start:    parseTime(entry.Start),
				End:      parseTime(entry.End),
				ExitCode: entry.ExitCode,
				Output:   entry.Output,
			})
		}
	}
	return detail, nil
}

// InspectContainer folds the parts of an inspect that also belong on the list
// row into a container, so the detail screen shows the same facts as the row it
// was opened from — read from a better source. The restart policy is the one
// that matters: `podman ps` does not report it at all.
func InspectContainer(base container.Container, output string) container.Container {
	docs, err := decodeArray[inspectDoc](output, "podman inspect")
	if err != nil || len(docs) == 0 {
		return base
	}
	doc := docs[0]
	if started := parseTime(doc.State.StartedAt); !started.IsZero() {
		base.Started = started
	}
	base.RestartCount = doc.RestartCount
	if policy := doc.HostConfig.RestartPolicy.Name; policy != "" {
		base.RestartPolicy = policy
		if policy == "on-failure" && doc.HostConfig.RestartPolicy.MaximumRetryCount > 0 {
			base.RestartPolicy += ":" +
				strconv.Itoa(doc.HostConfig.RestartPolicy.MaximumRetryCount)
		}
	}
	if len(doc.Config.Labels) > 0 {
		base.Labels = doc.Config.Labels
		applyComposeLabels(&base)
	}
	return base
}

// statsRow is one element of `podman stats --no-stream --format json`.
type statsRow struct {
	CPU      flexString `json:"CPU"`
	MemUsage flexString `json:"MemUsage"`
	MemPerc  flexString `json:"MemPerc"`
	NetIO    flexString `json:"NetIO"`
	BlockIO  flexString `json:"BlockIO"`
	PIDs     flexString `json:"PIDS"`
	// PIDsAlt is the same field under the spelling older Podman used.
	PIDsAlt flexString `json:"PIDs"`
}

// ParseStats reads one sample.
func ParseStats(output string) (container.Stats, error) {
	rows, err := decodeArray[statsRow](output, "podman stats")
	if err != nil {
		return container.Stats{}, err
	}
	if len(rows) == 0 {
		return container.Stats{}, fmt.Errorf(
			"podman: `podman stats` returned no sample for this container")
	}
	row := rows[0]
	pids := string(row.PIDs)
	if pids == "" {
		pids = string(row.PIDsAlt)
	}
	return container.Stats{
		CPUPercent: string(row.CPU),
		MemUsage:   string(row.MemUsage),
		MemPercent: string(row.MemPerc),
		NetIO:      string(row.NetIO),
		BlockIO:    string(row.BlockIO),
		PIDs:       pids,
	}, nil
}

// ParseEnv reads the NAME=VALUE list an inspect returns, masking the values
// whose names say they carry a secret. It is the same rule the Docker half
// applies, spelled here so the two packages stay independent of each other.
func ParseEnv(entries []string) []container.EnvVar {
	out := make([]container.EnvVar, 0, len(entries))
	for _, entry := range entries {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			name, value = entry, ""
		}
		variable := container.EnvVar{Name: name, Value: value}
		if SecretName(name) && value != "" {
			variable.Value, variable.Masked = Masked, true
		}
		out = append(out, variable)
	}
	return out
}

// secretWords are the substrings that mark an environment variable whose value
// is not shown.
//
// The test is on the *name* because that is the only thing about an environment
// variable that is reliably descriptive. The value is replaced and the name is
// still listed: knowing that DATABASE_PASSWORD is set, and that what is on
// screen is not it, is what a reader needs. It over-matches happily — a
// PUBLIC_KEY is masked too — because a mask on something harmless costs a
// keystroke and a leaked token costs a rotation.
var secretWords = []string{"PASS", "SECRET", "TOKEN", "KEY", "CREDENTIAL"}

// SecretName reports whether a variable's name says it carries a secret.
func SecretName(name string) bool {
	upper := strings.ToUpper(name)
	for _, word := range secretWords {
		if strings.Contains(upper, word) {
			return true
		}
	}
	return false
}

// QuadletDirs are where Podman looks for Quadlet files, per scope.
//
// A Quadlet file is a container written as a systemd unit: at boot, a generator
// turns each `.container` into a `.service`, and systemd runs it. They are read
// here and not written: changing one means regenerating and restarting a unit,
// which is systemd's business rather than this tool's, and doing half of that
// would be worse than saying where the file is.
func QuadletDirs(scope container.Scope) []string {
	if scope == container.ScopeSystem {
		return []string{
			"/etc/containers/systemd",
			"/usr/share/containers/systemd",
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	return []string{filepath.Join(config, "containers", "systemd")}
}

// quadletSuffixes are the file kinds a Quadlet generator turns into units. Only
// `.container` becomes a container; the others are listed because a reader
// looking for where a container came from needs to see the volume and network
// it was declared with.
var quadletSuffixes = []string{".container", ".volume", ".network", ".pod",
	".kube", ".image", ".build"}

// FindQuadlets lists the Quadlet files of one scope.
//
// It reads directories rather than asking Podman, because Podman has no command
// that lists them: `podman generate systemd` writes a unit from a *running*
// container, which is the old way of doing this and the opposite direction.
// A directory that cannot be read is skipped silently — on the system scope it
// is usually simply not there.
func FindQuadlets(scope container.Scope) []container.QuadletUnit {
	var out []container.QuadletUnit
	for _, dir := range QuadletDirs(scope) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !quadletSuffix(name) {
				continue
			}
			out = append(out, container.QuadletUnit{
				Path:  filepath.Join(dir, name),
				Name:  QuadletUnitName(name),
				Scope: scope,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// quadletSuffix reports whether a file name is one a Quadlet generator reads.
func quadletSuffix(name string) bool {
	for _, suffix := range quadletSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// QuadletUnitName is the systemd unit a Quadlet file generates. The rule is the
// generator's: `web.container` becomes `web.service`, and every other kind
// becomes a unit named after its own type.
func QuadletUnitName(file string) string {
	base := strings.TrimSuffix(file, filepath.Ext(file))
	switch filepath.Ext(file) {
	case ".container", ".kube", ".build":
		return base + ".service"
	case ".volume":
		return base + "-volume.service"
	case ".network":
		return base + "-network.service"
	case ".pod":
		return base + "-pod.service"
	case ".image":
		return base + "-image.service"
	default:
		return base
	}
}

// ComposeFilesFor reads the compose files a project was built from, out of the
// label Compose wrote on its containers.
func ComposeFilesFor(p container.Project) []string {
	for _, c := range p.Containers {
		if files := c.Labels[LabelConfigFiles]; files != "" {
			var out []string
			for _, file := range strings.Split(files, ",") {
				if file = strings.TrimSpace(file); file != "" {
					out = append(out, file)
				}
			}
			return out
		}
	}
	return nil
}

// decodeArray reads one of Podman's JSON arrays, naming the command in the
// error so a parse failure says which read produced it.
func decodeArray[T any](output, what string) ([]T, error) {
	text := strings.TrimSpace(output)
	if text == "" {
		return nil, nil
	}
	var rows []T
	if err := json.Unmarshal([]byte(text), &rows); err != nil {
		return nil, fmt.Errorf("podman: `%s` did not parse: %w", what, err)
	}
	return rows, nil
}

// parseHealth reads a health word, whose "none" and empty both mean a container
// that declares no check at all.
func parseHealth(value string) container.Health {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "healthy":
		return container.HealthHealthy
	case "unhealthy":
		return container.HealthUnhealthy
	case "starting":
		return container.HealthStarting
	default:
		return container.HealthNone
	}
}

// parseTime reads the RFC 3339 timestamps an inspect returns.
func parseTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "0001-01-01") {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// firstOf takes the first element of a list, or the empty string.
func firstOf(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

// short truncates a full 64-character id to the 12 characters both engines
// print, so an id read from Podman lines up with one read from Docker.
func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// HumanSize renders a byte count the way both engines do, in decimal units.
// Podman reports a number where Docker reports a phrase, and the column has to
// read the same either way.
func HumanSize(bytes int64) string {
	const unit = 1000
	if bytes < unit {
		return strconv.FormatInt(bytes, 10) + "B"
	}
	value := float64(bytes)
	units := []string{"kB", "MB", "GB", "TB", "PB"}
	index := -1
	for value >= unit && index < len(units)-1 {
		value /= unit
		index++
	}
	if value >= 100 {
		return strconv.FormatFloat(value, 'f', 0, 64) + units[index]
	}
	return strconv.FormatFloat(value, 'f', 2, 64) + units[index]
}

// HumanDuration renders an uptime the way both engines phrase it, in the single
// largest unit that fits.
func HumanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return plural(int(d.Seconds()), "second")
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour")
	case d < 365*24*time.Hour:
		return plural(int(d.Hours()/24), "day")
	default:
		return plural(int(d.Hours()/24/365), "year")
	}
}

// plural renders a count with its unit.
func plural(count int, unit string) string {
	text := strconv.Itoa(count) + " " + unit
	if count != 1 {
		text += "s"
	}
	return text
}

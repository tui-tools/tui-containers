package docker

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tui-tools/tui-containers/internal/container"
)

// This file turns what the Docker CLI prints into the neutral model. It starts
// no process: everything here takes the text a command returned and gives back
// values, so every parser is a table test against captured output.

// Target is the one target this package produces. Docker has a single scope:
// dockerd owns every container on the machine, and who may talk to it is a
// question about the socket rather than about the container.
var Target = container.Target{Engine: container.EngineDocker}

// timeLayouts are the shapes Docker prints a timestamp in. The first is what
// the list commands use; the second is the RFC 3339 form `inspect` returns.
var timeLayouts = []string{
	"2006-01-02 15:04:05 -0700 MST",
	"2006-01-02 15:04:05 -0700",
	time.RFC3339Nano,
	time.RFC3339,
}

// parseTime reads a Docker timestamp, returning the zero time for anything it
// cannot read — which is shown as "—" rather than as a wrong date.
func parseTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "0001-01-01") {
		return time.Time{}
	}
	for _, layout := range timeLayouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// psRow is one line of `docker ps --format json`.
type psRow struct {
	Command      string `json:"Command"`
	CreatedAt    string `json:"CreatedAt"`
	HealthStatus string `json:"HealthStatus"`
	ID           string `json:"ID"`
	Image        string `json:"Image"`
	Labels       string `json:"Labels"`
	Mounts       string `json:"Mounts"`
	Names        string `json:"Names"`
	Networks     string `json:"Networks"`
	Ports        string `json:"Ports"`
	State        string `json:"State"`
	Status       string `json:"Status"`
}

// ParseContainers reads `docker ps -a --format json`, which prints one JSON
// object per line rather than an array. A blank line is skipped and a line that
// does not parse is reported, because a list that silently dropped a container
// would be the one failure this screen must never have.
func ParseContainers(output string) ([]container.Container, error) {
	var out []container.Container
	for number, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row psRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("docker: line %d of `docker ps` did not parse: %w",
				number+1, err)
		}
		out = append(out, row.toContainer(line))
	}
	return out, nil
}

// toContainer folds one row into the model.
func (r psRow) toContainer(raw string) container.Container {
	labels := ParseLabels(r.Labels)
	c := container.Container{
		ID:      r.ID,
		Name:    firstName(r.Names),
		Target:  Target,
		Image:   r.Image,
		Command: strings.Trim(r.Command, `"`),
		State:   container.State(strings.ToLower(strings.TrimSpace(r.State))),
		Status:  r.Status,
		Health:  parseHealth(r.HealthStatus),
		Created: parseTime(r.CreatedAt),
		Ports:   ParsePorts(r.Ports),
		Labels:  labels,
		Raw:     raw,
	}
	c.Uptime = UptimeFromStatus(r.Status)
	if code, ok := ExitCodeFromStatus(r.Status); ok {
		c.ExitCode, c.ExitCodeKnown = code, true
	}
	c.Networks = splitList(r.Networks)
	c.Mounts = splitList(r.Mounts)
	applyComposeLabels(&c)
	return c
}

// applyComposeLabels reads the Compose fields back out of the labels. They are
// the same three labels on both engines, because Podman writes Docker's.
func applyComposeLabels(c *container.Container) {
	c.Project = c.Labels[LabelProject]
	c.Service = c.Labels[LabelService]
	c.WorkingDir = c.Labels[LabelWorkingDir]
}

// The Compose labels this tool reads. They are Docker's names on both engines:
// Compose writes them whichever engine it is talking to, and podman-compose
// writes the same ones.
const (
	LabelProject     = "com.docker.compose.project"
	LabelService     = "com.docker.compose.service"
	LabelWorkingDir  = "com.docker.compose.project.working_dir"
	LabelConfigFiles = "com.docker.compose.project.config_files"
)

// labelNameRe is what a label key looks like. It is used to decide where one
// label ends and the next begins in the flattened list Docker prints.
var labelNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*=`)

// ParseLabels reads the label list `docker ps` prints, which is every label
// joined with commas into one string.
//
// That format is lossy and there is no version of it that is not: a label value
// may itself contain a comma, and Docker escapes nothing. The rule here is the
// only one available — a fragment that looks like `name=` starts a new label,
// and one that does not is the rest of the previous value. It reunites the
// commas inside a description and it would still mis-split a value that
// happens to contain `, something=`; the alternative is one `inspect` per
// container on every reload, which on a machine with sixty containers is sixty
// processes to read a column.
func ParseLabels(text string) map[string]string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	labels := map[string]string{}
	var key string
	for _, fragment := range strings.Split(text, ",") {
		if labelNameRe.MatchString(fragment) {
			name, value, _ := strings.Cut(fragment, "=")
			labels[name], key = value, name
			continue
		}
		if key != "" {
			labels[key] += "," + fragment
		}
	}
	return labels
}

// portRe reads one mapping of the Ports column: an optional host side, the
// container port and the protocol.
var portRe = regexp.MustCompile(
	`^(?:(\[[0-9a-fA-F:]+\]|[0-9.]+):([0-9]+)->)?([0-9]+)/([a-z]+)$`)

// ParsePorts reads the Ports column, which is the published mappings joined
// with commas: "0.0.0.0:8080->80/tcp, [::]:8080->80/tcp".
//
// The two entries above are one mapping published on both address families,
// and they are folded into one: a reader wants to know that 8080 reaches 80,
// not that the machine has two stacks.
func ParsePorts(text string) []container.Port {
	var out []container.Port
	seen := map[string]bool{}
	for _, part := range strings.Split(text, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		match := portRe.FindStringSubmatch(part)
		if match == nil {
			continue
		}
		port := container.Port{Protocol: match[4]}
		port.ContainerPort, _ = strconv.Atoi(match[3])
		if match[2] != "" {
			port.HostPort, _ = strconv.Atoi(match[2])
			if ip := strings.Trim(match[1], "[]"); ip != "::" && ip != "0.0.0.0" {
				port.HostIP = ip
			}
		}
		key := port.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, port)
	}
	return out
}

// exitRe reads the exit status out of a status sentence: "Exited (137) 17
// hours ago".
var exitRe = regexp.MustCompile(`Exited \((-?[0-9]+)\)`)

// ExitCodeFromStatus reads how a container's last run ended, and whether the
// status said at all.
func ExitCodeFromStatus(status string) (int, bool) {
	match := exitRe.FindStringSubmatch(status)
	if match == nil {
		return 0, false
	}
	code, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	return code, true
}

// upRe reads the uptime phrase out of a status sentence: "Up 3 hours
// (healthy)" gives "3 hours".
var upRe = regexp.MustCompile(`^Up ([^(]+?)\s*(?:\(|$)`)

// UptimeFromStatus is how long a running container has been up, in the
// engine's own words. Anything that is not running has none.
func UptimeFromStatus(status string) string {
	match := upRe.FindStringSubmatch(strings.TrimSpace(status))
	if match == nil {
		return ""
	}
	return strings.TrimSpace(match[1])
}

// parseHealth reads the health column, whose "none" is Docker's word for a
// container that declares no check at all.
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

// firstName takes the first of the comma-separated names a container answers
// to, which is the one every other command prints.
func firstName(names string) string {
	name, _, _ := strings.Cut(names, ",")
	return strings.TrimSpace(name)
}

// splitList reads one of the comma-separated columns into a slice, dropping the
// empties.
func splitList(text string) []string {
	var out []string
	for _, part := range strings.Split(text, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// imageRow is one line of `docker images --format json`.
type imageRow struct {
	Containers string `json:"Containers"`
	CreatedAt  string `json:"CreatedAt"`
	ID         string `json:"ID"`
	Repository string `json:"Repository"`
	Size       string `json:"Size"`
	Tag        string `json:"Tag"`
}

// ParseImages reads `docker images --format json`, one object per line.
func ParseImages(output string) ([]container.Image, error) {
	var out []container.Image
	for number, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row imageRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf(
				"docker: line %d of `docker images` did not parse: %w", number+1, err)
		}
		image := container.Image{
			ID:         row.ID,
			Repository: row.Repository,
			Tag:        row.Tag,
			Target:     Target,
			Created:    parseTime(row.CreatedAt),
			SizeText:   row.Size,
			SizeBytes:  ParseSize(row.Size),
			// A dangling image is one no tag points at any more: the layers a
			// rebuild left behind. Docker prints both halves as <none>.
			Dangling: none(row.Repository) && none(row.Tag),
		}
		out = append(out, image)
	}
	return out, nil
}

// none reports Docker's word for an absent value.
func none(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || value == "<none>"
}

// sizeRe reads a human size: a number, an optional unit, and whatever follows.
var sizeRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([kKmMgGtT]?)i?[bB]?`)

// sizeUnits is what each unit multiplies by. Docker prints decimal units — a
// "1.63GB" image is 1.63 × 10⁹ bytes, not 2³⁰ — so the table is decimal.
var sizeUnits = map[string]float64{
	"": 1, "k": 1e3, "m": 1e6, "g": 1e9, "t": 1e12,
}

// ParseSize reads a human size into bytes, for sorting. It returns 0 for
// anything it cannot read, and 0 sorts last, which is the right place for a
// size nobody could establish.
func ParseSize(text string) int64 {
	match := sizeRe.FindStringSubmatch(strings.TrimSpace(text))
	if match == nil {
		return 0
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0
	}
	return int64(value * sizeUnits[strings.ToLower(match[2])])
}

// volumeRow is one line of `docker volume ls --format json`.
type volumeRow struct {
	Driver     string `json:"Driver"`
	Labels     string `json:"Labels"`
	Mountpoint string `json:"Mountpoint"`
	Name       string `json:"Name"`
}

// anonymousLabel is what Docker marks a volume it named itself with.
const anonymousLabel = "com.docker.volume.anonymous"

// ParseVolumes reads `docker volume ls --format json`.
func ParseVolumes(output string) ([]container.Volume, error) {
	var out []container.Volume
	for number, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row volumeRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf(
				"docker: line %d of `docker volume ls` did not parse: %w",
				number+1, err)
		}
		_, anonymous := ParseLabels(row.Labels)[anonymousLabel]
		out = append(out, container.Volume{
			Name:       row.Name,
			Driver:     row.Driver,
			Mountpoint: row.Mountpoint,
			Target:     Target,
			Anonymous:  anonymous,
		})
	}
	return out, nil
}

// networkRow is one line of `docker network ls --format json`.
type networkRow struct {
	Driver   string `json:"Driver"`
	ID       string `json:"ID"`
	Internal string `json:"Internal"`
	Name     string `json:"Name"`
}

// builtinNetworks are the ones Docker creates for itself and will not remove.
var builtinNetworks = map[string]bool{"bridge": true, "host": true, "none": true}

// ParseNetworks reads `docker network ls --format json`.
func ParseNetworks(output string) ([]container.Network, error) {
	var out []container.Network
	for number, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row networkRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf(
				"docker: line %d of `docker network ls` did not parse: %w",
				number+1, err)
		}
		out = append(out, container.Network{
			ID:       row.ID,
			Name:     row.Name,
			Driver:   row.Driver,
			Target:   Target,
			Internal: row.Internal == "true",
			Builtin:  builtinNetworks[row.Name],
		})
	}
	return out, nil
}

// infoDoc is the part of `docker info --format json` this tool reads.
type infoDoc struct {
	ServerVersion     string   `json:"ServerVersion"`
	Driver            string   `json:"Driver"`
	CgroupVersion     string   `json:"CgroupVersion"`
	DockerRootDir     string   `json:"DockerRootDir"`
	Containers        int      `json:"Containers"`
	ContainersRunning int      `json:"ContainersRunning"`
	ContainersPaused  int      `json:"ContainersPaused"`
	ContainersStopped int      `json:"ContainersStopped"`
	Images            int      `json:"Images"`
	SecurityOptions   []string `json:"SecurityOptions"`
	RegistryConfig    struct {
		Mirrors []string `json:"Mirrors"`
	} `json:"RegistryConfig"`
}

// ParseInfo reads `docker info --format json` into the engine summary.
func ParseInfo(output string) (container.EngineInfo, error) {
	var doc infoDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &doc); err != nil {
		return container.EngineInfo{}, fmt.Errorf(
			"docker: `docker info` did not parse: %w", err)
	}
	info := container.EngineInfo{
		Target:          Target,
		Available:       true,
		Installed:       true,
		ServerVersion:   doc.ServerVersion,
		StorageDriver:   doc.Driver,
		CgroupVersion:   doc.CgroupVersion,
		Root:            doc.DockerRootDir,
		Containers:      doc.Containers,
		Running:         doc.ContainersRunning,
		Paused:          doc.ContainersPaused,
		Stopped:         doc.ContainersStopped,
		Images:          doc.Images,
		RegistryMirrors: doc.RegistryConfig.Mirrors,
	}
	// Rootless Docker announces itself in the security options, which is the
	// only place the daemon says so. It changes what a reader should expect:
	// ports below 1024, the storage location and the network stack all differ.
	for _, option := range doc.SecurityOptions {
		if strings.Contains(option, "rootless") {
			info.Rootless = true
		}
	}
	return info, nil
}

// percentRe matches the parenthesised percentage `system df` appends to the
// reclaimable column.
var percentRe = regexp.MustCompile(`^\([0-9]+%\)$`)

// ParseDisk reads the table `docker system df` prints, which is the same five
// columns on Docker and on Podman.
func ParseDisk(output string) []container.DiskRow {
	var out []container.DiskRow
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		// The header, a blank line, and anything narrower than the five
		// columns are skipped. The type is one or two words ("Local Volumes",
		// "Build Cache"), so the row is read from the right.
		if len(fields) < 5 || strings.EqualFold(fields[0], "TYPE") {
			continue
		}
		// The reclaimable column is two whitespace-separated tokens when the
		// engine appends a percentage — "9.201GB (44%)" — and one when it does
		// not, which is what the build cache row looks like. Rejoining them
		// first is what lets the rest of the row be read from the right.
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

// inspectDoc is the part of `docker inspect` this tool reads. Both engines
// answer with the same shape here, because Podman implements Docker's schema.
type inspectDoc struct {
	ID      string   `json:"Id"`
	Created string   `json:"Created"`
	Path    string   `json:"Path"`
	Args    []string `json:"Args"`
	State   struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		Paused     bool   `json:"Paused"`
		Restarting bool   `json:"Restarting"`
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
		Entrypoint []string          `json:"Entrypoint"`
		Cmd        []string          `json:"Cmd"`
	} `json:"Config"`
	HostConfig struct {
		RestartPolicy struct {
			Name              string `json:"Name"`
			MaximumRetryCount int    `json:"MaximumRetryCount"`
		} `json:"RestartPolicy"`
		Memory            int64  `json:"Memory"`
		MemoryReservation int64  `json:"MemoryReservation"`
		NanoCpus          int64  `json:"NanoCpus"`
		CpuShares         int64  `json:"CpuShares"`
		PidsLimit         *int64 `json:"PidsLimit"`
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

// ParseInspect reads `docker inspect`, which answers with an array of one.
func ParseInspect(output string) (container.Detail, error) {
	var docs []inspectDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &docs); err != nil {
		return container.Detail{}, fmt.Errorf(
			"docker: `docker inspect` did not parse: %w", err)
	}
	if len(docs) == 0 {
		return container.Detail{}, fmt.Errorf(
			"docker: `docker inspect` returned nothing for this container")
	}
	doc := docs[0]

	detail := container.Detail{
		Raw:        strings.TrimSpace(output),
		Entrypoint: doc.Config.Entrypoint,
		Args:       doc.Config.Cmd,
		Limits: container.Limits{
			MemoryBytes:            doc.HostConfig.Memory,
			MemoryReservationBytes: doc.HostConfig.MemoryReservation,
			NanoCPUs:               doc.HostConfig.NanoCpus,
			CPUShares:              doc.HostConfig.CpuShares,
		},
	}
	if doc.HostConfig.PidsLimit != nil {
		detail.Limits.PidsLimit = *doc.HostConfig.PidsLimit
	}
	detail.Env = ParseEnv(doc.Config.Env)

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
	sortAttachments(detail.Networks)

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
// was opened from — read from a better source.
func InspectContainer(base container.Container, output string) container.Container {
	var docs []inspectDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &docs); err != nil ||
		len(docs) == 0 {
		return base
	}
	doc := docs[0]
	base.Started = parseTime(doc.State.StartedAt)
	base.RestartCount = doc.RestartCount
	base.ExitCode, base.ExitCodeKnown = doc.State.ExitCode, true
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

// secretRe is the name of a variable whose value is not shown.
//
// It matches on the *name* because that is the only thing about an environment
// variable that is reliably descriptive. The value is replaced and the name is
// still listed: knowing that DATABASE_PASSWORD is set, and that what is on
// screen is not it, is what a reader needs. It over-matches happily — a
// PUBLIC_KEY is masked too — because a mask on something harmless costs a
// keystroke and a leaked token costs a rotation.
var secretRe = regexp.MustCompile(`(?i)(PASS|SECRET|TOKEN|KEY|CREDENTIAL)`)

// Masked is the placeholder shown in place of a secret.
const Masked = "••••••••"

// ParseEnv reads the NAME=VALUE list an inspect returns, masking the values
// whose names say they carry a secret.
func ParseEnv(entries []string) []container.EnvVar {
	out := make([]container.EnvVar, 0, len(entries))
	for _, entry := range entries {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			name, value = entry, ""
		}
		variable := container.EnvVar{Name: name, Value: value}
		if secretRe.MatchString(name) && value != "" {
			variable.Value, variable.Masked = Masked, true
		}
		out = append(out, variable)
	}
	return out
}

// statsRow is one line of `docker stats --no-stream --format json`.
type statsRow struct {
	BlockIO  string `json:"BlockIO"`
	CPUPerc  string `json:"CPUPerc"`
	MemPerc  string `json:"MemPerc"`
	MemUsage string `json:"MemUsage"`
	NetIO    string `json:"NetIO"`
	PIDs     string `json:"PIDs"`
}

// ParseStats reads one sample. The command prints one object per container and
// is asked for one, so the first line is the answer.
func ParseStats(output string) (container.Stats, error) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row statsRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return container.Stats{}, fmt.Errorf(
				"docker: `docker stats` did not parse: %w", err)
		}
		return container.Stats{
			CPUPercent: row.CPUPerc,
			MemUsage:   row.MemUsage,
			MemPercent: row.MemPerc,
			NetIO:      row.NetIO,
			BlockIO:    row.BlockIO,
			PIDs:       row.PIDs,
		}, nil
	}
	return container.Stats{}, fmt.Errorf("docker: `docker stats` returned nothing")
}

// ComposeFilesFor reads the compose files a project was built from, out of the
// label Compose wrote on its containers.
func ComposeFilesFor(p container.Project) []string {
	for _, c := range p.Containers {
		if files := c.Labels[LabelConfigFiles]; files != "" {
			return splitList(files)
		}
	}
	return nil
}

// sortAttachments puts the networks in a stable order, because a Go map is not
// one and a detail screen whose lines move between reads is a detail screen
// nobody trusts.
func sortAttachments(list []container.NetworkAttachment) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].Name < list[j-1].Name; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

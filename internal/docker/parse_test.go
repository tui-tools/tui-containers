package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tui-tools/tui-containers/internal/container"
)

// The fixtures in testdata are what a real Fedora 42 host printed: `docker ps`,
// `docker images`, `docker volume ls`, `docker network ls`, `docker info`,
// `docker system df` and `docker inspect` all answer to any account in the
// `docker` group, so those files are captured output with the project paths and
// container names rewritten to neutral ones. Only stats.json is written by hand
// and says so: the host had no running container to sample, and a fixture
// nobody can reproduce is worse than one that names where it came from.

// read loads a fixture.
func read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	return string(data)
}

// TestParseContainers is the assertion the whole containers screen rests on: a
// stream of one JSON object per line becomes rows, and every field the columns
// show is read from the one the engine actually filled.
func TestParseContainers(t *testing.T) {
	containers, err := ParseContainers(read(t, "ps.json"))
	if err != nil {
		t.Fatalf("ParseContainers: %v", err)
	}
	if len(containers) != 4 {
		t.Fatalf("parsed %d containers, want 4", len(containers))
	}

	first := containers[0]
	if first.Name != "shopfront-db" {
		t.Errorf("name = %q", first.Name)
	}
	if first.ID != "3589b88586f6" {
		t.Errorf("id = %q", first.ID)
	}
	if first.Image != "postgres:17" {
		t.Errorf("image = %q", first.Image)
	}
	if first.State != container.StateExited {
		t.Errorf("state = %q", first.State)
	}
	// The status sentence is never rewritten: the column shows what the engine
	// said, and the exit code is read out of it rather than replacing it.
	if !strings.HasPrefix(first.Status, "Exited (137)") {
		t.Errorf("status = %q", first.Status)
	}
	if !first.ExitCodeKnown || first.ExitCode != 137 {
		t.Errorf("exit code = %d (known %v), want 137",
			first.ExitCode, first.ExitCodeKnown)
	}
	if first.Target != Target {
		t.Errorf("target = %v", first.Target)
	}
	if first.Created.IsZero() {
		t.Errorf("the created timestamp was not read")
	}
	// The Compose fields come out of the labels, which is what makes the
	// grouping on the containers screen possible at all.
	if first.Project != "shopfront" || first.Service != "postgres" {
		t.Errorf("compose = %q / %q", first.Project, first.Service)
	}
	if first.WorkingDir != "/srv/shopfront" {
		t.Errorf("working dir = %q", first.WorkingDir)
	}
}

// TestParseContainersRejectsBrokenLines: a list that silently dropped a
// container would be the one failure this screen must never have, so a line
// that does not parse is an error naming the line.
func TestParseContainersRejectsBrokenLines(t *testing.T) {
	_, err := ParseContainers("{\"ID\":\"a\"}\nnot json at all\n")
	if err == nil {
		t.Fatal("a line of rubbish parsed cleanly")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("the error does not name the line: %v", err)
	}
}

// TestParseContainersSkipsBlankLines: the engine ends its output with a
// newline, and an empty store prints nothing at all.
func TestParseContainersSkipsBlankLines(t *testing.T) {
	containers, err := ParseContainers("\n\n")
	if err != nil {
		t.Fatalf("ParseContainers: %v", err)
	}
	if len(containers) != 0 {
		t.Errorf("parsed %d containers from nothing", len(containers))
	}
}

// TestParseLabels covers the format Docker gives us and the one thing it
// cannot express.
func TestParseLabels(t *testing.T) {
	labels := ParseLabels("com.docker.compose.project=shopfront," +
		"com.docker.compose.service=web," +
		"description=a thing, and another thing," +
		"org.opencontainers.image.version=24.04")
	if labels[LabelProject] != "shopfront" {
		t.Errorf("project = %q", labels[LabelProject])
	}
	if labels[LabelService] != "web" {
		t.Errorf("service = %q", labels[LabelService])
	}
	// The comma inside a value is put back, because the fragment after it does
	// not look like a new label name.
	if labels["description"] != "a thing, and another thing" {
		t.Errorf("description = %q", labels["description"])
	}
	if labels["org.opencontainers.image.version"] != "24.04" {
		t.Errorf("version = %q", labels["org.opencontainers.image.version"])
	}
	if ParseLabels("") != nil {
		t.Errorf("an empty label list produced a map")
	}
}

// TestParsePorts folds a mapping published on both address families into one,
// because a reader wants to know that 8080 reaches 80, not that the machine
// has two stacks.
func TestParsePorts(t *testing.T) {
	ports := ParsePorts("0.0.0.0:8080->80/tcp, [::]:8080->80/tcp, " +
		"127.0.0.1:5432->5432/tcp, 9000/tcp")
	want := []string{"8080->80/tcp", "127.0.0.1:5432->5432/tcp", "9000/tcp"}
	if len(ports) != len(want) {
		t.Fatalf("parsed %d ports, want %d: %v", len(ports), len(want), ports)
	}
	for i, expected := range want {
		if got := ports[i].String(); got != expected {
			t.Errorf("port %d = %q, want %q", i, got, expected)
		}
	}
	if got := ParsePorts(""); got != nil {
		t.Errorf("an empty ports column produced %v", got)
	}
}

// TestUptimeAndExitCode reads the two things the status sentence carries and
// nothing else does.
func TestUptimeAndExitCode(t *testing.T) {
	tests := []struct {
		status string
		uptime string
		code   int
		known  bool
	}{
		{"Up 6 days (healthy)", "6 days", 0, false},
		{"Up 3 hours", "3 hours", 0, false},
		{"Up About a minute (health: starting)", "About a minute", 0, false},
		{"Exited (137) 17 hours ago", "", 137, true},
		{"Exited (0) 6 weeks ago", "", 0, true},
		{"Restarting (1) 12 seconds ago", "", 0, false},
		{"Created", "", 0, false},
	}
	for _, test := range tests {
		if got := UptimeFromStatus(test.status); got != test.uptime {
			t.Errorf("UptimeFromStatus(%q) = %q, want %q",
				test.status, got, test.uptime)
		}
		code, known := ExitCodeFromStatus(test.status)
		if known != test.known || (known && code != test.code) {
			t.Errorf("ExitCodeFromStatus(%q) = %d, %v; want %d, %v",
				test.status, code, known, test.code, test.known)
		}
	}
}

// TestParseImages reads the store, including the two halves of what "dangling"
// means.
func TestParseImages(t *testing.T) {
	images, err := ParseImages(read(t, "images.json"))
	if err != nil {
		t.Fatalf("ParseImages: %v", err)
	}
	if len(images) == 0 {
		t.Fatal("no images were parsed")
	}
	for _, image := range images {
		if image.ID == "" {
			t.Errorf("an image was parsed with no id: %+v", image)
		}
		if image.SizeText != "" && image.SizeBytes == 0 {
			t.Errorf("%s: size %q did not convert to bytes",
				image.Name(), image.SizeText)
		}
		if image.Target != Target {
			t.Errorf("%s: target = %v", image.Name(), image.Target)
		}
	}
}

// TestDanglingIsBothHalves: Docker prints an untagged image as <none>:<none>,
// and a tagged one with a <none> digest is not dangling.
func TestDanglingIsBothHalves(t *testing.T) {
	images, err := ParseImages(
		`{"ID":"aaa","Repository":"<none>","Tag":"<none>","Size":"182MB"}` + "\n" +
			`{"ID":"bbb","Repository":"postgres","Tag":"17","Size":"454MB"}`)
	if err != nil {
		t.Fatalf("ParseImages: %v", err)
	}
	if !images[0].Dangling {
		t.Errorf("an untagged image is not marked dangling")
	}
	if images[1].Dangling {
		t.Errorf("a tagged image was marked dangling")
	}
	if got := images[0].Name(); got != "aaa" {
		t.Errorf("a dangling image is named %q, want its id", got)
	}
	if got := images[1].Name(); got != "postgres:17" {
		t.Errorf("name = %q", got)
	}
}

// TestParseSize covers the units Docker prints, which are decimal.
func TestParseSize(t *testing.T) {
	tests := map[string]int64{
		"7.45MB":  7_450_000,
		"1.63GB":  1_630_000_000,
		"398MB":   398_000_000,
		"63B":     63,
		"0B":      0,
		"":        0,
		"nothing": 0,
	}
	for text, want := range tests {
		if got := ParseSize(text); got != want {
			t.Errorf("ParseSize(%q) = %d, want %d", text, got, want)
		}
	}
}

// TestParseVolumesAndNetworks reads the two lists the storage screen is built
// from, including the flags that decide what may be removed.
func TestParseVolumesAndNetworks(t *testing.T) {
	volumes, err := ParseVolumes(read(t, "volume-ls.json"))
	if err != nil {
		t.Fatalf("ParseVolumes: %v", err)
	}
	if len(volumes) == 0 {
		t.Fatal("no volumes were parsed")
	}
	for _, volume := range volumes {
		if volume.Name == "" || volume.Mountpoint == "" {
			t.Errorf("a volume was parsed with no name or mountpoint: %+v", volume)
		}
	}

	networks, err := ParseNetworks(read(t, "network-ls.json"))
	if err != nil {
		t.Fatalf("ParseNetworks: %v", err)
	}
	var builtin int
	for _, network := range networks {
		if network.Name == "" {
			t.Errorf("a network was parsed with no name: %+v", network)
		}
		if network.Builtin {
			builtin++
		}
	}
	// bridge and host are in the fixture, and both are networks Docker made
	// for itself and will not remove.
	if builtin < 2 {
		t.Errorf("%d built-in networks recognised, want at least 2", builtin)
	}
}

// TestParseInfo reads the engine summary, including the rootless flag that is
// only ever announced in the security options.
func TestParseInfo(t *testing.T) {
	info, err := ParseInfo(read(t, "info.json"))
	if err != nil {
		t.Fatalf("ParseInfo: %v", err)
	}
	if !info.Available || !info.Installed {
		t.Errorf("an info that parsed reports the engine as unavailable")
	}
	if info.ServerVersion == "" || info.StorageDriver == "" {
		t.Errorf("info = %+v", info)
	}
	if info.CgroupVersion != "2" {
		t.Errorf("cgroup version = %q", info.CgroupVersion)
	}
	if info.Containers == 0 {
		t.Errorf("the container count was not read")
	}
}

func TestParseInfoDetectsRootless(t *testing.T) {
	info, err := ParseInfo(
		`{"ServerVersion":"27.3.1","Driver":"overlay2",` +
			`"SecurityOptions":["name=seccomp","name=rootless"]}`)
	if err != nil {
		t.Fatalf("ParseInfo: %v", err)
	}
	if !info.Rootless {
		t.Errorf("a rootless daemon was not recognised")
	}
}

// TestParseDisk reads the five-column table, whose type column is one or two
// words — which is why the row is read from the right.
func TestParseDisk(t *testing.T) {
	rows := ParseDisk(read(t, "system-df.txt"))
	if len(rows) != 4 {
		t.Fatalf("parsed %d rows, want 4: %+v", len(rows), rows)
	}
	want := []string{"Images", "Containers", "Local Volumes", "Build Cache"}
	for i, name := range want {
		if rows[i].Type != name {
			t.Errorf("row %d type = %q, want %q", i, rows[i].Type, name)
		}
		if rows[i].Size == "" || rows[i].Reclaimable == "" {
			t.Errorf("row %d is missing a column: %+v", i, rows[i])
		}
	}
}

// TestParseInspect reads the detail screen's whole content out of one command.
func TestParseInspect(t *testing.T) {
	detail, err := ParseInspect(read(t, "inspect.json"))
	if err != nil {
		t.Fatalf("ParseInspect: %v", err)
	}
	if len(detail.Env) == 0 {
		t.Errorf("no environment was read")
	}
	if len(detail.Mounts) == 0 {
		t.Errorf("no mounts were read")
	}
	if len(detail.Networks) == 0 {
		t.Errorf("no networks were read")
	}
	for _, network := range detail.Networks {
		if network.Name == "" {
			t.Errorf("a network attachment has no name: %+v", network)
		}
	}
}

// TestInspectContainerFillsTheRestartPolicy: the list command does not report
// it, and it is the thing the o key changes.
func TestInspectContainerFillsTheRestartPolicy(t *testing.T) {
	base := container.Container{ID: "3589b88586f6", Name: "shopfront-db"}
	filled := InspectContainer(base, read(t, "inspect.json"))
	if filled.RestartPolicy == "" {
		t.Errorf("the restart policy was not read out of the inspect")
	}
	if !filled.ExitCodeKnown {
		t.Errorf("the exit code was not read out of the inspect")
	}
}

// TestParseEnvMasksSecrets is the rule the detail screen depends on: the name
// is always shown and a value whose name says it carries a secret never is.
func TestParseEnvMasksSecrets(t *testing.T) {
	env := ParseEnv([]string{
		"PATH=/usr/bin",
		"DATABASE_PASSWORD=hunter2",
		"session_secret=abc",
		"STRIPE_API_KEY=sk_live_x",
		"GITHUB_TOKEN=ghp_x",
		"AWS_CREDENTIAL_FILE=/x",
		"EMPTY_TOKEN=",
		"NOVALUE",
	})
	masked := map[string]bool{}
	for _, variable := range env {
		if variable.Masked {
			masked[variable.Name] = true
			if variable.Value != Masked {
				t.Errorf("%s is marked masked but shows %q",
					variable.Name, variable.Value)
			}
		}
	}
	for _, name := range []string{"DATABASE_PASSWORD", "session_secret",
		"STRIPE_API_KEY", "GITHUB_TOKEN", "AWS_CREDENTIAL_FILE"} {
		if !masked[name] {
			t.Errorf("%s was not masked", name)
		}
	}
	if masked["PATH"] {
		t.Errorf("PATH was masked, which hides something worth reading")
	}
	// An empty value has nothing to hide, and masking it would claim there is
	// a secret set where there is not.
	if masked["EMPTY_TOKEN"] {
		t.Errorf("an empty value was masked")
	}
	// Every name is still on screen, masked or not.
	if len(env) != 8 {
		t.Errorf("parsed %d variables, want 8", len(env))
	}
}

// TestParseStats reads one sample, which is all the detail screen asks for.
func TestParseStats(t *testing.T) {
	stats, err := ParseStats(read(t, "stats.json"))
	if err != nil {
		t.Fatalf("ParseStats: %v", err)
	}
	if stats.Empty() {
		t.Fatal("the sample parsed as empty")
	}
	if stats.CPUPercent != "2.41%" || stats.PIDs != "17" {
		t.Errorf("stats = %+v", stats)
	}
	if _, err := ParseStats(""); err == nil {
		t.Errorf("an empty answer parsed as a sample")
	}
}

// TestComposeFilesFor reads the compose files out of the label Compose wrote,
// which is what makes the project actions exact rather than guessed.
func TestComposeFilesFor(t *testing.T) {
	containers, err := ParseContainers(read(t, "ps.json"))
	if err != nil {
		t.Fatalf("ParseContainers: %v", err)
	}
	project := container.Project{Name: "shopfront", Containers: containers}
	files := ComposeFilesFor(project)
	if len(files) == 0 {
		t.Fatal("no compose file was read out of the labels")
	}
	for _, file := range files {
		if !strings.HasPrefix(file, "/") {
			t.Errorf("compose file %q is not an absolute path", file)
		}
	}
}

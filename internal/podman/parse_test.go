package podman

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tui-tools/tui-containers/internal/container"
)

// The fixtures in testdata are what a real Fedora 42 host printed: `podman ps`,
// `podman images`, `podman volume ls`, `podman network ls`, `podman info`,
// `podman system df` and `podman inspect` all answer to the account that owns
// the rootless store, so those files are captured output with the home path
// rewritten. Only stats.json is written by hand and says so: the host had no
// running container to sample.

// now is the clock the uptime column is computed against, fixed so the test
// does not change its answer between one second and the next.
var now = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

// read loads a fixture.
func read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // the name is a literal in the test above, and testdata is in the repository
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	return string(data)
}

// TestParseContainers is the assertion the containers screen rests on for the
// Podman half: an array of typed objects becomes rows, and the scope every row
// came from is carried on it.
func TestParseContainers(t *testing.T) {
	containers, err := ParseContainers(read(t, "ps.json"),
		container.ScopeUser, now)
	if err != nil {
		t.Fatalf("ParseContainers: %v", err)
	}
	if len(containers) != 1 {
		t.Fatalf("parsed %d containers, want 1", len(containers))
	}
	c := containers[0]
	if c.Name != "test-bao" {
		t.Errorf("name = %q", c.Name)
	}
	if c.Target != TargetFor(container.ScopeUser) {
		t.Errorf("target = %v", c.Target)
	}
	if c.State != container.StateExited {
		t.Errorf("state = %q", c.State)
	}
	// Podman reports an exit code for everything; only one that has actually
	// exited has a code worth believing, and this one has.
	if !c.ExitCodeKnown || c.ExitCode != 0 {
		t.Errorf("exit = %d (known %v)", c.ExitCode, c.ExitCodeKnown)
	}
	if c.Created.IsZero() {
		t.Errorf("the created timestamp was not read")
	}
	if len(c.Ports) == 0 {
		t.Fatalf("no ports were read")
	}
	if got := c.Ports[0].String(); got != "8201->8200/tcp" {
		t.Errorf("port = %q", got)
	}
	// The command is a list in Podman's JSON and a phrase in the column.
	if c.Command == "" {
		t.Errorf("the command was not read")
	}
}

// TestUptimeIsComputedFromTheStartTime: Podman prints the second the container
// started, so the column is exact rather than read back out of a sentence.
func TestUptimeIsComputedFromTheStartTime(t *testing.T) {
	started := now.Add(-3 * time.Hour).Unix()
	text := `[{"Id":"abc","Names":["web"],"State":"running",` +
		`"Created":1,"StartedAt":` + itoa(started) + `}]`
	containers, err := ParseContainers(text, container.ScopeUser, now)
	if err != nil {
		t.Fatalf("ParseContainers: %v", err)
	}
	if got := containers[0].Uptime; got != "3 hours" {
		t.Errorf("uptime = %q, want %q", got, "3 hours")
	}

	// A container that is not running has no uptime at all, rather than a
	// number counted from a start that ended.
	stopped := strings.Replace(text, `"running"`, `"exited"`, 1)
	containers, err = ParseContainers(stopped, container.ScopeUser, now)
	if err != nil {
		t.Fatalf("ParseContainers: %v", err)
	}
	if got := containers[0].Uptime; got != "" {
		t.Errorf("a stopped container has an uptime of %q", got)
	}
}

// itoa keeps the test's JSON building readable.
func itoa(n int64) string {
	return time.Unix(n, 0).UTC().Format("") + formatInt(n)
}

// formatInt is strconv.FormatInt without the import.
func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// TestInfraContainersAreDropped: the pause process holding a pod's namespaces
// open runs nothing anyone wrote, and one extra unexplained row per pod is
// worse than not showing it.
func TestInfraContainersAreDropped(t *testing.T) {
	text := `[{"Id":"a","Names":["pod-infra"],"State":"running","IsInfra":true},` +
		`{"Id":"b","Names":["web"],"State":"running"}]`
	containers, err := ParseContainers(text, container.ScopeUser, now)
	if err != nil {
		t.Fatalf("ParseContainers: %v", err)
	}
	if len(containers) != 1 || containers[0].Name != "web" {
		t.Errorf("parsed %+v", containers)
	}
}

// TestHealthAcceptsBothShapes: Podman prints the health field as a word on some
// versions and as the whole object on others, and null when there is no check.
func TestHealthAcceptsBothShapes(t *testing.T) {
	tests := map[string]container.Health{
		`"unhealthy"`:            container.HealthUnhealthy,
		`"healthy"`:              container.HealthHealthy,
		`{"Status":"unhealthy"}`: container.HealthUnhealthy,
		`null`:                   container.HealthNone,
		`""`:                     container.HealthNone,
	}
	for shape, want := range tests {
		text := `[{"Id":"a","Names":["web"],"State":"running","Health":` +
			shape + `}]`
		containers, err := ParseContainers(text, container.ScopeUser, now)
		if err != nil {
			t.Fatalf("ParseContainers(%s): %v", shape, err)
		}
		if got := containers[0].Health; got != want {
			t.Errorf("health from %s = %q, want %q", shape, got, want)
		}
	}
}

// TestEmptyStoreIsNotAnError: a scope with no containers is an ordinary scope,
// and `podman ps` answers `[]`.
func TestEmptyStoreIsNotAnError(t *testing.T) {
	containers, err := ParseContainers("[]", container.ScopeUser, now)
	if err != nil {
		t.Fatalf("ParseContainers: %v", err)
	}
	if len(containers) != 0 {
		t.Errorf("parsed %d containers from an empty store", len(containers))
	}
	if _, err := ParseContainers("", container.ScopeUser, now); err != nil {
		t.Errorf("an empty answer is an error: %v", err)
	}
	if _, err := ParseContainers("not json", container.ScopeUser, now); err == nil {
		t.Errorf("rubbish parsed cleanly")
	}
}

// TestParseImages reads the store. Podman names an image by every reference
// that points at it, and an image with no names left is what dangling means.
func TestParseImages(t *testing.T) {
	images, err := ParseImages(read(t, "images.json"), container.ScopeUser)
	if err != nil {
		t.Fatalf("ParseImages: %v", err)
	}
	if len(images) == 0 {
		t.Fatal("no images were parsed")
	}
	for _, image := range images {
		if len(image.ID) != 12 {
			t.Errorf("%s: id %q is not the 12 characters both engines print",
				image.Name(), image.ID)
		}
		if image.SizeBytes == 0 || image.SizeText == "" {
			t.Errorf("%s: size = %d (%q)", image.Name(), image.SizeBytes,
				image.SizeText)
		}
	}

	dangling, err := ParseImages(`[{"Id":"aaaaaaaaaaaaaaaa","Size":1000,`+
		`"Names":null}]`, container.ScopeUser)
	if err != nil {
		t.Fatalf("ParseImages: %v", err)
	}
	if !dangling[0].Dangling {
		t.Errorf("an image with no names is not marked dangling")
	}
}

// TestSplitReference: the colon of a registry port is not a tag separator.
func TestSplitReference(t *testing.T) {
	tests := []struct{ in, repo, tag string }{
		{"docker.io/library/registry:2", "docker.io/library/registry", "2"},
		{"quay.io/openbao/openbao:latest", "quay.io/openbao/openbao", "latest"},
		{"localhost:5000/app", "localhost:5000/app", ""},
		{"localhost:5000/app:v2", "localhost:5000/app", "v2"},
		{"alpine", "alpine", ""},
	}
	for _, test := range tests {
		repo, tag := SplitReference(test.in)
		if repo != test.repo || tag != test.tag {
			t.Errorf("SplitReference(%q) = %q, %q; want %q, %q",
				test.in, repo, tag, test.repo, test.tag)
		}
	}
}

// TestParseVolumesAndNetworks reads the two lists the storage screen is built
// from. Podman's network keys are lower-case where every other command's are
// capitalised, which is the sort of thing a fixture exists to catch.
func TestParseVolumesAndNetworks(t *testing.T) {
	volumes, err := ParseVolumes(read(t, "volume-ls.json"), container.ScopeUser)
	if err != nil {
		t.Fatalf("ParseVolumes: %v", err)
	}
	if len(volumes) == 0 {
		t.Fatal("no volumes were parsed")
	}
	var anonymous int
	for _, volume := range volumes {
		if volume.Name == "" || volume.Mountpoint == "" {
			t.Errorf("a volume was parsed with no name or mountpoint: %+v", volume)
		}
		if volume.Anonymous {
			anonymous++
		}
	}
	if anonymous == 0 {
		t.Errorf("no anonymous volume was recognised in a store full of them")
	}

	networks, err := ParseNetworks(read(t, "network-ls.json"), container.ScopeUser)
	if err != nil {
		t.Fatalf("ParseNetworks: %v", err)
	}
	if len(networks) == 0 {
		t.Fatal("no networks were parsed")
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
	if builtin != 1 {
		t.Errorf("%d built-in networks, want exactly the one called %q",
			builtin, DefaultNetwork)
	}
}

// TestParseInfo reads the engine summary, including the rootless flag that is
// the single most explanatory fact about a Podman store.
func TestParseInfo(t *testing.T) {
	info, err := ParseInfo(read(t, "info.json"), container.ScopeUser)
	if err != nil {
		t.Fatalf("ParseInfo: %v", err)
	}
	if !info.Available || !info.Rootless {
		t.Errorf("info = %+v", info)
	}
	if info.ServerVersion == "" || info.StorageDriver == "" || info.Root == "" {
		t.Errorf("info = %+v", info)
	}
	// The cgroup version is "v2" in Podman's JSON and "2" in Docker's; the
	// column has to read the same either way.
	if info.CgroupVersion != "2" {
		t.Errorf("cgroup version = %q, want %q", info.CgroupVersion, "2")
	}
	if len(info.SearchRegistries) == 0 {
		t.Errorf("the search registries were not read")
	}
}

// TestParseDisk reads the five-column table, whose reclaimable column carries a
// percentage in parentheses.
func TestParseDisk(t *testing.T) {
	rows := ParseDisk(read(t, "system-df.txt"))
	if len(rows) != 3 {
		t.Fatalf("parsed %d rows, want 3: %+v", len(rows), rows)
	}
	if rows[2].Type != "Local Volumes" {
		t.Errorf("the two-word type was split: %q", rows[2].Type)
	}
	for _, row := range rows {
		if row.Size == "" || row.Reclaimable == "" {
			t.Errorf("a row is missing a column: %+v", row)
		}
	}
}

// TestParseInspect reads the detail screen's content. Podman implements
// Docker's inspect schema, which is what lets the screen be written once.
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
	filled := InspectContainer(container.Container{ID: "d1af55e7463a"},
		read(t, "inspect.json"))
	if filled.RestartPolicy == "" {
		t.Errorf("the restart policy was not read; `podman ps` does not report it")
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
		"EMPTY_TOKEN=",
	})
	byName := map[string]container.EnvVar{}
	for _, variable := range env {
		byName[variable.Name] = variable
	}
	for _, name := range []string{"DATABASE_PASSWORD", "session_secret",
		"STRIPE_API_KEY"} {
		if !byName[name].Masked || byName[name].Value != Masked {
			t.Errorf("%s was not masked: %+v", name, byName[name])
		}
	}
	if byName["PATH"].Masked {
		t.Errorf("PATH was masked")
	}
	if byName["EMPTY_TOKEN"].Masked {
		t.Errorf("an empty value was masked, which claims a secret that is not set")
	}
}

// TestParseStats reads one sample, tolerating the two shapes Podman has printed
// the numeric fields in.
func TestParseStats(t *testing.T) {
	stats, err := ParseStats(read(t, "stats.json"))
	if err != nil {
		t.Fatalf("ParseStats: %v", err)
	}
	if stats.CPUPercent != "2.41%" || stats.PIDs != "17" {
		t.Errorf("stats = %+v", stats)
	}
	// A number where a string was expected must not break the pane.
	numeric, err := ParseStats(`[{"CPU":2.41,"MemPerc":42.66,"PIDS":17}]`)
	if err != nil {
		t.Fatalf("ParseStats(numeric): %v", err)
	}
	if numeric.CPUPercent != "2.41" || numeric.PIDs != "17" {
		t.Errorf("numeric stats = %+v", numeric)
	}
	if _, err := ParseStats("[]"); err == nil {
		t.Errorf("an empty sample list parsed as a sample")
	}
}

// TestHumanSize renders a byte count the way Docker prints one, so a column
// filled from either engine reads the same.
func TestHumanSize(t *testing.T) {
	tests := map[int64]string{
		0: "0B", 63: "63B", 408_368_836: "408MB", 25_400_000: "25.40MB",
		1_630_000_000: "1.63GB",
	}
	for bytes, want := range tests {
		if got := HumanSize(bytes); got != want {
			t.Errorf("HumanSize(%d) = %q, want %q", bytes, got, want)
		}
	}
}

// TestQuadletUnitNames pins the generator's own naming rule, which is what the
// engines screen prints next to each file.
func TestQuadletUnitNames(t *testing.T) {
	tests := map[string]string{
		"registry.container": "registry.service",
		"data.volume":        "data-volume.service",
		"web.network":        "web-network.service",
		"app.pod":            "app-pod.service",
		"site.image":         "site-image.service",
		"stack.kube":         "stack.service",
	}
	for file, want := range tests {
		if got := QuadletUnitName(file); got != want {
			t.Errorf("QuadletUnitName(%q) = %q, want %q", file, got, want)
		}
	}
}

// TestQuadletDirsAreScoped: the two scopes look in entirely different places,
// and a tool that read one and labelled it the other would be reporting root's
// containers as yours.
func TestQuadletDirsAreScoped(t *testing.T) {
	system := QuadletDirs(container.ScopeSystem)
	if len(system) == 0 || system[0] != "/etc/containers/systemd" {
		t.Errorf("system dirs = %v", system)
	}
	t.Setenv("XDG_CONFIG_HOME", "/tmp/example-config")
	user := QuadletDirs(container.ScopeUser)
	if len(user) != 1 ||
		user[0] != "/tmp/example-config/containers/systemd" {
		t.Errorf("user dirs = %v", user)
	}
	for _, dir := range user {
		for _, other := range system {
			if dir == other {
				t.Errorf("the two scopes share the directory %q", dir)
			}
		}
	}
}

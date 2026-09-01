package docker

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tui-tools/tui-containers/internal/container"
)

// Every parser in this package reads bytes the tool did not write: the JSON
// the Docker CLI prints, the flattened columns it prints beside it, and the
// `system df` table. `go test` replays the seeds below on every commit, and
// `go test -run=^$ -fuzz=FuzzParseContainers ./internal/docker/` explores past
// them locally — see the family rule in tui-kit/templates/FUZZING.md.
//
// The seeds are the captured fixtures the table tests use, so the corpus
// starts on real output and mutates from there instead of guessing its shape.

// fuzzSeed adds every named testdata file to the corpus, plus the shapes a
// real capture never has: nothing, a lone separator, a truncated document.
func fuzzSeed(f *testing.F, names ...string) {
	f.Helper()
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // the name is a literal in the tests, and testdata is in the repository
		if err != nil {
			f.Fatalf("read fixture %s: %v", name, err)
		}
		f.Add(string(raw))
	}
	f.Add("")
	f.Add("\n\n\n")
	f.Add("{}")
	f.Add("[]")
	f.Add("[{")
	f.Add("null")
}

// oneLine asserts that a field the UI draws in a column really is one line.
func oneLine(t *testing.T, what, value string) {
	t.Helper()
	if strings.ContainsAny(value, "\n\r") {
		t.Fatalf("%s carries a line break: %q", what, value)
	}
}

// labelKeyRe is what a label name may look like once ParseLabels has split
// one out. It is labelNameRe without the trailing `=`, because that is the
// only thing that made the fragment a label in the first place.
var labelKeyRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func FuzzParseContainers(f *testing.F) {
	fuzzSeed(f, "ps.json")
	f.Fuzz(func(t *testing.T, output string) {
		rows, err := ParseContainers(output)
		if err != nil {
			if rows != nil {
				t.Fatalf("returned %d containers with an error", len(rows))
			}
			return
		}
		for _, c := range rows {
			// Every row is acted on through its target: a command built for
			// the wrong engine would run somewhere the row is not.
			if c.Target != Target {
				t.Fatalf("row claims target %+v", c.Target)
			}
			if c.Raw == "" {
				t.Fatalf("row %q kept none of the line it came from", c.Name)
			}
			// State is compared against the constants everywhere, so the
			// normalisation has to have happened.
			if string(c.State) != strings.ToLower(strings.TrimSpace(string(c.State))) {
				t.Fatalf("state was not normalised: %q", c.State)
			}
			oneLine(t, "name", c.Name)
			oneLine(t, "image", c.Image)
			for _, network := range c.Networks {
				if strings.TrimSpace(network) == "" {
					t.Fatal("blank network name")
				}
			}
		}
	})
}

func FuzzParseLabels(f *testing.F) {
	for _, seed := range []string{
		"com.docker.compose.project=web,com.docker.compose.service=api",
		"maintainer=someone, with a comma,org.opencontainers.image.title=x",
		"", ",", "=", "a=", "=b", "0=1,,2",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		for name, value := range ParseLabels(text) {
			// A key that still carried an `=` would mean the split ran on
			// something that is not a label, and the Compose lookups read
			// these keys by exact name.
			if !labelKeyRe.MatchString(name) {
				t.Fatalf("label name is not a label name: %q", name)
			}
			oneLine(t, "label value", value)
		}
	})
}

func FuzzParsePorts(f *testing.F) {
	for _, seed := range []string{
		"0.0.0.0:8080->80/tcp, [::]:8080->80/tcp",
		"53/udp", "127.0.0.1:5432->5432/tcp", "", ",", "->", "0:0->0/",
		"[::]:1->1/tcp",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		seen := map[string]bool{}
		for _, port := range ParsePorts(text) {
			if port.Protocol == "" {
				t.Fatal("mapping with no protocol")
			}
			if port.ContainerPort <= 0 {
				t.Fatalf("container port is not a port: %d", port.ContainerPort)
			}
			// The two address families are folded into one row on purpose, so
			// a duplicate here would be the fold failing.
			if key := port.String(); seen[key] {
				t.Fatalf("mapping %q listed twice", key)
			} else {
				seen[key] = true
			}
			// The wildcard addresses are dropped rather than shown: a reader
			// wants the port, not the fact that the machine has two stacks.
			if port.HostIP == "0.0.0.0" || port.HostIP == "::" {
				t.Fatalf("wildcard address kept: %q", port.HostIP)
			}
			if strings.ContainsAny(port.HostIP, "[]") {
				t.Fatalf("host address still bracketed: %q", port.HostIP)
			}
		}
	})
}

func FuzzParseImages(f *testing.F) {
	fuzzSeed(f, "images.json")
	f.Fuzz(func(t *testing.T, output string) {
		rows, err := ParseImages(output)
		if err != nil {
			if rows != nil {
				t.Fatalf("returned %d images with an error", len(rows))
			}
			return
		}
		for _, image := range rows {
			if image.Target != Target {
				t.Fatalf("image claims target %+v", image.Target)
			}
			// A size that went negative would sort an image to the top of the
			// "what can I reclaim" list on nothing but an overflow.
			if image.SizeBytes < 0 {
				t.Fatalf("negative size %d from %q", image.SizeBytes, image.SizeText)
			}
			// Dangling is what the prune screen counts, so it means exactly
			// what it says: no repository and no tag.
			if image.Dangling != (none(image.Repository) && none(image.Tag)) {
				t.Fatalf("dangling=%v for %q:%q",
					image.Dangling, image.Repository, image.Tag)
			}
			oneLine(t, "repository", image.Repository)
			oneLine(t, "tag", image.Tag)
		}
	})
}

func FuzzParseSize(f *testing.F) {
	for _, seed := range []string{
		"1.63GB", "0B", "123", "12 kB", "9.9TB", "", "-1GB", "1e40GB",
		"999999999999999999999999TB",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		// The result is only ever a sort key, and 0 is the documented answer
		// for a size nobody could establish. A negative one is neither.
		if got := ParseSize(text); got < 0 {
			t.Fatalf("ParseSize(%q) = %d", text, got)
		}
	})
}

func FuzzParseVolumes(f *testing.F) {
	fuzzSeed(f, "volume-ls.json")
	f.Fuzz(func(t *testing.T, output string) {
		rows, err := ParseVolumes(output)
		if err != nil {
			if rows != nil {
				t.Fatalf("returned %d volumes with an error", len(rows))
			}
			return
		}
		for _, volume := range rows {
			if volume.Target != Target {
				t.Fatalf("volume claims target %+v", volume.Target)
			}
			oneLine(t, "volume name", volume.Name)
			oneLine(t, "mountpoint", volume.Mountpoint)
		}
	})
}

func FuzzParseNetworks(f *testing.F) {
	fuzzSeed(f, "network-ls.json")
	f.Fuzz(func(t *testing.T, output string) {
		rows, err := ParseNetworks(output)
		if err != nil {
			if rows != nil {
				t.Fatalf("returned %d networks with an error", len(rows))
			}
			return
		}
		for _, network := range rows {
			if network.Target != Target {
				t.Fatalf("network claims target %+v", network.Target)
			}
			// Builtin is what stops a prune from being offered for a network
			// the engine will not remove, so it follows the name and nothing
			// else.
			if network.Builtin != builtinNetworks[network.Name] {
				t.Fatalf("builtin=%v for %q", network.Builtin, network.Name)
			}
			oneLine(t, "network name", network.Name)
		}
	})
}

func FuzzParseInfo(f *testing.F) {
	fuzzSeed(f, "info.json")
	f.Fuzz(func(t *testing.T, output string) {
		info, err := ParseInfo(output)
		if err != nil {
			if info.Available || info.Installed {
				t.Fatal("returned a usable engine summary with an error")
			}
			return
		}
		// The screen reads Available and Installed to decide whether there is
		// an engine to talk to at all, and an `info` that parsed is one.
		if !info.Available || !info.Installed {
			t.Fatal("parsed an info that says the engine is not there")
		}
		if info.Target != Target {
			t.Fatalf("info claims target %+v", info.Target)
		}
		oneLine(t, "server version", info.ServerVersion)
		oneLine(t, "storage driver", info.StorageDriver)
	})
}

func FuzzParseDisk(f *testing.F) {
	fuzzSeed(f, "system-df.txt")
	f.Fuzz(func(t *testing.T, output string) {
		for _, row := range ParseDisk(output) {
			// Every cell is drawn in its own column, so none of them may be
			// blank or carry the whitespace the split was done on.
			for what, value := range map[string]string{
				"type": row.Type, "total": row.Total, "active": row.Active,
				"size": row.Size, "reclaimable": row.Reclaimable,
			} {
				if value == "" {
					t.Fatalf("%s cell is empty in %+v", what, row)
				}
				oneLine(t, what+" cell", value)
			}
		}
	})
}

func FuzzParseInspect(f *testing.F) {
	fuzzSeed(f, "inspect.json")
	f.Fuzz(func(t *testing.T, output string) {
		detail, err := ParseInspect(output)
		if err != nil {
			return
		}
		if detail.Raw == "" {
			t.Fatal("parsed a detail that kept none of its source")
		}
		for _, mount := range detail.Mounts {
			oneLine(t, "mount source", mount.Source)
			oneLine(t, "mount destination", mount.Destination)
		}
		// The networks are sorted so the screen does not reshuffle between
		// two reads of the same container.
		for i := 1; i < len(detail.Networks); i++ {
			if detail.Networks[i-1].Name > detail.Networks[i].Name {
				t.Fatalf("networks are out of order: %q before %q",
					detail.Networks[i-1].Name, detail.Networks[i].Name)
			}
		}
		checkEnv(t, detail.Env)
	})
}

func FuzzParseStats(f *testing.F) {
	fuzzSeed(f, "stats.json")
	f.Fuzz(func(t *testing.T, output string) {
		stats, err := ParseStats(output)
		if err != nil {
			return
		}
		oneLine(t, "cpu", stats.CPUPercent)
		oneLine(t, "memory", stats.MemUsage)
		oneLine(t, "network io", stats.NetIO)
		oneLine(t, "block io", stats.BlockIO)
	})
}

// checkEnv asserts the one thing the environment list promises: a variable
// whose name says it carries a secret shows the mask and not the value.
func checkEnv(t *testing.T, vars []container.EnvVar) {
	t.Helper()
	for _, variable := range vars {
		if variable.Masked && variable.Value != Masked {
			t.Fatalf("%q is masked but shows %q", variable.Name, variable.Value)
		}
		if !variable.Masked && variable.Value != "" &&
			secretRe.MatchString(variable.Name) {
			t.Fatalf("%q looks like a secret and is not masked", variable.Name)
		}
	}
}

func FuzzParseEnv(f *testing.F) {
	for _, seed := range []string{
		"PATH=/usr/bin", "DATABASE_PASSWORD=hunter2", "TOKEN=", "NOEQUALS",
		"", "=value", "key=value=with=equals", "api_key=x",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, entry string) {
		checkEnv(t, ParseEnv([]string{entry}))
	})
}

func FuzzStatusSentence(f *testing.F) {
	for _, seed := range []string{
		"Up 3 hours (healthy)", "Exited (137) 17 hours ago", "Created",
		"Up About a minute", "Exited (-1) now", "", "Up ", "Exited (99999999999999999999)",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, status string) {
		uptime := UptimeFromStatus(status)
		// The uptime is drawn as a bare phrase in its own column, so the
		// parenthesised health note is not part of it and neither is padding.
		if strings.ContainsAny(uptime, "(") {
			t.Fatalf("uptime kept the health note: %q", uptime)
		}
		if uptime != strings.TrimSpace(uptime) {
			t.Fatalf("uptime is padded: %q", uptime)
		}
		// An exit code is either read or not; a false claim is what would put
		// a number nobody reported next to a container.
		if _, ok := ExitCodeFromStatus(status); ok &&
			!strings.Contains(status, "Exited") {
			t.Fatalf("claimed an exit code from %q", status)
		}
	})
}

// FuzzBuildRun explores the one builder in this package whose input comes from
// a keyboard rather than from the engine's own output. The property is the
// promise the preview makes: either the value is refused with a reason, or the
// argv it produced carries nothing that could turn one command into two.
func FuzzBuildRun(f *testing.F) {
	f.Add("nginx:1.27", "edge", "8080:80", "/srv/x:/srv/x:ro", "/etc/app.env", "always")
	f.Add("", "", "", "", "", "")
	f.Add("nginx", "-rm", "80", "./data:/data", "app.env", "no")
	f.Add("nginx", "a b", "1:1\n2:2", "/a:/b:rw", "/e", "on-failure:3")
	f.Fuzz(func(t *testing.T, image, name, ports, volumes, envFile, policy string) {
		cmd, err := BuildRun(container.RunSpec{
			Image:         image,
			Name:          name,
			Ports:         strings.Fields(ports),
			Volumes:       strings.Fields(volumes),
			EnvFile:       envFile,
			RestartPolicy: policy,
		})
		if err != nil {
			if len(cmd.Argv) != 0 {
				t.Fatalf("a refused run still built %v", cmd.Argv)
			}
			return
		}
		for _, arg := range cmd.Argv {
			if strings.ContainsAny(arg, "\n\r\x00") {
				t.Fatalf("argv carries a control character: %q", arg)
			}
			if strings.ContainsAny(arg, "$`;&|<>") {
				t.Fatalf("argv carries a shell metacharacter: %q", arg)
			}
		}
		// The image is always last, so nothing a user typed can be read as an
		// option to the engine.
		if got := cmd.Argv[len(cmd.Argv)-1]; got != strings.TrimSpace(image) {
			t.Fatalf("the last argument is %q, not the image %q", got, image)
		}
		oneLine(t, "description", cmd.Description)
	})
}

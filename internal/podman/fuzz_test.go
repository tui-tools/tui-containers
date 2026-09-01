package podman

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tui-tools/tui-containers/internal/container"
)

// Every parser in this package reads bytes the tool did not write: the JSON
// Podman answers with, and the `system df` table it prints. `go test` replays
// the seeds below on every commit, and
// `go test -run=^$ -fuzz=FuzzParseContainers ./internal/podman/` explores past
// them locally — see the family rule in tui-kit/templates/FUZZING.md.
//
// The seeds are the captured fixtures the table tests use, so the corpus
// starts on real output and mutates from there instead of guessing its shape.

// fuzzSeed adds every named testdata file to the corpus, plus the shapes a
// real capture never has: nothing, an empty document, a truncated one.
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

func FuzzParseContainers(f *testing.F) {
	fuzzSeed(f, "ps.json")
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, output string) {
		for _, scope := range []container.Scope{container.ScopeUser, container.ScopeSystem} {
			rows, err := ParseContainers(output, scope, now)
			if err != nil {
				if rows != nil {
					t.Fatalf("returned %d containers with an error", len(rows))
				}
				continue
			}
			for _, c := range rows {
				// Every row is acted on through its target, and a command
				// built for the wrong scope runs against the wrong store.
				if c.Target != TargetFor(scope) {
					t.Fatalf("row claims target %+v in scope %q", c.Target, scope)
				}
				if string(c.State) != strings.ToLower(strings.TrimSpace(string(c.State))) {
					t.Fatalf("state was not normalised: %q", c.State)
				}
				oneLine(t, "name", c.Name)
				oneLine(t, "image", c.Image)
				for _, port := range c.Ports {
					// The wildcard addresses say nothing in a narrow column
					// and are dropped rather than shown.
					if port.HostIP == "0.0.0.0" || port.HostIP == "::" {
						t.Fatalf("wildcard address kept: %q", port.HostIP)
					}
				}
			}
		}
	})
}

func FuzzParseImages(f *testing.F) {
	fuzzSeed(f, "images.json")
	f.Fuzz(func(t *testing.T, output string) {
		rows, err := ParseImages(output, container.ScopeUser)
		if err != nil {
			if rows != nil {
				t.Fatalf("returned %d images with an error", len(rows))
			}
			return
		}
		for _, image := range rows {
			// The id is shown next to a Docker one, so it is always the
			// twelve characters both engines print.
			if len(image.ID) > 12 {
				t.Fatalf("image id was not shortened: %q", image.ID)
			}
			if image.SizeText == "" {
				t.Fatalf("image %q has no size to draw", image.ID)
			}
			oneLine(t, "size", image.SizeText)
			oneLine(t, "repository", image.Repository)
		}
	})
}

func FuzzParseVolumes(f *testing.F) {
	fuzzSeed(f, "volume-ls.json")
	f.Fuzz(func(t *testing.T, output string) {
		rows, err := ParseVolumes(output, container.ScopeSystem)
		if err != nil {
			if rows != nil {
				t.Fatalf("returned %d volumes with an error", len(rows))
			}
			return
		}
		for _, volume := range rows {
			if volume.Target != TargetFor(container.ScopeSystem) {
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
		rows, err := ParseNetworks(output, container.ScopeUser)
		if err != nil {
			if rows != nil {
				t.Fatalf("returned %d networks with an error", len(rows))
			}
			return
		}
		for _, network := range rows {
			// Builtin is what stops a removal from being offered for the
			// network Podman will not remove, so it follows the name.
			if network.Builtin != (network.Name == DefaultNetwork) {
				t.Fatalf("builtin=%v for %q", network.Builtin, network.Name)
			}
			if len(network.ID) > 12 {
				t.Fatalf("network id was not shortened: %q", network.ID)
			}
			oneLine(t, "network name", network.Name)
		}
	})
}

func FuzzParseInfo(f *testing.F) {
	fuzzSeed(f, "info.json")
	f.Fuzz(func(t *testing.T, output string) {
		info, err := ParseInfo(output, container.ScopeUser)
		if err != nil {
			if info.Available || info.Installed {
				t.Fatal("returned a usable engine summary with an error")
			}
			return
		}
		if !info.Available || !info.Installed {
			t.Fatal("parsed an info that says the engine is not there")
		}
		// The cgroup version is drawn as a bare number beside Docker's, so the
		// "v" Podman writes has to be off.
		if strings.HasPrefix(info.CgroupVersion, "v") {
			t.Fatalf("cgroup version kept its prefix: %q", info.CgroupVersion)
		}
		oneLine(t, "server version", info.ServerVersion)
		oneLine(t, "storage driver", info.StorageDriver)
	})
}

func FuzzParseDisk(f *testing.F) {
	fuzzSeed(f, "system-df.txt")
	f.Fuzz(func(t *testing.T, output string) {
		for _, row := range ParseDisk(output) {
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
		if !variable.Masked && variable.Value != "" && SecretName(variable.Name) {
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

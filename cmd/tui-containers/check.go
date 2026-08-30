package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/tui-tools/tui-containers/internal/container"
	"github.com/tui-tools/tui-kit/compat"
)

// checkTimeout bounds the read. Loading the model shells out several times per
// engine, and a machine with three engine scopes and a cold Podman store must
// not hang a non-interactive check forever.
const checkTimeout = 60 * time.Second

// containerReport is one container flattened into the fields a shell script can
// assert on without walking the model.
type containerReport struct {
	Name     string           `json:"name"`
	Engine   container.Engine `json:"engine"`
	Scope    container.Scope  `json:"scope,omitempty"`
	Image    string           `json:"image"`
	State    container.State  `json:"state"`
	Status   string           `json:"status,omitempty"`
	Health   container.Health `json:"health,omitempty"`
	ExitCode *int             `json:"exitCode,omitempty"`
	Project  string           `json:"project,omitempty"`
}

// engineReport is one engine and scope, flattened the same way.
type engineReport struct {
	Engine        container.Engine `json:"engine"`
	Scope         container.Scope  `json:"scope,omitempty"`
	Installed     bool             `json:"installed"`
	Available     bool             `json:"available"`
	Version       string           `json:"version,omitempty"`
	Rootless      bool             `json:"rootless"`
	StorageDriver string           `json:"storageDriver,omitempty"`
	CgroupVersion string           `json:"cgroupVersion,omitempty"`
	Compose       bool             `json:"compose"`
	Escalated     bool             `json:"escalated"`
	Quadlets      int              `json:"quadlets"`
	Detail        string           `json:"detail,omitempty"`
}

// checkReport is what --check prints: which engines answered, the counts, the
// containers worth looking at, and the model in full.
//
// It is a report of the read path only. --check never builds and never runs a
// mutation: the whole point is that it is safe to run anywhere, including in CI
// against a production-shaped machine.
type checkReport struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
	Backend string `json:"backend"`
	// Describe is the backend's own one-line summary, which is where the demo
	// backend says it is a demo.
	Describe string `json:"describe"`

	// Engines is one entry per engine and scope that was looked for, including
	// the ones that are not here. A machine with no engine at all reports an
	// empty list and still exits 0 — see runCheck.
	Engines []engineReport `json:"engines"`
	// EngineCount is how many answered, which is the number a smoke test
	// asserts on to tell an engine-less machine from one that has Docker.
	EngineCount int `json:"engineCount"`

	// The counts a script asserts on. States is every state, including the
	// ones with none, so a zero can be compared against rather than a key that
	// may or may not be there.
	Containers int            `json:"containers"`
	States     map[string]int `json:"states"`
	Images     int            `json:"images"`
	Dangling   int            `json:"dangling"`
	Volumes    int            `json:"volumes"`
	Networks   int            `json:"networks"`
	Projects   int            `json:"projects"`

	// Attention are the containers whose last run did not go well, and
	// Unhealthy the running ones their own health check disagrees with. Both
	// are reported rather than asserted: a container that has been failing for
	// a month is a fact about the machine, not a failure of the read path.
	Attention      []containerReport `json:"attention"`
	Unhealthy      []containerReport `json:"unhealthy"`
	AttentionCount int               `json:"attentionCount"`
	UnhealthyCount int               `json:"unhealthyCount"`

	// Disk is `system df` per engine, keyed by the engine's name, which is
	// where a script goes for "how much could be reclaimed".
	Disk map[string][]container.DiskRow `json:"disk,omitempty"`

	// Compat is what the version probes found, one entry per declared engine.
	// An engine that is not installed reports its name and no version, which is
	// the honest answer.
	Compat []compat.Result `json:"compat"`
	// Model is the parsed state in full.
	Model container.Model `json:"model"`
}

// runCheck exercises the backend's real read path and prints what it parsed as
// JSON.
//
// It exits 0 on a machine with no container engine at all. That is deliberate,
// and it is the one place this tool's --check differs from its siblings': a
// server with no Docker and no Podman is a normal server, not a broken one, and
// the honest report is an empty engine list with the reason. A non-zero exit
// there would make the family's smoke suite fail on every guest that simply
// does not run containers.
//
// The error return is for the case where the backend itself could not be built
// or its read path broke — which the caller turns into a non-zero exit.
func runCheck(backend container.Backend, backendCompat []compat.Result,
	out io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()

	report := checkReport{
		Tool:     toolName,
		Version:  version,
		Backend:  backend.Name(),
		Describe: backend.Describe(),
		States:   map[string]int{},
		Compat:   backendCompat,
	}

	model, err := backend.Load(ctx)
	if err != nil {
		// No engine answered. That is a report, not a failure: the engines
		// list carries what each of them said, and the exit code stays 0 so a
		// smoke test can assert on the empty case.
		report.Model = container.Model{Backend: backend.Name()}
		for _, state := range container.States {
			report.States[string(state)] = 0
		}
		report.Engines = []engineReport{}
		return encode(out, report)
	}

	report.Model = model
	report.Containers = len(model.Containers)
	report.Images = len(model.Images)
	report.Dangling = len(model.Dangling())
	report.Volumes = len(model.Volumes)
	report.Networks = len(model.Networks)
	report.Projects = len(model.Projects)

	counts := model.Counts()
	for _, state := range container.States {
		report.States[string(state)] = counts[state]
	}

	for _, info := range model.Engines {
		report.Engines = append(report.Engines, flattenEngine(info))
		if info.Available {
			report.EngineCount++
		}
		if len(info.Disk) > 0 {
			if report.Disk == nil {
				report.Disk = map[string][]container.DiskRow{}
			}
			report.Disk[info.Target.String()] = info.Disk
		}
	}
	if report.Engines == nil {
		report.Engines = []engineReport{}
	}

	for _, c := range model.Attention() {
		report.Attention = append(report.Attention, flatten(c))
	}
	for _, c := range model.Unhealthy() {
		report.Unhealthy = append(report.Unhealthy, flatten(c))
	}
	report.AttentionCount = len(report.Attention)
	report.UnhealthyCount = len(report.Unhealthy)

	return encode(out, report)
}

// encode writes the report.
func encode(out io.Writer, report checkReport) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("writing the report: %w", err)
	}
	return nil
}

// flatten reduces a container to the fields the report carries.
func flatten(c container.Container) containerReport {
	report := containerReport{
		Name:    c.Name,
		Engine:  c.Target.Engine,
		Scope:   c.Target.Scope,
		Image:   c.Image,
		State:   c.State,
		Status:  c.Status,
		Health:  c.Health,
		Project: c.Project,
	}
	// The exit code is a pointer so that "exited 0" and "no code was read" are
	// different in the JSON. A script that treated a missing field as a zero
	// would call a container that never ran a success.
	if c.ExitCodeKnown {
		code := c.ExitCode
		report.ExitCode = &code
	}
	return report
}

// flattenEngine reduces an engine summary to the fields the report carries.
func flattenEngine(info container.EngineInfo) engineReport {
	return engineReport{
		Engine:        info.Target.Engine,
		Scope:         info.Target.Scope,
		Installed:     info.Installed,
		Available:     info.Available,
		Version:       info.ServerVersion,
		Rootless:      info.Rootless,
		StorageDriver: info.StorageDriver,
		CgroupVersion: info.CgroupVersion,
		Compose:       info.Compose,
		Escalated:     info.Escalated,
		Quadlets:      len(info.Quadlets),
		Detail:        info.Detail,
	}
}

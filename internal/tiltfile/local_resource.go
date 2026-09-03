package tiltfile

import (
	"fmt"

	"github.com/pkg/errors"
	"go.starlark.net/starlark"

	"github.com/tilt-dev/tilt/internal/tiltfile/links"
	"github.com/tilt-dev/tilt/internal/tiltfile/probe"
	"github.com/tilt-dev/tilt/internal/tiltfile/starkit"
	"github.com/tilt-dev/tilt/internal/tiltfile/value"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/logger"
	"github.com/tilt-dev/tilt/pkg/model"
)

const testDeprecationMsg = "test() is deprecated and will be removed in a future release.\n" +
	"Change this call to use `local_resource(..., allow_parallel=True)`"

// ServePortEnvVar carries the deconflicted serve port to the serve process:
// the loader injects it into ServeCmd.Env for worktree runs (see
// translateLocal). The Tiltfile author binds
// `$TILT_SERVE_PORT` in serve_cmd.
const ServePortEnvVar = "TILT_SERVE_PORT"

// wtLocalResourcePortOwner is the portregistry allocation key for one
// worktree run's local_resource serve port: stable across reloads (same
// worktree + authored name → same owner → same port) and distinct across
// worktrees and resources. Mirrors wtPortOwner (docker_compose.go) — the
// `lr:` prefix namespaces local_resource allocations away from DC ones.
func wtLocalResourcePortOwner(worktree, resourceName string) string {
	return fmt.Sprintf("lr:%s/%s", worktree, resourceName)
}

type localResource struct {
	name      string
	updateCmd model.Cmd
	serveCmd  model.Cmd
	// Local port the serve process binds (serve_port=). Authoritative in
	// worktree runs: the loader rewrites it through portregistry.Allocate
	// and injects TILT_SERVE_PORT so the process binds the deconflicted
	// port (plan §4.5/§5). Main runs keep the authored value.
	servePort     int
	threadDir     string
	deps          []string
	triggerMode   triggerMode
	autoInit      bool
	resourceDeps  []string
	ignores       []string
	allowParallel bool
	links         []model.Link
	labels        map[string]string

	readinessProbe *v1alpha1.Probe
}

func (s *tiltfileState) localResource(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name value.Name
	var updateCmdVal, updateCmdBatVal, serveCmdVal, serveCmdBatVal starlark.Value
	var updateEnv, serveEnv value.StringStringMap
	var triggerMode triggerMode
	var readinessProbe probe.Probe
	var updateCmdDirVal, serveCmdDirVal starlark.Value
	var servePortVal value.Int32

	deps := value.NewLocalPathListUnpacker(thread)

	var resourceDepsVal starlark.Sequence
	var ignoresVal starlark.Value
	var allowParallel bool
	var links links.LinkList
	var labels value.LabelSet
	autoInit := true
	if fn.Name() == testN {
		// If we're initializing a test, by default parallelism is on
		allowParallel = true
		logger.Get(s.ctx).Warnf("%s", testDeprecationMsg)
	}

	if err := s.unpackArgs(fn.Name(), args, kwargs,
		"name", &name,
		"cmd?", &updateCmdVal,
		"deps?", &deps,
		"trigger_mode?", &triggerMode,
		"resource_deps?", &resourceDepsVal,
		"ignore?", &ignoresVal,
		"auto_init?", &autoInit,
		"serve_cmd?", &serveCmdVal,
		"cmd_bat?", &updateCmdBatVal,
		"serve_cmd_bat?", &serveCmdBatVal,
		"allow_parallel?", &allowParallel,
		"links?", &links,
		"labels?", &labels,
		"env?", &updateEnv,
		"serve_env?", &serveEnv,
		"readiness_probe?", &readinessProbe,
		"dir?", &updateCmdDirVal,
		"serve_dir?", &serveCmdDirVal,
		"serve_port?", &servePortVal,
	); err != nil {
		return nil, err
	}

	resourceDeps, err := value.SequenceToStringSlice(resourceDepsVal)
	if err != nil {
		return nil, errors.Wrapf(err, "%s: resource_deps", fn.Name())
	}

	ignores, err := parseValuesToStrings(ignoresVal, "ignore")
	if err != nil {
		return nil, err
	}

	updateCmd, err := value.ValueGroupToCmdHelper(thread, updateCmdVal, updateCmdBatVal, updateCmdDirVal, updateEnv)
	if err != nil {
		return nil, err
	}
	serveCmd, err := value.ValueGroupToCmdHelper(thread, serveCmdVal, serveCmdBatVal, serveCmdDirVal, serveEnv)
	if err != nil {
		return nil, err
	}

	// Validate dir/cmd combinations to prevent common mistakes
	if updateCmdDirVal != nil && updateCmd.Empty() {
		if !serveCmd.Empty() {
			return nil, fmt.Errorf("local_resource: 'dir' only affects 'cmd', not 'serve_cmd'. Did you mean to use 'serve_dir' instead?")
		}
		return nil, fmt.Errorf("local_resource: 'dir' specified but 'cmd' is empty")
	}

	if serveCmdDirVal != nil && serveCmd.Empty() {
		return nil, fmt.Errorf("local_resource: 'serve_dir' specified but 'serve_cmd' is empty")
	}

	if updateCmd.Empty() && serveCmd.Empty() {
		return nil, fmt.Errorf("local_resource must have a cmd and/or a serve_cmd, but both were empty")
	}

	servePort := servePortVal.Int32()
	if servePort != 0 && serveCmd.Empty() {
		return nil, fmt.Errorf("local_resource: 'serve_port' specified but 'serve_cmd' is empty")
	}

	probeSpec := readinessProbe.Spec()
	if probeSpec != nil && serveCmd.Empty() {
		s.logger.Warnf("Ignoring readiness probe for local resource %q (no serve_cmd was defined)", name)
		probeSpec = nil
	}

	res := &localResource{
		name:           string(name),
		updateCmd:      updateCmd,
		serveCmd:       serveCmd,
		servePort:      int(servePort),
		threadDir:      starkit.AbsWorkingDir(thread),
		deps:           deps.Value,
		triggerMode:    triggerMode,
		autoInit:       autoInit,
		resourceDeps:   resourceDeps,
		ignores:        ignores,
		allowParallel:  allowParallel,
		links:          links.Links,
		labels:         labels.Values,
		readinessProbe: probeSpec,
	}

	// check for duplicate resources by name and throw error if found
	err = s.checkResourceConflict(res.name)
	if err != nil {
		return nil, err
	}
	s.localResources = append(s.localResources, res)
	s.localByName[res.name] = res

	return starlark.None, nil
}

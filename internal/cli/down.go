package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"github.com/tilt-dev/tilt/internal/analytics"
	ctrltiltfile "github.com/tilt-dev/tilt/internal/controllers/apis/tiltfile"
	"github.com/tilt-dev/tilt/internal/controllers/core/kubernetesapply"
	"github.com/tilt-dev/tilt/internal/k8s"
	"github.com/tilt-dev/tilt/internal/localexec"
	"github.com/tilt-dev/tilt/internal/tiltfile"
	"github.com/tilt-dev/tilt/internal/tiltfile/worktree"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/logger"
	"github.com/tilt-dev/tilt/pkg/model"
)

type downCmd struct {
	fileName         string
	deleteNamespaces bool
	deleteVolumes    bool
	// worktrees enables multi-worktree teardown (plan §9.1): `tilt down`
	// re-executes the root Tiltfile per discovered worktree and deletes
	// their manifests and clone objects. Default on — down tears down
	// everything `tilt up` loaded (plan §2); --worktrees=false restores
	// the classic single-Tiltfile teardown.
	worktrees        bool
	downDepsProvider func(ctx context.Context, tiltAnalytics *analytics.TiltAnalytics, subcommand model.TiltSubcommand) (DownDeps, error)
}

type dependencyNode struct {
	manifest   model.Manifest
	dependents []*dependencyNode
	processed  bool
}

func newDownCmd() *downCmd {
	return &downCmd{downDepsProvider: wireDownDeps}
}

func (c *downCmd) name() model.TiltSubcommand { return "down" }

func (c *downCmd) register() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "down [<tilt flags>] [-- <Tiltfile args>]",
		DisableFlagsInUseLine: true,
		Short:                 "Delete resources created by 'tilt up'",
		Long: `
Deletes resources specified in the Tiltfile

Specify additional flags and arguments to control which resources are deleted.

Namespaces are not deleted by default. Use --delete-namespaces to change that.

Docker Volumes are not deleted by default. Use --delete-volumes to change that.

Kubernetes resources with the annotation 'tilt.dev/down-policy: keep' are not deleted.

With worktrees present in the worktree dir (default .worktree/), their resources are
deleted too: each worktree's Tiltfile is re-executed and its manifests and clones are
deleted. Use --worktrees=false to delete only the main Tiltfile's resources.

For more complex cases, the Tiltfile has APIs to add additional flags and arguments to the Tilt CLI.
These arguments can be scripted to define custom subsets of resources to delete.
See https://docs.tilt.dev/tiltfile_config.html for examples.
`,
	}

	addTiltfileFlag(cmd, &c.fileName)
	addKubeContextFlag(cmd)
	addNamespaceFlag(cmd)
	cmd.Flags().BoolVar(&c.deleteNamespaces, "delete-namespaces", false, "delete namespaces defined in the Tiltfile (by default, don't)")
	cmd.Flags().BoolVar(&c.worktrees, "worktrees", true, "also tear down git worktrees discovered in the worktree dir (each worktree's Tiltfile is re-executed and its resources deleted)")
	return cmd
}

func (c *downCmd) run(ctx context.Context, args []string) error {
	a := analytics.Get(ctx)
	a.Incr("cmd.down", map[string]string{})
	defer a.Flush(time.Second)

	downDeps, err := c.downDepsProvider(ctx, a, "down")
	if err != nil {
		return err
	}
	return c.down(ctx, downDeps, args)
}

func (c *downCmd) down(ctx context.Context, downDeps DownDeps, args []string) error {
	// Worktree teardown (plan §9.1): re-execute the SAME root Tiltfile per
	// discovered worktree — the same execution `tilt up` runs — so every
	// worktree's manifests delete alongside main's.
	var wtRuns []worktreeRun
	if c.worktrees {
		var err error
		wtRuns, err = c.discoverWorktreeRuns(ctx, downDeps.tfl, args)
		if err != nil {
			return err
		}
	}

	tlr := downDeps.tfl.Load(ctx, ctrltiltfile.MainTiltfile(c.fileName, args), nil)
	err := tlr.Error
	if err != nil {
		return err
	}

	// Main-run manifests keep the existing enabled-resources filtering.
	// Every worktree manifest is additionally delete-eligible: down tears
	// down everything `tilt up` loaded (plan §9.1), and a worktree run's
	// enabled set is its own.
	manifests := tlr.Manifests
	enabledNames := append([]model.ManifestName{}, tlr.EnabledManifests...)
	seen := make(map[model.ManifestName]bool, len(tlr.Manifests))
	for _, m := range tlr.Manifests {
		seen[m.Name] = true
	}
	for _, run := range wtRuns {
		if run.tlr.Error != nil {
			// A worktree run whose Tiltfile no longer loads (e.g. the branch
			// edited it into a broken state) must not block tearing down the
			// rest: skip it and move on.
			logger.Get(ctx).Infof("Skipping worktree %q: %v", run.name, run.tlr.Error)
			continue
		}
		for _, m := range run.tlr.Manifests {
			// Keep the first definition of a name: a shared manifest
			// redefined by a worktree run is ONE cluster object, and a
			// duplicate would only produce a redundant delete.
			if !seen[m.Name] {
				seen[m.Name] = true
				manifests = append(manifests, m)
			}
			enabledNames = append(enabledNames, m.Name)
		}
	}

	sortedManifests := sortManifestsForDeletion(manifests, enabledNames)
	if err := deleteK8sEntities(ctx, sortedManifests, tlr.UpdateSettings, downDeps, c.deleteNamespaces); err != nil {
		return err
	}

	if err := deleteWorktreeClones(ctx, wtRuns, downDeps); err != nil {
		return err
	}

	dcProjects := make(map[string]v1alpha1.DockerComposeProject)
	for _, m := range sortedManifests {
		if !m.IsDC() {
			continue
		}
		proj := m.DockerComposeTarget().Spec.Project

		if _, exists := dcProjects[proj.Name]; !exists {
			dcProjects[proj.Name] = proj
		}
	}

	for _, dcProject := range dcProjects {
		dcc := downDeps.dcClient
		err = dcc.Down(ctx, dcProject, logger.Get(ctx).Writer(logger.InfoLvl), logger.Get(ctx).Writer(logger.InfoLvl), c.deleteVolumes)
		if err != nil {
			return errors.Wrap(err, "Running `docker-compose down`")
		}
	}

	return nil
}

// worktreeRun is one worktree's re-execution of the root Tiltfile.
type worktreeRun struct {
	name string
	tlr  tiltfile.TiltfileLoadResult
}

// discoverWorktreeRuns re-loads the root Tiltfile for every discovered
// worktree (plan §2 position-based discovery): the same executions `tilt up`
// performs, so `tilt down` deletes what `tilt up` created.
func (c *downCmd) discoverWorktreeRuns(ctx context.Context, tfl tiltfile.TiltfileLoader, args []string) ([]worktreeRun, error) {
	rootTiltfilePath := ctrltiltfile.ResolveFilename(c.fileName)
	wts, err := worktree.Discover(filepath.Dir(rootTiltfilePath), worktree.DefaultDir)
	if err != nil {
		return nil, err
	}

	runs := make([]worktreeRun, 0, len(wts))
	for _, wt := range wts {
		tlr := tfl.Load(ctx, ctrltiltfile.WorktreeTiltfile(wt.Name, rootTiltfilePath, args), nil)
		runs = append(runs, worktreeRun{name: wt.Name, tlr: tlr})
	}
	return runs, nil
}

// deleteWorktreeClones deletes the clone objects (plan §4.2) each worktree
// run applies alongside its YAML: the run's manifests still carry the
// stable names, so the clone set is recomputed with WorktreeClones — the
// exact objects the apply pass stamped (plan §9.1).
func deleteWorktreeClones(ctx context.Context, runs []worktreeRun, downDeps DownDeps) error {
	errs := []error{}
	for _, run := range runs {
		if run.tlr.Error != nil {
			continue
		}
		clones, err := kubernetesapply.WorktreeClones(manifestEntities(run.tlr.Manifests), run.name)
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "computing worktree clones for %q", run.name))
			continue
		}
		// The clone is a DeepCopy of the stable entity, so it inherits the
		// stable's annotations: the documented 'tilt.dev/down-policy: keep'
		// contract (cmd help text) covers clones too.
		clones, _, err = k8s.Filter(clones, func(e k8s.K8sEntity) (bool, error) {
			downPolicy, exists := e.Annotations()["tilt.dev/down-policy"]
			return !exists || downPolicy != "keep", nil
		})
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "filtering worktree clones for %q", run.name))
			continue
		}
		if len(clones) == 0 {
			continue
		}
		if err := downDeps.kClient.Delete(ctx, clones, 0); err != nil {
			errs = append(errs, errors.Wrapf(err, "deleting worktree clones for %q", run.name))
		}
	}
	return utilerrors.NewAggregate(errs)
}

// manifestEntities parses every k8s target's YAML across manifests.
func manifestEntities(manifests []model.Manifest) []k8s.K8sEntity {
	var entities []k8s.K8sEntity
	for _, m := range manifests {
		if !m.IsK8s() {
			continue
		}
		parsed, err := k8s.ParseYAMLFromString(m.K8sTarget().YAML)
		if err != nil {
			// k8sToDelete reports parse errors through its own path; here a
			// malformed worktree YAML can only mean the run was loaded by a
			// different parse path (e.g. ApplyCmd), and there is nothing to
			// recompute clones from.
			continue
		}
		entities = append(entities, parsed...)
	}
	return entities
}

func sortManifestsForDeletion(manifests []model.Manifest, enabledManifests []model.ManifestName) []model.Manifest {
	enabledNames := make(map[model.ManifestName]bool, len(enabledManifests))
	for _, n := range enabledManifests {
		enabledNames[n] = true
	}

	nodes := []*dependencyNode{}
	nodeMap := map[model.ManifestName]*dependencyNode{}

	for i := range manifests {
		manifest := manifests[len(manifests)-i-1]

		node := &dependencyNode{
			manifest:   manifest,
			dependents: []*dependencyNode{},
		}

		nodes = append(nodes, node)
		nodeMap[manifest.Name] = node
	}

	for _, node := range nodes {
		for _, resourceDep := range node.manifest.ResourceDependencies {
			if dependency, ok := nodeMap[resourceDep]; ok {
				dependency.dependents = append(dependency.dependents, node)
			}
		}
	}

	// The tiltfile loader returns all manifests,
	// with the ones that weren't selected disabled.
	var sortedManifests []model.Manifest
	for _, node := range nodes {
		for _, m := range manifestsForNode(node) {
			if enabledNames[m.Name] {
				sortedManifests = append(sortedManifests, m)
			}
		}
	}

	return sortedManifests
}

func manifestsForNode(node *dependencyNode) []model.Manifest {
	if node.processed {
		return []model.Manifest{}
	}

	node.processed = true

	var manifests []model.Manifest

	for _, dependent := range node.dependents {
		manifests = append(manifests, manifestsForNode(dependent)...)
	}

	return append(manifests, node.manifest)
}

func deleteK8sEntities(ctx context.Context, manifests []model.Manifest, updateSettings model.UpdateSettings, downDeps DownDeps, deleteNamespaces bool) error {
	kubeconfigWriter := downDeps.kubeconfigWriter
	kClient := downDeps.kClient

	entities, deleteCmds, err := k8sToDelete(manifests...)
	if err != nil {
		return errors.Wrap(err, "Parsing manifest YAML")
	}

	// If we need to inject the kubeconfig into external
	// commands, freeze it first, so that we capture all the cli flags.
	kubeconfigPath := ""
	if len(deleteCmds) > 0 {
		var err error
		kubeconfigPath, err = kubeconfigWriter.WriteFrozenKubeConfig(
			ctx,
			types.NamespacedName{Name: v1alpha1.ClusterNameDefault},
			kClient.APIConfig())
		if err != nil {
			return errors.Wrap(err, "Writing kubeconfig connection")
		}
		defer func() {
			_ = downDeps.fs.Remove(kubeconfigPath)
		}()
	}

	entities, _, err = k8s.Filter(entities, func(e k8s.K8sEntity) (b bool, err error) {
		downPolicy, exists := e.Annotations()["tilt.dev/down-policy"]
		return !exists || downPolicy != "keep", nil
	})
	if err != nil {
		return errors.Wrap(err, "Filtering entities by down policy")
	}

	if !deleteNamespaces {
		var namespaces []k8s.K8sEntity
		entities, namespaces, err = k8s.Filter(entities, func(e k8s.K8sEntity) (b bool, err error) {
			return e.GVK() != schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Namespace"}, nil
		})
		if err != nil {
			return errors.Wrap(err, "filtering out namespaces")
		}
		if len(namespaces) > 0 {
			var nsNames []string
			for _, ns := range namespaces {
				nsNames = append(nsNames, ns.Name())
			}
			logger.Get(ctx).Infof("Not deleting namespaces: %s", strings.Join(nsNames, ", "))
			logger.Get(ctx).Infof("Run with --delete-namespaces to delete namespaces as well.")
		}
	}

	errs := []error{}
	if len(entities) > 0 {
		dCtx, cancel := context.WithTimeout(ctx, updateSettings.K8sUpsertTimeout())
		err = downDeps.kClient.Delete(dCtx, entities, 0)
		cancel()
		if err != nil {
			errs = append(errs, errors.Wrap(err, "Deleting k8s entities"))
		}
	}

	for _, deleteCmd := range deleteCmds {
		dCtx, cancel := context.WithTimeout(ctx, updateSettings.K8sUpsertTimeout())
		deleteCmd.Env = append(deleteCmd.Env, fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))
		err := localexec.OneShotToLogger(dCtx, downDeps.execer, deleteCmd)
		cancel()

		if err != nil {
			errs = append(errs, errors.Wrapf(err, "Deleting k8s entities for cmd: %s", deleteCmd.String()))
		}
	}

	return utilerrors.NewAggregate(errs)
}

func k8sToDelete(manifests ...model.Manifest) ([]k8s.K8sEntity, []model.Cmd, error) {
	var allEntities []k8s.K8sEntity
	var deleteCmds []model.Cmd
	for _, m := range manifests {
		if !m.IsK8s() {
			continue
		}
		kt := m.K8sTarget()

		if kt.DeleteCmd != nil {
			deleteCmds = append(deleteCmds, model.Cmd{
				Argv: kt.DeleteCmd.Args,
				Dir:  kt.DeleteCmd.Dir,
				Env:  kt.DeleteCmd.Env,
			})
		} else {
			entities, err := k8s.ParseYAMLFromString(kt.YAML)
			if err != nil {
				return nil, nil, err
			}
			allEntities = append(allEntities, k8s.ReverseSortedEntities(entities)...)
		}
	}
	return allEntities, deleteCmds, nil
}

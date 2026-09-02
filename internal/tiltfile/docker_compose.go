package tiltfile

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"golang.org/x/exp/slices"

	"github.com/compose-spec/compose-go/v2/consts"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/distribution/reference"
	"github.com/pkg/errors"
	"go.starlark.net/starlark"
	composeyaml "gopkg.in/yaml.v3"

	"github.com/tilt-dev/tilt/internal/container"
	"github.com/tilt-dev/tilt/internal/controllers/apis/liveupdate"
	"github.com/tilt-dev/tilt/internal/dockercompose"
	"github.com/tilt-dev/tilt/internal/portregistry"
	"github.com/tilt-dev/tilt/internal/sliceutils"
	"github.com/tilt-dev/tilt/internal/tiltfile/io"
	"github.com/tilt-dev/tilt/internal/tiltfile/links"
	"github.com/tilt-dev/tilt/internal/tiltfile/starkit"
	"github.com/tilt-dev/tilt/internal/tiltfile/value"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/logger"
	"github.com/tilt-dev/tilt/pkg/model"
)

// dcResourceSet represents a single docker-compose config file and all its associated services
type dcResourceSet struct {
	Project v1alpha1.DockerComposeProject

	configPaths  []string
	tiltfilePath string
	services     map[string]*dcService
	serviceNames []string
	resOptions   map[string]*dcResourceOptions
}

type dcResourceMap map[string]*dcResourceSet

func (dc dcResourceSet) Empty() bool { return reflect.DeepEqual(dc, dcResourceSet{}) }

func (dc dcResourceSet) ServiceCount() int { return len(dc.services) }

func (dcm dcResourceMap) ServiceCount() int {
	svcCount := 0
	for _, dc := range dcm {
		svcCount += dc.ServiceCount()
	}
	return svcCount
}

func (s *tiltfileState) dockerCompose(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var configPaths starlark.Value
	var projectName string
	var profiles value.StringOrStringList
	var wait = value.Optional[starlark.Bool]{Value: false}
	envFile := value.NewLocalPathUnpacker(thread)

	err := s.unpackArgs(fn.Name(), args, kwargs,
		"configPaths", &configPaths,
		"env_file?", &envFile,
		"project_name?", &projectName,
		"profiles?", &profiles,
		"wait?", &wait,
	)
	if err != nil {
		return nil, err
	}

	paths := starlarkValueOrSequenceToSlice(configPaths)

	if len(paths) == 0 {
		return nil, fmt.Errorf("Nothing to compose")
	}

	project := v1alpha1.DockerComposeProject{
		Name:     projectName,
		EnvFile:  envFile.Value,
		Profiles: profiles.Values,
		Wait:     bool(wait.Value),
	}

	if project.EnvFile != "" {
		err = io.RecordReadPath(thread, io.WatchFileOnly, project.EnvFile)
		if err != nil {
			return nil, err
		}
	}

	for _, val := range paths {
		switch v := val.(type) {
		case nil:
			continue
		case io.Blob:
			yaml := v.String()
			message := "unable to store yaml blob"
			tmpdir, err := s.tempDir()
			if err != nil {
				return nil, errors.Wrap(err, message)
			}
			tmpfile, err := os.Create(filepath.Join(tmpdir.Path(), fmt.Sprintf("%x.yml", sha256.Sum256([]byte(yaml)))))
			if err != nil {
				return nil, errors.Wrap(err, message)
			}
			_, err = tmpfile.WriteString(yaml)
			if err != nil {
				tmpfile.Close()
				return nil, errors.Wrap(err, message)
			}
			err = tmpfile.Close()
			if err != nil {
				return nil, errors.Wrap(err, message)
			}
			project.ConfigPaths = append(project.ConfigPaths, tmpfile.Name())
		default:
			path, err := value.ValueToAbsPath(thread, val)
			if err != nil {
				return starlark.None, fmt.Errorf("expected blob | path (string). Actual type: %T", val)
			}

			// Set project path/name to dir of first compose file, like DC CLI does
			if project.ProjectPath == "" {
				project.ProjectPath = filepath.Dir(path)
			}
			if project.Name == "" {
				project.Name = loader.NormalizeProjectName(filepath.Base(filepath.Dir(path)))
			}

			project.ConfigPaths = append(project.ConfigPaths, path)
			err = io.RecordReadPath(thread, io.WatchFileOnly, path)
			if err != nil {
				return nil, err
			}
		}
	}

	currentTiltfilePath := starkit.CurrentExecPath(thread)

	if project.Name == "" {
		project.Name = loader.NormalizeProjectName(filepath.Base(filepath.Dir(currentTiltfilePath)))
	}

	// Worktree runs isolate their compose project from the main run's (plan
	// §4.5): containers/networks/volumes are keyed by project name, so two
	// worktrees plus main can compose up simultaneously without collisions.
	// The suffix applies to the dir-derived default AND an explicit
	// project_name= — the latter keeps the Tiltfile author-visible name while
	// the engine sees the per-run one (dc_resource(project_name=) translates
	// back via authoredDCProjectName). Main runs keep the authored name.
	if wt := starkit.WorktreeContextOf(thread); wt.Name != "" {
		project.Name = wtComposeProjectName(project.Name, wt.Name)
	}

	// Set to tiltfile directory for YAML blob tempfiles
	if project.ProjectPath == "" {
		project.ProjectPath = filepath.Dir(currentTiltfilePath)
	}

	if !profiles.IsSet && os.Getenv(consts.ComposeProfiles) != "" {
		logger.Get(s.ctx).Infof("Compose project %q loading profiles from environment: %s",
			project.Name, os.Getenv(consts.ComposeProfiles))
		project.Profiles = strings.Split(os.Getenv(consts.ComposeProfiles), ",")
	}

	dc := s.dc[project.Name]
	if dc == nil {
		dc = &dcResourceSet{
			Project:      project,
			services:     make(map[string]*dcService),
			resOptions:   make(map[string]*dcResourceOptions),
			configPaths:  project.ConfigPaths,
			tiltfilePath: currentTiltfilePath,
		}
		s.dc[project.Name] = dc
	} else {
		dc.configPaths = sliceutils.AppendWithoutDupes(dc.configPaths, project.ConfigPaths...)
		dc.Project.ConfigPaths = dc.configPaths
		if project.EnvFile != "" {
			dc.Project.EnvFile = project.EnvFile
		}
		project = dc.Project
	}

	services, err := parseDCConfig(s.ctx, s.dcCli, dc)
	if err != nil {
		return nil, err
	}

	dc.services = make(map[string]*dcService)
	dc.serviceNames = []string{}
	for _, svc := range services {
		err := s.checkResourceConflict(svc.Name)
		if err != nil {
			return nil, err
		}

		dc.serviceNames = append(dc.serviceNames, svc.Name)
		for _, f := range svc.ServiceConfig.EnvFiles {
			if !filepath.IsAbs(f.Path) {
				f.Path = filepath.Join(project.ProjectPath, f.Path)
			}
			err = io.RecordReadPath(thread, io.WatchFileOnly, f.Path)
			if err != nil {
				return nil, err
			}
		}
		dc.services[svc.Name] = svc
	}

	// Worktree runs: rebind published host ports via the port registry
	// (plan §4.5, §5) so two worktrees plus main can compose up
	// simultaneously. Main runs keep the authored bindings untouched.
	// The gate reads the thread's worktree context: builtins run DURING
	// ExecFile, before loadManifests stamps s.worktree.
	if starkit.WorktreeContextOf(thread).Name != "" {
		err = s.deconflictDCPorts(dc, services)
		if err != nil {
			return nil, err
		}
	}

	return starlark.None, nil
}

// DCResource allows you to adjust specific settings on a DC resource that we assume
// to be defined in a `docker_compose.yml`
func (s *tiltfileState) dcResource(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name string
	var projectName string
	var newName string
	var imageVal starlark.Value
	var triggerMode triggerMode
	var resourceDepsVal starlark.Sequence
	var inferLinks = value.Optional[starlark.Bool]{Value: true}
	var links links.LinkList
	var labels value.LabelSet
	var autoInit = value.Optional[starlark.Bool]{Value: true}

	if err := s.unpackArgs(fn.Name(), args, kwargs,
		"name", &name,
		// TODO(milas): this argument is undocumented and arguably unnecessary
		// 	now that Tilt correctly infers the Docker Compose image ref format
		"image?", &imageVal,
		"trigger_mode?", &triggerMode,
		"resource_deps?", &resourceDepsVal,
		"infer_links?", &inferLinks,
		"links?", &links,
		"labels?", &labels,
		"auto_init?", &autoInit,
		"project_name?", &projectName,
		"new_name?", &newName,
	); err != nil {
		return nil, err
	}

	if name == "" {
		return nil, fmt.Errorf("dc_resource: `name` must not be empty")
	}

	var imageRefAsStr *string
	switch imageVal := imageVal.(type) {
	case nil: // optional arg, this is fine
	case starlark.String:
		s := string(imageVal)
		imageRefAsStr = &s
	default:
		return nil, fmt.Errorf("image arg must be a string; got %T", imageVal)
	}

	projectName, svc, err := s.getDCService(name, s.authoredDCProjectName(thread, projectName))
	if err != nil {
		return nil, err
	}

	if newName != "" {
		name, err = s.renameDCService(projectName, name, newName, svc)
		if err != nil {
			return nil, err
		}
	}

	options := s.dc[projectName].resOptions[name]
	if options == nil {
		options = newDcResourceOptions()
	}

	if triggerMode != TriggerModeUnset {
		options.TriggerMode = triggerMode
	}

	if inferLinks.IsSet {
		options.InferLinks = inferLinks
	}

	options.Links = append(options.Links, links.Links...)

	for key, val := range labels.Values {
		options.Labels[key] = val
	}

	if imageRefAsStr != nil {
		normalized, err := container.ParseNamed(*imageRefAsStr)
		if err != nil {
			return nil, err
		}
		options.imageRefFromUser = normalized
	}

	rds, err := value.SequenceToStringSlice(resourceDepsVal)
	if err != nil {
		return nil, errors.Wrapf(err, "%s: resource_deps", fn.Name())
	}
	for _, dep := range rds {
		options.resourceDeps = sliceutils.AppendWithoutDupes(options.resourceDeps, dep)
	}

	if autoInit.IsSet {
		options.AutoInit = autoInit
	}

	s.dc[projectName].resOptions[name] = options
	svc.Options = options
	return starlark.None, nil
}

func (s *tiltfileState) getDCService(svcName, projName string) (string, *dcService, error) {
	allNames := []string{}
	foundProjName := ""
	var foundSvc *dcService

	for _, dc := range s.dc {
		if projName != "" && dc.Project.Name != projName {
			continue
		}

		for key, svc := range dc.services {
			if key == svcName {
				if foundSvc != nil {
					return "", nil, fmt.Errorf("found multiple resources named %q, "+
						"please specify which one with project_name= argument", svcName)
				}
				foundProjName = dc.Project.Name
				foundSvc = svc
			}
			allNames = append(allNames, key)
		}
	}

	if foundSvc == nil {
		return "", nil, fmt.Errorf("no Docker Compose service found with name %q. "+
			"Found these instead:\n\t%s", svcName, strings.Join(allNames, "; "))
	}

	return foundProjName, foundSvc, nil
}

func (s *tiltfileState) renameDCService(projectName, name, newName string, svc *dcService) (string, error) {
	err := s.checkResourceConflict(newName)
	if err != nil {
		return "", err
	}

	project := s.dc[projectName]
	services := project.services

	services[newName] = svc
	delete(services, name)
	if opts, exists := project.resOptions[name]; exists {
		project.resOptions[newName] = opts
		delete(project.resOptions, name)
	}
	index := -1
	for i, n := range project.serviceNames {
		if n == name && index == -1 {
			index = i
		} else if sd, ok := services[n].ServiceConfig.DependsOn[name]; ok {
			services[n].ServiceConfig.DependsOn[newName] = sd
			if rdIndex := slices.Index(services[n].Options.resourceDeps, name); rdIndex != -1 {
				services[n].Options.resourceDeps[rdIndex] = newName
			}
			delete(services[n].ServiceConfig.DependsOn, name)
		}
	}
	project.serviceNames[index] = newName
	svc.Name = newName
	return newName, nil
}

// A docker-compose service, according to Tilt.
type dcService struct {
	Name string

	// Contains the name of the service as referenced in the compose file where it was loaded.
	ServiceName string

	// these are the host machine paths that DC will sync from the local volume into the container
	// https://docs.docker.com/compose/compose-file/#volumes
	MountedLocalDirs []string

	// RefSelector of the image associated with this service
	// The user-provided image ref overrides the config-provided image ref
	imageRefFromConfig reference.Named // from docker-compose.yml `Image` field

	ServiceConfig types.ServiceConfig

	// Currently just use this to diff against when config files are edited to see if manifest has changed
	ServiceYAML []byte

	ImageMapDeps   []string
	PublishedPorts []int

	Options *dcResourceOptions
}

// Options set via dc_resource
type dcResourceOptions struct {
	imageRefFromUser reference.Named
	TriggerMode      triggerMode
	InferLinks       value.Optional[starlark.Bool]
	Links            []model.Link
	AutoInit         value.Optional[starlark.Bool]

	Labels map[string]string

	resourceDeps []string
}

func newDcResourceOptions() *dcResourceOptions {
	return &dcResourceOptions{
		Labels: make(map[string]string),
	}
}

func (svc dcService) ImageRef() reference.Named {
	if svc.Options != nil && svc.Options.imageRefFromUser != nil {
		return svc.Options.imageRefFromUser
	}
	return svc.imageRefFromConfig
}

func dockerComposeConfigToService(dcrs *dcResourceSet, projectName string, svcConfig types.ServiceConfig) (dcService, error) {
	var mountedLocalDirs []string
	for _, v := range svcConfig.Volumes {
		mountedLocalDirs = append(mountedLocalDirs, v.Source)
	}

	var publishedPorts []int
	for _, portSpec := range svcConfig.Ports {
		// a published port can be a string range of ports (e.g. "80-90")
		// this case is unusual and unsupported/ignored by Tilt for now
		publishedPort, err := strconv.Atoi(portSpec.Published)
		if err == nil && publishedPort != 0 {
			publishedPorts = append(publishedPorts, publishedPort)
		}
	}

	rawConfig, err := composeyaml.Marshal(svcConfig)
	if err != nil {
		return dcService{}, err
	}

	imageName := svcConfig.Image
	if imageName == "" {
		// see https://github.com/docker/compose/blob/7b84f2c2a538a1241dcf65f4b2828232189ef0ad/pkg/compose/create.go#L221-L227
		imageName = fmt.Sprintf("%s_%s", projectName, svcConfig.Name)
	}

	imageRef, err := container.ParseNamed(imageName)
	if err != nil {
		// TODO(nick): This doesn't seem like the right place to report this
		// error, but we don't really have a better way right now.
		return dcService{}, fmt.Errorf("Error parsing image name %q: %v", imageName, err)
	}

	options, exists := dcrs.resOptions[svcConfig.Name]
	if !exists {
		options = newDcResourceOptions()
		dcrs.resOptions[svcConfig.Name] = options
	}

	options.resourceDeps = sliceutils.DedupedAndSorted(
		append(options.resourceDeps, svcConfig.GetDependencies()...))

	svc := dcService{
		Name:               svcConfig.Name,
		ServiceName:        svcConfig.Name,
		ServiceConfig:      svcConfig,
		MountedLocalDirs:   mountedLocalDirs,
		ServiceYAML:        rawConfig,
		PublishedPorts:     publishedPorts,
		Options:            options,
		imageRefFromConfig: imageRef,
	}

	return svc, nil
}

func parseDCConfig(ctx context.Context, dcc dockercompose.DockerComposeClient, dcrs *dcResourceSet) ([]*dcService, error) {
	proj, err := dcc.Project(ctx, dcrs.Project)
	if err != nil {
		return nil, err
	}

	var services []*dcService
	err = proj.ForEachService(proj.ServiceNames(), func(name string, svcConfig *types.ServiceConfig) error {
		svc, err := dockerComposeConfigToService(dcrs, proj.Name, *svcConfig)
		if err != nil {
			return errors.Wrapf(err, "getting service %s", svcConfig.Name)
		}
		services = append(services, &svc)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return services, nil
}

func (s *tiltfileState) dcServiceToManifest(service *dcService, dcSet *dcResourceSet, iTargets []model.ImageTarget) (model.Manifest, error) {
	options := service.Options
	if options == nil {
		options = newDcResourceOptions()
	}

	dcInfo := model.DockerComposeTarget{
		Name: model.TargetName(service.Name),
		Spec: v1alpha1.DockerComposeServiceSpec{
			Service: service.ServiceName,
			Project: dcSet.Project,
		},
		ServiceYAML: string(service.ServiceYAML),
		Links:       options.Links,
	}.WithImageMapDeps(model.FilterLiveUpdateOnly(service.ImageMapDeps, iTargets)).
		WithPublishedPorts(service.PublishedPorts)

	if options.InferLinks.IsSet {
		dcInfo = dcInfo.WithInferLinks(bool(options.InferLinks.Value))
	}

	autoInit := true
	if options.AutoInit.IsSet {
		autoInit = bool(options.AutoInit.Value)
	}
	um, err := starlarkTriggerModeToModel(s.triggerModeForResource(options.TriggerMode), autoInit)
	if err != nil {
		return model.Manifest{}, err
	}

	var mds []model.ManifestName
	for _, md := range options.resourceDeps {
		mds = append(mds, model.ManifestName(md))
	}

	for i, iTarget := range iTargets {
		if liveupdate.IsEmptySpec(iTarget.LiveUpdateSpec) {
			continue
		}
		iTarget.LiveUpdateReconciler = true
		iTargets[i] = iTarget
	}

	m := model.Manifest{
		Name:                 model.ManifestName(service.Name),
		TriggerMode:          um,
		ResourceDependencies: mds,
	}.WithDeployTarget(dcInfo).
		WithLabels(options.Labels).
		WithImageTargets(iTargets)

	return m, nil
}

// wtComposeProjectName returns the per-run compose project name:
// `<authored>-wt-<worktree>` (loader.NormalizeProjectName characters only,
// so it stays a valid compose project name and container name prefix).
func wtComposeProjectName(authored, worktree string) string {
	return fmt.Sprintf("%s-wt-%s", authored, worktree)
}

// authoredDCProjectName strips a worktree run's project-name suffix:
// dc_resource(project_name=) is authored against the Tiltfile's name, which
// dockerCompose() suffixed for the engine (wtComposeProjectName); resolve it
// back so the builtin finds the resource set it belongs to. The run's
// worktree comes off the thread: builtins run DURING ExecFile, before
// loadManifests stamps s.worktree.
func (s *tiltfileState) authoredDCProjectName(thread *starlark.Thread, projectName string) string {
	wtName := starkit.WorktreeContextOf(thread).Name
	if wtName == "" {
		return projectName
	}
	// dc_resource(project_name=) is authored with the bare (author-visible)
	// name; the registry of parsed projects (s.dc) is keyed by the engine
	// name, which in a worktree run carries the -wt-<worktree> suffix
	// (wtComposeProjectName). Translate authored -> engine here.
	if projectName != "" {
		return wtComposeProjectName(projectName, wtName)
	}
	return projectName
}

// deconflictDCPorts rewrites the run's published host ports through the port
// registry so two worktrees (and main) can compose up simultaneously
// without clashes (plan §4.5, §5). Author-visible bindings never pass
// through the registry: main runs compose exactly what the user wrote, and
// a worktree's allocation is keyed on the authored binding (service +
// container port + protocol — wtPortOwner), so it is stable across Tiltfile
// reloads while staying distinct across worktrees — the same contract
// tk-zfi wired for portforward locals.
//
// compose-go merges ports lists across -f files (override/merge.go has no
// services.*.ports rule — sequences append), so a rewritten override file
// would ADD to the authored bindings, not replace them. Instead the full
// resolved config is re-marshaled with the new host ports and materialized
// as the project's only config file (tempDir, the same pattern as inline
// YAML blobs): runtime `compose up` binds the deconflicted ports. The
// parsed services are re-mutated in place so PublishedPorts/ServiceYAML and
// the manifest-level endpoint links match what actually runs.
//
// Caller has already stamped dcrs.Project.Name with the per-run project
// name, so allocations from two worktrees never share an owner.
func (s *tiltfileState) deconflictDCPorts(dcrs *dcResourceSet, services []*dcService) error {
	proj, err := s.dcCli.Project(s.ctx, dcrs.Project)
	if err != nil {
		return err
	}

	rewrote := false
	for _, name := range proj.ServiceNames() {
		svcConfig := proj.Services[name]
		var ports []types.ServicePortConfig
		for _, portSpec := range svcConfig.Ports {
			authored, err := strconv.Atoi(portSpec.Published)
			if err != nil || authored == 0 {
				// not a fixed host binding (e.g. an ephemeral or
				// non-numeric form): the OS assigns it, nothing to
				// deconflict
				ports = append(ports, portSpec)
				continue
			}
			port, err := portregistry.Allocate(wtPortOwner(dcrs.Project.Name, name, portSpec), 0)
			if err != nil {
				return errors.Wrapf(err, "allocating worktree port for compose service %q (authored host port %d)",
					name, authored)
			}
			rewrote = true
			portSpec.Published = strconv.Itoa(port)
			ports = append(ports, portSpec)
		}
		svcConfig.Ports = ports
		proj.Services[name] = svcConfig
	}

	if !rewrote {
		return nil
	}

	resolved, err := composeyaml.Marshal(proj)
	if err != nil {
		return errors.Wrap(err, "marshaling deconflicted docker-compose config")
	}
	tmpdir, err := s.tempDir()
	if err != nil {
		return err
	}
	tmpfile, err := os.Create(filepath.Join(tmpdir.Path(), fmt.Sprintf("%x.yml", sha256.Sum256([]byte(resolved)))))
	if err != nil {
		return err
	}
	if _, err := tmpfile.WriteString(string(resolved)); err != nil {
		tmpfile.Close()
		return err
	}
	if err := tmpfile.Close(); err != nil {
		return err
	}

	// The rewritten config replaces the authored file set everywhere the
	// project travels: runtime compose calls (projectArgs) and the
	// reload-diff (dc.configPaths).
	dcrs.Project.ConfigPaths = []string{tmpfile.Name()}
	dcrs.Project.ProjectPath = proj.WorkingDir
	dcrs.configPaths = dcrs.Project.ConfigPaths

	// Re-parse through the standard path so dcService.PublishedPorts (UI
	// endpoint links) and dcService.ServiceYAML (config diffing) carry the
	// deconflicted bindings, and authored options (dc_resource) still
	// resolve. Resolved paths come back absolute from the loader, matching
	// dockerComposeConfigToService's expectations.
	rewritten, err := parseDCConfig(s.ctx, s.dcCli, dcrs)
	if err != nil {
		return err
	}
	byName := make(map[string]*dcService, len(rewritten))
	for _, svc := range rewritten {
		byName[svc.Name] = svc
	}
	for _, svc := range services {
		updated, ok := byName[svc.Name]
		if !ok {
			continue
		}
		svc.PublishedPorts = updated.PublishedPorts
		svc.ServiceYAML = updated.ServiceYAML
		svc.ServiceConfig = updated.ServiceConfig
		svc.MountedLocalDirs = updated.MountedLocalDirs
		svc.imageRefFromConfig = updated.imageRefFromConfig
	}
	return nil
}

// wtPortOwner keys a registry allocation on the authored binding: stable
// across reloads (same binding → same owner → same port) and distinct per
// project/service/target/protocol — the project name is already the per-run
// suffixed one (wtComposeProjectName), so two worktrees never share an
// owner, and two projects in one worktree don't either.
func wtPortOwner(projectName, serviceName string, portSpec types.ServicePortConfig) string {
	return fmt.Sprintf("dc:%s/%s:%d/%s", projectName, serviceName, portSpec.Target, portSpec.Protocol)
}

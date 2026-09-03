package tiltfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/semver"

	"github.com/tilt-dev/tilt/internal/controllers/apis/liveupdate"
	ctrltiltfile "github.com/tilt-dev/tilt/internal/controllers/apis/tiltfile"
	"github.com/tilt-dev/tilt/internal/dockercompose"
	"github.com/tilt-dev/tilt/internal/portregistry"
	"github.com/tilt-dev/tilt/internal/tiltfile/worktree"
	"github.com/tilt-dev/tilt/pkg/model"
)

const simpleConfig = `version: '3'
services:
  foo:
    build: ./foo
    command: sleep 100
    ports:
      - "12312:80"`

const configWithMounts = `version: '3.2'
services:
  foo:
    build: ./foo
    command: sleep 100
    volumes:
      - ./foo:/foo
      # these volumes are currently unsupported, but included here to ensure we don't blow up on them
      - bar:/bar
      - type: volume
        source: baz
        target: /baz
    ports:
      - "12312:80"
volumes:
  bar: {}
  baz: {}`

const barServiceConfig = `version: '3'
services:
  bar:
    image: bar-image
    expose:
      - "3000"
    depends_on:
      - foo
`

const twoServiceConfig = `version: '3'
services:
  foo:
    build: ./foo
    command: sleep 100
    ports:
      - "12312:80"
  bar:
    image: bar-image
    expose:
      - "3000"
    depends_on:
      - foo
`

const twoServiceConfigWithProfiles = `version: '3'
services:
  foo:
    build: ./foo
    command: sleep 100
    ports:
      - "12312:80"
  bar:
    image: bar-image
    expose:
      - "3000"
    depends_on:
      - foo
    profiles:
      - barprofile
`

const threeServiceConfig = `version: '3'
services:
  db:
    image: db-image
  foo:
    image: foo-image
    command: sleep 100
    ports:
      - "12312:80"
    depends_on:
      - db
  bar:
    image: bar-image
    expose:
      - "3000"
    depends_on:
      - db
      - foo
`

// YAML for Foo config looks a little different from the above after being read into
// a struct and YAML'd back out...
func (f *fixture) simpleConfigAfterParse() string {
	return fmt.Sprintf(`build:
    context: %s
    dockerfile: Dockerfile
command:
    - sleep
    - "100"
networks:
    default: null
ports:
    - mode: ingress
      target: 80
      published: "12312"
      protocol: tcp`, f.JoinPath("foo"))
}

func TestDockerComposeNothingError(t *testing.T) {
	f := newFixture(t)

	f.file("Tiltfile", "docker_compose(None)")

	f.loadErrString("Nothing to compose")
}

func TestBuildURL(t *testing.T) {
	f := newFixture(t)

	f.file("Tiltfile", "docker_compose('docker-compose.yml')")

	f.file("docker-compose.yml", `services:
  app:
    command: sh -c 'node server.js'
    build: https://github.com/tilt-dev/tilt-docker-compose-example.git
    ports:
    - published: 3000
      target: 30
`)
	f.load()
}

func TestDockerComposeBadTypeError(t *testing.T) {
	f := newFixture(t)

	f.file("Tiltfile", "docker_compose(True)")

	f.loadErrString("expected blob | path (string). Actual type: starlark.Bool")
}

func TestDockerComposeManifest(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", simpleConfig)
	f.file("Tiltfile", "docker_compose('docker-compose.yml')")

	f.load()
	f.assertDcManifest("foo",
		dcServiceYAML(f.simpleConfigAfterParse()),
		dockerComposeManagedImage(f.JoinPath("foo", "Dockerfile"), f.JoinPath("foo")),
		dcPublishedPorts(12312),
	)

	expectedConfFiles := []string{
		"Tiltfile",
		".tiltignore",
		"docker-compose.yml",
		f.JoinPath("foo", ".dockerignore"),
	}
	f.assertConfigFiles(expectedConfFiles...)
}

func TestDockerComposeEnvFile(t *testing.T) {
	f := newFixture(t)

	f.file("docker-compose.yml", `services:
  bar:
    image: bar-image
    ports:
      - "$BAR_PORT:$BAR_PORT"
`)
	f.file("local.env", "BAR_PORT=4000\n")
	f.file("Tiltfile", "docker_compose('docker-compose.yml', env_file='local.env')")

	f.load()
	f.assertDcManifest("bar", dcPublishedPorts(4000))

	expectedConfFiles := []string{
		"Tiltfile",
		".tiltignore",
		"local.env",
		"docker-compose.yml",
	}
	f.assertConfigFiles(expectedConfFiles...)
}

func TestDockerComposeServiceEnvFile(t *testing.T) {
	f := newFixture(t)

	f.file("docker-compose.yml", `services:
  bar:
    image: bar-image
    env_file:
      - bar.env
`)
	f.file("bar.env", "BAR_PORT=4000\n")
	f.file("Tiltfile", "docker_compose('docker-compose.yml')")

	f.load()
	f.assertDcManifest("bar")

	expectedConfFiles := []string{
		"Tiltfile",
		".tiltignore",
		"docker-compose.yml",
		"bar.env",
	}
	f.assertConfigFiles(expectedConfFiles...)
}

func TestDockerComposeProjectName(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", simpleConfig)
	f.file("Tiltfile", `docker_compose('docker-compose.yml', project_name='hello')`)

	f.load()
	m := f.assertDcManifest("foo")
	require.Equal(t, "hello", m.DockerComposeTarget().Spec.Project.Name)
}

func TestDockerComposeConflict(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", simpleConfig)
	f.file("Tiltfile", `
local_resource("foo", "foo")
docker_compose('docker-compose.yml')
`)

	f.loadErrString(`local_resource named "foo" already exists`)
}

func TestDockerComposeYAMLBlob(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", simpleConfig)
	f.file("Tiltfile", "docker_compose(read_file('docker-compose.yml'))")

	f.load()
	f.assertDcManifest("foo",
		dcServiceYAML(f.simpleConfigAfterParse()),
		dockerComposeManagedImage(f.JoinPath("foo", "Dockerfile"), f.JoinPath("foo")),
		dcPublishedPorts(12312),
	)

	expectedConfFiles := []string{
		"Tiltfile",
		".tiltignore",
		"docker-compose.yml",
		f.JoinPath("foo", ".dockerignore"),
	}
	f.assertConfigFiles(expectedConfFiles...)
}

func TestDockerComposeTwoInlineBlobs(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("Tiltfile", fmt.Sprintf(`docker_compose([blob("""\n%s\n"""), blob("""\n%s\n""")])`, simpleConfig, barServiceConfig))

	f.load()

	assert.Equal(t, 2, len(f.loadResult.Manifests))
}

func TestDockerComposeBlobAndFileUsesFileDirForProjectPath(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", simpleConfig)
	f.file("Tiltfile", fmt.Sprintf(`docker_compose([blob("""\n%s\n"""), 'docker-compose.yml'])`, barServiceConfig))

	f.load()

	assert.Equal(t, 2, len(f.loadResult.Manifests))
	f.assertDcManifest("foo",
		dcServiceYAML(f.simpleConfigAfterParse()),
		dockerComposeManagedImage(f.JoinPath("foo", "Dockerfile"), f.JoinPath("foo")),
		dcPublishedPorts(12312),
	)
}

func TestDockerComposeManifestNoDockerfile(t *testing.T) {
	f := newFixture(t)

	f.file("docker-compose.yml", `version: '3'
services:
  bar:
    image: redis:alpine`)
	f.file("Tiltfile", "docker_compose('docker-compose.yml')")

	expectedYAML := `image: redis:alpine
networks:
    default: null`

	f.load("bar")
	f.assertDcManifest("bar",
		dcServiceYAML(expectedYAML),
		noImage(),
		// TODO(maia): assert m.tiltFilename
	)

	expectedConfFiles := []string{"Tiltfile", ".tiltignore", "docker-compose.yml"}
	f.assertConfigFiles(expectedConfFiles...)
}

func TestDockerComposeManifestAlternateDockerfile(t *testing.T) {
	f := newFixture(t)

	f.dockerfile("baz/alternate-Dockerfile")
	f.file("docker-compose.yml", fmt.Sprintf(`
version: '3'
services:
  baz:
    build:
      context: %s
      dockerfile: alternate-Dockerfile`, f.JoinPath("baz")))
	f.file("Tiltfile", "docker_compose('docker-compose.yml')")

	expectedYAML := fmt.Sprintf(`build:
    context: %s
    dockerfile: alternate-Dockerfile
networks:
    default: null`,
		f.JoinPath("baz"))

	f.load("baz")
	f.assertDcManifest("baz",
		dcServiceYAML(expectedYAML),
		dockerComposeManagedImage(f.JoinPath("baz", "alternate-Dockerfile"), f.JoinPath("baz")),
		// TODO(maia): assert m.tiltFilename
	)

	expectedConfFiles := []string{"Tiltfile", ".tiltignore", "docker-compose.yml", "baz/.dockerignore"}
	f.assertConfigFiles(expectedConfFiles...)
}

func TestDockerComposeManifestAbsoluteDockerfile(t *testing.T) {
	f := newFixture(t)

	dockerfilePath := f.JoinPath("baz", "Dockerfile")
	f.dockerfile(dockerfilePath)
	f.file("docker-compose.yml", fmt.Sprintf(`
version: '3'
services:
  baz:
    build:
      context: %s
      dockerfile: %s`, f.JoinPath("baz"), dockerfilePath))
	f.file("Tiltfile", "docker_compose('docker-compose.yml')")

	expectedYAML := fmt.Sprintf(`build:
    context: %s
    dockerfile: %s
networks:
    default: null`,
		f.JoinPath("baz"),
		dockerfilePath)

	f.load("baz")
	f.assertDcManifest("baz",
		dcServiceYAML(expectedYAML),
		dockerComposeManagedImage(f.JoinPath("baz", "alternate-Dockerfile"), f.JoinPath("baz")),
		// TODO(maia): assert m.tiltFilename
	)

	expectedConfFiles := []string{"Tiltfile", ".tiltignore", "docker-compose.yml", "baz/.dockerignore"}
	f.assertConfigFiles(expectedConfFiles...)
}

func TestDockerComposeManifestAlternateDockerfileAndDockerIgnore(t *testing.T) {
	f := newFixture(t)

	f.dockerfile("baz/alternate-Dockerfile")
	f.dockerignore("baz/alternate-Dockerfile.dockerignore")
	f.file("docker-compose.yml", fmt.Sprintf(`
version: '3'
services:
  baz:
    build:
      context: %s
      dockerfile: alternate-Dockerfile`, f.JoinPath("baz")))
	f.file("Tiltfile", "docker_compose('docker-compose.yml')")

	expectedYAML := fmt.Sprintf(`build:
    context: %s
    dockerfile: alternate-Dockerfile
networks:
    default: null`,
		f.JoinPath("baz"))

	f.load("baz")
	f.assertDcManifest("baz",
		dcServiceYAML(expectedYAML),
		dockerComposeManagedImage(f.JoinPath("baz", "alternate-Dockerfile"), f.JoinPath("baz")),
		// TODO(maia): assert m.tiltFilename
	)

	expectedConfFiles := []string{
		"Tiltfile",
		".tiltignore",
		"docker-compose.yml",
		"baz/alternate-Dockerfile.dockerignore",
	}
	f.assertConfigFiles(expectedConfFiles...)
}

func TestMultipleDockerComposeDifferentDirs(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose1.yml", simpleConfig)

	f.dockerfile(filepath.Join("subdir", "foo", "Dockerfile"))
	f.file(filepath.Join("subdir", "Tiltfile"), `docker_compose('docker-compose2.yml')`)
	f.file(filepath.Join("subdir", "docker-compose2.yml"), simpleConfig)

	tf := `
include('./subdir/Tiltfile')
dc_resource('foo', project_name='subdir', new_name='foo2')
docker_compose('docker-compose1.yml')`
	f.file("Tiltfile", tf)

	f.load()

	assert.Equal(t, 2, len(f.loadResult.Manifests))
}

func TestMultipleDockerComposeNameConflict(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose1.yml", simpleConfig)

	f.dockerfile(filepath.Join("subdir", "foo", "Dockerfile"))
	f.file(filepath.Join("subdir", "Tiltfile"), `docker_compose('docker-compose2.yml')`)
	f.file(filepath.Join("subdir", "docker-compose2.yml"), simpleConfig)

	tf := `
include('./subdir/Tiltfile')
docker_compose('docker-compose1.yml')`
	f.file("Tiltfile", tf)

	f.loadErrString(`dc_resource named "foo" already exists`)
}

func TestDockerComposeNewNameWithDependencies(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		renames map[string]string
	}{
		{
			"default",
			make(map[string]string),
		},
		{
			"rename db",
			map[string]string{"db": "db2"},
		},
		{
			"rename foo",
			map[string]string{"foo": "foo2"},
		},
		{
			"rename bar",
			map[string]string{"bar": "bar2"},
		},
		{
			"rename foo + bar",
			map[string]string{
				"foo": "foo2",
				"bar": "bar2",
			},
		},
		{
			"rename db + foo + bar",
			map[string]string{
				"db":  "db2",
				"foo": "foo2",
				"bar": "bar2",
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			f := newFixture(t)

			f.file("docker-compose.yml", threeServiceConfig)

			tf := "docker_compose('docker-compose.yml')\n"

			allNames := map[string]string{
				"db":  "db",
				"foo": "foo",
				"bar": "bar",
			}

			for oldName, newName := range testCase.renames {
				tf += fmt.Sprintf("dc_resource('%s', new_name='%s')\n", oldName, newName)
				allNames[oldName] = newName
			}

			f.file("Tiltfile", tf)

			f.load()

			f.assertNextManifest(model.ManifestName(allNames["db"]), resourceDeps())
			f.assertNextManifest(model.ManifestName(allNames["foo"]), resourceDeps(allNames["db"]))
			f.assertNextManifest(model.ManifestName(allNames["bar"]), resourceDeps(allNames["db"], allNames["foo"]))
		})
	}
}

func TestMultipleDockerComposeSameDir(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose1.yml", simpleConfig)
	f.file("docker-compose2.yml", barServiceConfig)

	tf := `
docker_compose('docker-compose1.yml')
docker_compose('docker-compose2.yml')`
	f.file("Tiltfile", tf)

	f.load()

	assert.Equal(t, 2, len(f.loadResult.Manifests))
}

func TestDockerComposeAndK8sSupported(t *testing.T) {
	f := newFixture(t)

	f.setupFooAndBar()
	f.file("docker-compose.yml", simpleConfig)
	tf := `docker_compose('docker-compose.yml')
k8s_yaml('bar.yaml')`
	f.file("Tiltfile", tf)

	f.load()

	assert.Equal(t, 2, len(f.loadResult.Manifests))
}

func TestResourceConflictCombinations(t *testing.T) {
	tt := [][2]string{
		{`docker_compose('docker-compose.yml')
k8s_yaml('foo.yaml')`, `dc_resource named "foo" already exists`},
		{`k8s_yaml('foo.yaml')
docker_compose('docker-compose.yml')`, `dc_resource named "foo" already exists`},
		{`docker_compose('docker-compose.yml')
local_resource('foo', 'echo hello')`, `dc_resource named "foo" already exists`},
		{`local_resource('foo', 'echo hello')
docker_compose('docker-compose.yml')`, `local_resource named "foo" already exists`},
	}

	for i, tc := range tt {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			f := newFixture(t)
			f.setupFooAndBar()
			f.file("docker-compose.yml", simpleConfig)
			f.file("Tiltfile", tc[0])
			f.loadErrString(tc[1])
		})
	}
}

func TestDockerComposeResourceCreationFromAbsPath(t *testing.T) {
	f := newFixture(t)

	configPath := f.TempDirFixture.JoinPath("docker-compose.yml")
	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", `
version: '3'
services:
  foo:
    build: ./foo
    command: sleep 100
    ports:
      - "12312:80"`)
	f.file("Tiltfile", fmt.Sprintf("docker_compose(%q)", configPath))

	f.load("foo")
	f.assertDcManifest("foo")
}

func TestDockerComposeMultiStageBuild(t *testing.T) {
	f := newFixture(t)

	df := `FROM alpine as builder
ADD ./src /app
RUN echo hi

FROM alpine
COPY --from=builder /app /app
RUN echo bye`
	f.file(filepath.Join("foo", "Dockerfile"), df)
	f.file(filepath.Join("foo", "docker-compose.yml"), `version: '3'
services:
  foo:
    build:
      context: ./
    command: sleep 100
    ports:
      - "12312:80"`)
	f.file("Tiltfile", "docker_compose('foo/docker-compose.yml')")
	f.load("foo")
	f.assertDcManifest("foo",
		dcServiceYAML(f.simpleConfigAfterParse()),
		dockerComposeManagedImage(f.JoinPath("foo", "Dockerfile"), f.JoinPath("foo")),
		dcPublishedPorts(12312),
	)

	expectedConfFiles := []string{
		"Tiltfile",
		".tiltignore",
		filepath.Join("foo", "docker-compose.yml"),
		filepath.Join("foo", ".dockerignore"),
	}
	f.assertConfigFiles(expectedConfFiles...)
}

func TestDockerComposeHonorsDockerIgnore(t *testing.T) {
	f := newFixture(t)

	df := `FROM alpine

ADD . /app
COPY ./thing.go /stuff
RUN echo hi`
	f.file(filepath.Join("foo", "Dockerfile"), df)

	f.file("docker-compose.yml", simpleConfig)
	f.file("Tiltfile", "docker_compose('docker-compose.yml')")

	// the build context is ./foo so tmp should be ignored
	f.file(filepath.Join("foo", ".dockerignore"), "tmp")
	// this dockerignore is unrelated despite being a sibling to docker-compose.yml, so won't be used
	f.file(".dockerignore", "foo/tmp2")

	f.load("foo")

	f.assertNextManifest("foo",
		fileChangeMatches(filepath.Join("foo", "tmp2")),
		fileChangeFilters(filepath.Join("foo", "tmp")),
	)
}

func TestDockerComposeIgnoresFileChangesOnMountedVolumes(t *testing.T) {
	f := newFixture(t)

	df := `FROM alpine

ADD . /app
COPY ./thing.go /stuff
RUN echo hi`
	f.file(filepath.Join("foo", "Dockerfile"), df)

	f.file("docker-compose.yml", configWithMounts)
	f.file("Tiltfile", "docker_compose('docker-compose.yml')")

	f.load("foo")

	f.assertNextManifest("foo",
		// ensure that DC syncs *are* ignored for file watching, i.e., won't trigger builds
		fileChangeFilters(filepath.Join("foo", "blah")),
	)
}

func TestDockerComposeWithDockerBuild(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", simpleConfig)
	f.file("Tiltfile", `docker_build('gcr.io/foo', './foo')
docker_compose('docker-compose.yml')
dc_resource('foo', 'gcr.io/foo')
`)

	f.load()

	m := f.assertNextManifest("foo", db(image("gcr.io/foo")))
	iTarget := m.ImageTargetAt(0)

	// Make sure there's no live update in the default case.
	assert.True(t, iTarget.IsDockerBuild())
	assert.True(t, liveupdate.IsEmptySpec(iTarget.LiveUpdateSpec))

	configPath := f.TempDirFixture.JoinPath("docker-compose.yml")
	assert.Equal(t, m.DockerComposeTarget().Spec.Project.ConfigPaths, []string{configPath})
}

func TestDockerComposeWithDockerBuildAutoAssociate(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", `version: '3'
services:
  foo:
    image: gcr.io/as_specified_in_config
    build: ./foo
    command: sleep 100
    ports:
      - "12312:80"`)
	f.file("Tiltfile", `docker_build('gcr.io/as_specified_in_config', './foo')
docker_compose('docker-compose.yml')
`)

	f.load()

	// don't need a dc_resource call if the docker_build image matches the
	// `Image` specified in dc.yml
	m := f.assertNextManifest("foo", db(image("gcr.io/as_specified_in_config")))
	iTarget := m.ImageTargetAt(0)

	// Make sure there's no live update in the default case.
	assert.True(t, iTarget.IsDockerBuild())
	assert.True(t, liveupdate.IsEmptySpec(iTarget.LiveUpdateSpec))

	configPath := f.TempDirFixture.JoinPath("docker-compose.yml")
	assert.Equal(t, m.DockerComposeTarget().Spec.Project.ConfigPaths, []string{configPath})
}

// I.e. make sure that we handle de/normalization between `fooimage` <--> `docker.io/library/fooimage`
func TestDockerComposeWithDockerBuildLocalRef(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", simpleConfig)
	f.file("Tiltfile", `docker_build('fooimage', './foo')
docker_compose('docker-compose.yml')
dc_resource('foo', 'fooimage')
`)

	f.load()

	m := f.assertNextManifest("foo", db(image("fooimage")))
	assert.True(t, m.ImageTargetAt(0).IsDockerBuild())

	configPath := f.TempDirFixture.JoinPath("docker-compose.yml")
	assert.Equal(t, m.DockerComposeTarget().Spec.Project.ConfigPaths,
		[]string{configPath})
}

func TestDockerComposeWithProfiles(t *testing.T) {
	t.Run("include resource without profile", func(t *testing.T) {
		f := newFixture(t)

		f.setupFoo()
		f.file("docker-compose.yml", twoServiceConfigWithProfiles)
		f.file("Tiltfile", `docker_compose('docker-compose.yml')
dc_resource('foo')
`)
		f.load()

		_ = f.assertNextManifest("foo")
		f.assertNoMoreManifests()
	})

	t.Run("include specified profile", func(t *testing.T) {
		f := newFixture(t)

		f.setupFoo()
		f.file("docker-compose.yml", twoServiceConfigWithProfiles)
		f.file("Tiltfile", `docker_compose('docker-compose.yml', profiles=["barprofile"])
dc_resource('foo')
dc_resource('bar')
`)
		f.load()

		_ = f.assertNextManifest("foo")
		_ = f.assertNextManifest("bar")
		f.assertNoMoreManifests()
	})

	t.Run("include specified profile from env var", func(t *testing.T) {
		f := newFixture(t)
		t.Setenv("COMPOSE_PROFILES", "barprofile")

		f.setupFoo()
		f.file("docker-compose.yml", twoServiceConfigWithProfiles)
		f.file("Tiltfile", `docker_compose('docker-compose.yml')
dc_resource('foo')
dc_resource('bar')
`)
		f.load()

		_ = f.assertNextManifest("foo")
		_ = f.assertNextManifest("bar")
		f.assertNoMoreManifests()
	})

	t.Run("must include profile to have resource", func(t *testing.T) {
		f := newFixture(t)

		f.setupFoo()
		f.file("docker-compose.yml", twoServiceConfigWithProfiles)
		f.file("Tiltfile", `docker_compose('docker-compose.yml')
dc_resource('bar')
`)
		f.loadErrString("Error in dc_resource: no Docker Compose service found with name \"bar\".")
		f.assertNoMoreManifests()
	})
}

func TestMultipleDockerComposeWithDockerBuild(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.dockerfile(filepath.Join("bar", "Dockerfile"))
	f.file("docker-compose.yml", twoServiceConfig)
	f.file("Tiltfile", `docker_build('gcr.io/foo', './foo')
docker_build('gcr.io/bar', './bar')
docker_compose('docker-compose.yml')
dc_resource('foo', 'gcr.io/foo')
dc_resource('bar', 'gcr.io/bar')
`)

	f.load()

	foo := f.assertNextManifest("foo", db(image("gcr.io/foo")))
	assert.True(t, foo.ImageTargetAt(0).IsDockerBuild())

	bar := f.assertNextManifest("bar", db(image("gcr.io/bar")))
	assert.True(t, foo.ImageTargetAt(0).IsDockerBuild())

	configPath := f.TempDirFixture.JoinPath("docker-compose.yml")
	assert.Equal(t, foo.DockerComposeTarget().Spec.Project.ConfigPaths, []string{configPath})
	assert.Equal(t, bar.DockerComposeTarget().Spec.Project.ConfigPaths, []string{configPath})
}

func TestMultipleDockerComposeWithDockerBuildImageNames(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.dockerfile(filepath.Join("bar", "Dockerfile"))
	config := `version: '3'
services:
  foo:
    image: gcr.io/foo
  bar:
    image: gcr.io/bar
    depends_on: [foo]
`
	f.file("docker-compose.yml", config)
	f.file("Tiltfile", `
docker_build('gcr.io/foo', './foo')
docker_build('gcr.io/bar', './bar')
docker_compose('docker-compose.yml')
`)

	f.load()

	foo := f.assertNextManifest("foo", db(image("gcr.io/foo")))
	assert.True(t, foo.ImageTargetAt(0).IsDockerBuild())

	bar := f.assertNextManifest("bar", db(image("gcr.io/bar")))
	assert.True(t, bar.ImageTargetAt(0).IsDockerBuild())

	configPath := f.TempDirFixture.JoinPath("docker-compose.yml")
	assert.Equal(t, foo.DockerComposeTarget().Spec.Project.ConfigPaths, []string{configPath})
	assert.Equal(t, bar.DockerComposeTarget().Spec.Project.ConfigPaths, []string{configPath})
}

func TestDCImageRefSuggestion(t *testing.T) {
	f := newFixture(t)

	f.setupFoo()
	f.file("docker-compose.yml", `version: '3'
services:
  foo:
    image: gcr.io/foo
`)
	f.file("Tiltfile", `
docker_build('gcr.typo.io/foo', 'foo')
docker_compose('docker-compose.yml')
`)
	f.loadAssertWarnings(`Image not used in any Docker Compose config:
    ✕ gcr.typo.io/foo
Did you mean…
    - gcr.io/foo
Skipping this image build
If this is deliberate, suppress this warning with: update_settings(suppress_unused_image_warnings=["gcr.typo.io/foo"])`)
}

func TestDockerComposeOnlySomeWithDockerBuild(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", twoServiceConfig)
	f.file("Tiltfile", `img_name = 'gcr.io/foo'
docker_build(img_name, './foo')
docker_compose('docker-compose.yml')
dc_resource('foo', img_name)
`)

	f.load()

	foo := f.assertNextManifest("foo", db(image("gcr.io/foo")))
	assert.True(t, foo.ImageTargetAt(0).IsDockerBuild())

	bar := f.assertNextManifest("bar")
	assert.Empty(t, bar.ImageTargets)

	configPath := f.TempDirFixture.JoinPath("docker-compose.yml")
	assert.Equal(t, foo.DockerComposeTarget().Spec.Project.ConfigPaths, []string{configPath})
	assert.Equal(t, bar.DockerComposeTarget().Spec.Project.ConfigPaths, []string{configPath})
}

func TestDockerComposeResourceNoImageMatch(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", simpleConfig)
	f.file("Tiltfile", `docker_build('gcr.io/foo', './foo')
docker_compose('docker-compose.yml')
dc_resource('no-svc-with-this-name-eek', 'gcr.io/foo')
`)
	f.loadErrString("no Docker Compose service found with name")
}

func TestDockerComposeLoadConfigFilesOnFailure(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", simpleConfig)
	f.file("Tiltfile", `docker_build('gcr.io/foo', './foo')
docker_compose('docker-compose.yml')
fail("deliberate exit")
`)
	f.loadErrString("deliberate exit")

	// Make sure that even though tiltfile execution failed, we still
	// loaded config files correctly.
	f.assertConfigFiles(".tiltignore", "Tiltfile", "docker-compose.yml", "foo/Dockerfile")
}

func TestDockerComposeDoesntSupportEntrypointOverride(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", simpleConfig)
	f.file("Tiltfile", `docker_build('gcr.io/foo', './foo', entrypoint='./foo')
docker_compose('docker-compose.yml')
dc_resource('foo', 'gcr.io/foo')
`)

	f.loadErrString("docker_build/custom_build.entrypoint not supported for Docker Compose resources")
}

func TestDefaultRegistryWithDockerCompose(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", simpleConfig)
	f.file("Tiltfile", `
docker_compose('docker-compose.yml')
default_registry('bar.com')
`)

	f.loadErrString("default_registry is not supported with docker compose")
}

func TestDockerComposeLabels(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", simpleConfig)
	f.file("Tiltfile", `
docker_compose('docker-compose.yml')
dc_resource("foo", labels="test")
`)

	f.load("foo")
	f.assertNextManifest("foo", resourceLabels("test"))
}

func TestMultitleDockerComposeLabels(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", simpleConfig)
	f.file("docker-compose2.yml", barServiceConfig)
	f.file("Tiltfile", `
docker_compose('docker-compose.yml')
dc_resource("foo", labels="test")

docker_compose('docker-compose2.yml')
dc_resource("bar", labels="run")
`)

	f.load()
	f.assertNextManifest("foo", resourceLabels("test"))
	f.assertNextManifest("bar", resourceLabels("run"))
}

func TestTriggerModeDC(t *testing.T) {
	for _, testCase := range []struct {
		name                string
		globalSetting       triggerMode
		dcResourceSetting   triggerMode
		specifyAutoInit     bool
		autoInit            bool
		expectedTriggerMode model.TriggerMode
	}{
		{"default", TriggerModeUnset, TriggerModeUnset, false, false, model.TriggerModeAuto},
		{"explicit global auto", TriggerModeAuto, TriggerModeUnset, false, false, model.TriggerModeAuto},
		{"explicit global manual", TriggerModeManual, TriggerModeUnset, false, false, model.TriggerModeManualWithAutoInit},
		{"dc auto", TriggerModeUnset, TriggerModeUnset, false, false, model.TriggerModeAuto},
		{"dc manual", TriggerModeUnset, TriggerModeManual, false, false, model.TriggerModeManualWithAutoInit},
		{"dc manual, auto_init=False", TriggerModeUnset, TriggerModeManual, true, false, model.TriggerModeManual},
		{"dc manual, auto_init=True", TriggerModeUnset, TriggerModeManual, true, true, model.TriggerModeManualWithAutoInit},
		{"dc override auto", TriggerModeManual, TriggerModeAuto, false, false, model.TriggerModeAuto},
		{"dc override manual", TriggerModeAuto, TriggerModeManual, false, false, model.TriggerModeManualWithAutoInit},
		{"dc override manual, auto_init=False", TriggerModeAuto, TriggerModeManual, true, false, model.TriggerModeManual},
		{"dc override manual, auto_init=True", TriggerModeAuto, TriggerModeManual, true, true, model.TriggerModeManualWithAutoInit},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			f := newFixture(t)

			f.dockerfile(filepath.Join("foo", "Dockerfile"))
			f.file("docker-compose.yml", simpleConfig)

			var globalTriggerModeDirective string
			switch testCase.globalSetting {
			case TriggerModeUnset:
				globalTriggerModeDirective = ""
			default:
				globalTriggerModeDirective = fmt.Sprintf("trigger_mode(%s)", testCase.globalSetting.String())
			}

			var dcResourceDirective string
			switch testCase.dcResourceSetting {
			case TriggerModeUnset:
				dcResourceDirective = ""
			default:
				autoInitOption := ""
				if testCase.specifyAutoInit {
					autoInitOption = ", auto_init="
					if testCase.autoInit {
						autoInitOption += "True"
					} else {
						autoInitOption += "False"
					}
				}
				dcResourceDirective = fmt.Sprintf("dc_resource('foo', trigger_mode=%s%s)", testCase.dcResourceSetting.String(), autoInitOption)
			}

			f.file("Tiltfile", fmt.Sprintf(`
%s
docker_compose('docker-compose.yml')
%s
`, globalTriggerModeDirective, dcResourceDirective))

			f.load()

			f.assertNumManifests(1)
			f.assertNextManifest("foo", testCase.expectedTriggerMode)
		})
	}
}

func TestDCResourceNoImage(t *testing.T) {
	f := newFixture(t)

	f.setupFoo()
	f.file("docker-compose.yml", simpleConfig)
	f.file("Tiltfile", `
docker_compose('docker-compose.yml')
dc_resource('foo', trigger_mode=TRIGGER_MODE_AUTO)
`)

	f.load()
}

func TestDCDependsOnInferredFromComposeFile(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", twoServiceConfig)
	f.file("Tiltfile", `
docker_compose('docker-compose.yml')
`)

	f.load()
	f.assertNextManifest("foo", resourceDeps())
	f.assertNextManifest("bar", resourceDeps("foo"))
}

func TestDCDependsOnResourceDepSpecified(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", twoServiceConfig)
	f.file("Tiltfile", `
docker_compose('docker-compose.yml')
dc_resource('bar', resource_deps=['foo'])
`)

	f.load()
	f.assertNextManifest("foo", resourceDeps())
	f.assertNextManifest("bar", resourceDeps("foo"))
}

func TestDockerComposeVersionWarnings(t *testing.T) {
	type tc struct {
		version string
		warning string
		error   string
	}
	tcs := []tc{
		{version: "v1.28.0", error: "Tilt requires Docker Compose v1.28.3+ (you have v1.28.0). Please upgrade and re-launch Tilt."},
		{version: "v2.0.0-rc.3", warning: "Using Docker Compose v2.0.0-rc.3 (version < 2.2) may result in errors or broken functionality.\n" +
			"For best results, we recommend upgrading to Docker Compose >= v2.2.0."},
		{version: "v1.29.2" /* no errors or warnings */},
		{version: "v2.2.0" /* no errors or warnings */},
	}

	for _, tc := range tcs {
		t.Run(tc.version, func(t *testing.T) {
			f := newFixture(t)

			f.dockerfile(filepath.Join("foo", "Dockerfile"))
			f.file("docker-compose.yml", simpleConfig)
			f.file("Tiltfile", "docker_compose('docker-compose.yml')")

			f.load("foo")

			loader := f.newTiltfileLoader()
			if tl, ok := loader.(tiltfileLoader); ok {
				dcCli := dockercompose.NewFakeDockerComposeClient(t, f.ctx)
				dcCli.ConfigOutput = simpleConfig
				dcCli.VersionOutput = semver.Canonical(tc.version)
				tl.dcCli = dcCli
				loader = tl
			} else {
				require.Fail(t, "Could not set up fake Docker Compose client")
			}

			f.loadResult = loader.Load(f.ctx, ctrltiltfile.MainTiltfile(f.JoinPath("Tiltfile"), nil), nil)
			if tc.error == "" {
				require.NoError(t, f.loadResult.Error, "Tiltfile load result had unexpected error")
			} else {
				require.Contains(t, f.loadResult.Error.Error(), tc.error)
			}

			if tc.warning != "" {
				require.Len(t, f.warnings, 1)
				require.Contains(t, f.warnings[0], tc.warning)
			} else {
				require.Empty(t, f.warnings, "Tiltfile load result had unexpected warning(s)")
			}
		})
	}
}

func (f *fixture) assertDcManifest(name model.ManifestName, opts ...interface{}) model.Manifest {
	f.t.Helper()
	m := f.assertNextManifest(name)

	if !m.IsDC() {
		f.t.Error("expected a docker-compose manifest")
	}
	dcInfo := m.DockerComposeTarget()

	for _, opt := range opts {
		switch opt := opt.(type) {
		case dcServiceYAMLHelper:
			assert.YAMLEq(f.t, opt.yaml, dcInfo.ServiceYAML, "docker compose YAML")
		case noImageHelper:
			assert.Empty(f.t, m.ImageTargets, "Manifest should have had no ImageTargets")
		case dockerComposeImageHelper:
			ok, iTarget := assertImageTargetType(f.t, m.ImageTargets, model.DockerComposeBuild{})
			if ok {
				assert.Equal(f.t, opt.buildContext, iTarget.DockerComposeBuildInfo().Context,
					"Build context path did not match")
			}
		case dcPublishedPortsHelper:
			assert.Equal(f.t, opt.ports, dcInfo.PublishedPorts(), "docker compose published ports")
		default:
			f.t.Fatalf("unexpected arg to assertDcManifest: %T %v", opt, opt)
		}
	}
	return m
}

func assertImageTargetType(t *testing.T, iTargets []model.ImageTarget,
	buildDetailsType interface{}) (bool, model.ImageTarget) {
	t.Helper()
	if !assert.Len(t, iTargets, 1, "Manifest should have exactly one image target") {
		return false, model.ImageTarget{}
	}
	if !assert.IsType(t, buildDetailsType, iTargets[0].BuildDetails, "BuildDetails was not of expected type") {
		return false, model.ImageTarget{}
	}
	return true, iTargets[0]
}

type dcServiceYAMLHelper struct {
	yaml string
}

func dcServiceYAML(yaml string) dcServiceYAMLHelper {
	return dcServiceYAMLHelper{yaml}
}

type dockerComposeImageHelper struct {
	dfPath       string
	buildContext string
}

func dockerComposeManagedImage(dfPath string, buildContext string) dockerComposeImageHelper {
	return dockerComposeImageHelper{
		dfPath:       dfPath,
		buildContext: buildContext,
	}
}

type noImageHelper struct{}

func noImage() noImageHelper {
	return noImageHelper{}
}

type dcPublishedPortsHelper struct {
	ports []int
}

func dcPublishedPorts(ports ...int) dcPublishedPortsHelper {
	return dcPublishedPortsHelper{ports: ports}
}

// wtPortRangeFor reserves a narrow registry range for a test so fallback
// allocations are deterministic and assertions can check range membership;
// released on cleanup so parallel tests never fight over the range.
func wtPortRangeFor(t *testing.T, owner string) (int, int) {
	t.Helper()
	base := int(wtRangeCounter.Add(1)) * 100
	min, max := 20000+base, 20000+base+99
	t.Cleanup(func() {
		portregistry.SetPortRange(0, 0)
		for p := min; p <= max; p++ {
			portregistry.Release(fmt.Sprintf("%s-%d", owner, p))
		}
	})
	portregistry.SetPortRange(min, max)
	return min, max
}

var wtRangeCounter atomic.Int64

// releaseLRPorts frees the local_resource serve-port allocations a test
// made (owner keys lr:<worktree>/<resource>, resource "web" here) so the
// process-wide registry never leaks a sticky allocation into a later test.
func releaseLRPorts(t *testing.T, worktrees ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, wt := range worktrees {
			portregistry.Release(wtLocalResourcePortOwner(wt, "web"))
		}
	})
}

// wtSetupWorktreeCheckout mirrors a real worktree run's layout: the root
// Tiltfile stays at the repo root, and the worktree checkout (`.worktree/
// <name>/`) holds the files the run re-roots path resolution at (plan §0:
// docker_compose paths resolve against the worktree checkout). tiltfileBody
// is the root Tiltfile content (the same file the engine re-executes per
// worktree).
func wtSetupWorktreeCheckout(f *fixture, tiltfileBody string) {
	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file(filepath.Join(worktree.DefaultDir, "feat-auth", "docker-compose.yml"), simpleConfig)
	f.file(filepath.Join(worktree.DefaultDir, "fix-bug", "docker-compose.yml"), simpleConfig)
	f.file(filepath.Join(worktree.DefaultDir, "feat-auth", "foo", "Dockerfile"), simpleDockerfile)
	f.file(filepath.Join(worktree.DefaultDir, "fix-bug", "foo", "Dockerfile"), simpleDockerfile)
	f.file("Tiltfile", tiltfileBody)
}

// Acceptance (tk-ljk, plan §4.5): a worktree run's compose project name is
// suffixed per worktree — both the dir-derived default and an explicit
// project_name= — so two worktrees compose up as distinct projects.
func TestDockerComposeProjectName_WorktreeRun(t *testing.T) {
	for _, tc := range []struct {
		name           string
		tiltfile       string
		expectedSuffix string
	}{
		{"dir-derived default", `docker_compose('docker-compose.yml')`, "feat-auth-wt-feat-auth"},
		{"explicit project_name", `docker_compose('docker-compose.yml', project_name='hello')`, "hello-wt-feat-auth"},
		{"distinct authored names", `docker_compose('docker-compose.yml', project_name='hello2')`, "hello2-wt-feat-auth"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			wtSetupWorktreeCheckout(f, tc.tiltfile)

			tf := ctrltiltfile.WorktreeTiltfile("feat-auth", f.JoinPath("Tiltfile"), nil)
			tlr := f.newTiltfileLoader().Load(f.ctx, tf, nil)
			require.NoError(t, tlr.Error)
			f.loadResult = tlr

			m := f.assertDcManifest("foo")
			require.Equal(t, tc.expectedSuffix, m.DockerComposeTarget().Spec.Project.Name)
		})
	}
}

// Main runs keep the authored project name: no suffix, classic behavior.
func TestDockerComposeProjectName_MainRunUnchanged(t *testing.T) {
	f := newFixture(t)

	f.dockerfile(filepath.Join("foo", "Dockerfile"))
	f.file("docker-compose.yml", simpleConfig)
	f.file("Tiltfile", `docker_compose('docker-compose.yml', project_name='hello')`)

	f.load()
	m := f.assertDcManifest("foo")
	require.Equal(t, "hello", m.DockerComposeTarget().Spec.Project.Name)
}

// dc_resource(project_name=) is authored against the Tiltfile's name; in a
// worktree run the engine sees the suffixed one, and the builtin translates
// back so options still attach (here: renaming the service).
func TestDockerComposeDCResourceAuthoredProjectName_WorktreeRun(t *testing.T) {
	f := newFixture(t)
	wtSetupWorktreeCheckout(f, `
docker_compose('docker-compose.yml', project_name='hello')
dc_resource('foo', project_name='hello', new_name='foo2')
`)

	tf := ctrltiltfile.WorktreeTiltfile("feat-auth", f.JoinPath("Tiltfile"), nil)
	tlr := f.newTiltfileLoader().Load(f.ctx, tf, nil)
	require.NoError(t, tlr.Error)
	f.loadResult = tlr

	f.assertDcManifest("foo2")
}

// Acceptance (tk-ljk, plan §4.5): a worktree run's published host ports are
// rebound through the port registry — distinct from the authored binding,
// inside the configured range, and carried on the manifest target (what the
// UI links against) as well as the runtime config file (what compose up
// binds). Main runs are untouched.
func TestDockerComposeWorktreePortDeconflict(t *testing.T) {
	for _, tc := range []struct {
		name     string
		worktree string
	}{
		{"main run keeps authored ports", ""},
		{"worktree run rebinds", "feat-auth"},
		{"second worktree rebinds again", "fix-bug"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			min, max := wtPortRangeFor(t, "dcpd")

			f := newFixture(t)
			wtSetupWorktreeCheckout(f, `docker_compose('docker-compose.yml', project_name='dcpd-hello')`)
			f.file("docker-compose.yml", simpleConfig) // main-run case reads the root copy

			tf := ctrltiltfile.MainTiltfile(f.JoinPath("Tiltfile"), nil)
			if tc.worktree != "" {
				tf = ctrltiltfile.WorktreeTiltfile(tc.worktree, f.JoinPath("Tiltfile"), nil)
			}
			tlr := f.newTiltfileLoader().Load(f.ctx, tf, nil)
			require.NoError(t, tlr.Error)
			f.loadResult = tlr

			m := f.assertDcManifest("foo")
			ports := m.DockerComposeTarget().PublishedPorts()
			require.Len(t, ports, 1)

			if tc.worktree == "" {
				require.Equal(t, []int{12312}, ports)
				return
			}

			require.NotEqual(t, 12312, ports[0], "worktree must not hold the authored port")
			require.GreaterOrEqual(t, ports[0], min)
			require.LessOrEqual(t, ports[0], max)

			// The rewritten runtime config must carry the same binding —
			// this is the file `compose up` actually consumes.
			proj := m.DockerComposeTarget().Spec.Project
			require.Len(t, proj.ConfigPaths, 1)
			require.NotEqual(t, f.JoinPath("docker-compose.yml"), proj.ConfigPaths[0])
			runtimeYAML, err := os.ReadFile(proj.ConfigPaths[0])
			require.NoError(t, err)
			require.Contains(t, string(runtimeYAML), fmt.Sprintf("published: \"%d\"", ports[0]))
			require.NotContains(t, string(runtimeYAML), "published: \"12312\"")
		})
	}
}

// The acceptance test: two worktrees compose up simultaneously — distinct
// projects, distinct (registry-allocated) host ports for the same authored
// binding, no clashes.
func TestDockerComposeTwoWorktreesNoPortClash(t *testing.T) {
	wtPortRangeFor(t, "twowt")

	load := func(t *testing.T, f *fixture, worktree string) model.Manifest {
		t.Helper()
		tf := ctrltiltfile.WorktreeTiltfile(worktree, f.JoinPath("Tiltfile"), nil)
		tlr := f.newTiltfileLoader().Load(f.ctx, tf, nil)
		require.NoError(t, tlr.Error)
		return tlr.Manifests[0]
	}

	newWtFixture := func(t *testing.T) *fixture {
		f := newFixture(t)
		wtSetupWorktreeCheckout(f, `docker_compose('docker-compose.yml')`)
		return f
	}

	f1 := newWtFixture(t)
	m1 := load(t, f1, "feat-auth")
	f2 := newWtFixture(t)
	m2 := load(t, f2, "fix-bug")

	require.Equal(t, "feat-auth-wt-feat-auth", m1.DockerComposeTarget().Spec.Project.Name)
	require.Equal(t, "fix-bug-wt-fix-bug", m2.DockerComposeTarget().Spec.Project.Name)
	require.NotEqual(t, m1.DockerComposeTarget().Spec.Project.Name,
		m2.DockerComposeTarget().Spec.Project.Name)

	p1 := m1.DockerComposeTarget().PublishedPorts()
	p2 := m2.DockerComposeTarget().PublishedPorts()
	require.Len(t, p1, 1)
	require.Len(t, p2, 1)
	require.NotEqual(t, p1[0], p2[0], "two worktrees must not hold the same host port")
	require.NotEqual(t, p1[0], 12312)
	require.NotEqual(t, p2[0], 12312)
}

// Registry allocations are stable across Tiltfile reloads: re-declaring the
// same project/service/binding hands back the same port (contract from
// plan §5, shared with tk-zfi's portforward wiring).
func TestDockerComposeWorktreePortStableAcrossReloads(t *testing.T) {
	wtPortRangeFor(t, "reload")

	f := newFixture(t)
	wtSetupWorktreeCheckout(f, `docker_compose('docker-compose.yml')`)

	tf := ctrltiltfile.WorktreeTiltfile("feat-auth", f.JoinPath("Tiltfile"), nil)

	first := f.newTiltfileLoader().Load(f.ctx, tf, nil)
	require.NoError(t, first.Error)
	second := f.newTiltfileLoader().Load(f.ctx, tf, &first)
	require.NoError(t, second.Error)

	require.Equal(t,
		first.Manifests[0].DockerComposeTarget().PublishedPorts(),
		second.Manifests[0].DockerComposeTarget().PublishedPorts())
}

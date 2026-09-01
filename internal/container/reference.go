package container

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/distribution/reference"
	"github.com/pkg/errors"

	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
)

// RefSet describes the references for a given image:
//  1. ConfigurationRef: ref as specified in the Tiltfile
//  2. LocalRef(): ref as used outside of the cluster (for Docker etc.)
type RefSet struct {
	// Ref as specified in Tiltfile; used to match a DockerBuild with
	// corresponding k8s YAML. May contain tags, etc. (Also used as
	// user-facing name for this image.)
	ConfigurationRef RefSelector

	// (Optional) registry to prepend to ConfigurationRef to yield ref to use in update and deploy
	registry *v1alpha1.RegistryHosting

	// (Optional) worktree this run executes for. When set, AddTagSuffix appends
	// a `-wt-<worktree>` token to the composed tag so builds from different
	// worktrees never collide (or reuse each other's images). The token is
	// escaped into a valid docker tag by WorktreeTagSuffix.
	worktree string
}

// WithWorktree returns a copy of the RefSet bound to the given worktree, so
// image tags derived from it carry a per-worktree token (see AddTagSuffix).
func (rs RefSet) WithWorktree(name string) RefSet {
	rs.worktree = name
	return rs
}

// MustWithWorktree is like WithWorktree, panicking on an invalid name.
func (rs RefSet) MustWithWorktree(name string) RefSet {
	_, err := WorktreeTagSuffix(name)
	if err != nil {
		panic(err)
	}
	return rs.WithWorktree(name)
}

func NewRefSet(confRef RefSelector, reg *v1alpha1.RegistryHosting) (RefSet, error) {
	r := RefSet{
		ConfigurationRef: confRef,
		registry:         reg,
	}
	return r, r.Validate()
}

func MustSimpleRefSet(ref RefSelector) RefSet {
	r := RefSet{
		ConfigurationRef: ref,
	}
	if err := r.Validate(); err != nil {
		panic(err)
	}
	return r
}

func RefSetFromImageMap(spec v1alpha1.ImageMapSpec, cluster *v1alpha1.Cluster) (RefSet, error) {
	selector, err := SelectorFromImageMap(spec)
	if err != nil {
		return RefSet{}, fmt.Errorf("validating image: %v", err)
	}

	reg, err := RegistryFromCluster(cluster)
	if err != nil {
		return RefSet{}, fmt.Errorf("determining registry: %v", err)
	}

	refs, err := NewRefSet(selector, reg)
	if err != nil {
		return RefSet{}, fmt.Errorf("applying image %s to registry %s: %v", spec.Selector, reg, err)
	}
	return refs, nil
}

func (rs RefSet) WithoutRegistry() RefSet {
	out := MustSimpleRefSet(rs.ConfigurationRef)
	out.worktree = rs.worktree
	return out
}

func (rs RefSet) Registry() *v1alpha1.RegistryHosting {
	if rs.registry == nil {
		return nil
	}
	return rs.registry.DeepCopy()
}

func (rs RefSet) MustWithRegistry(reg *v1alpha1.RegistryHosting) RefSet {
	rs.registry = reg
	err := rs.Validate()
	if err != nil {
		panic(err)
	}
	return rs
}

func (rs RefSet) Validate() error {
	if rs.registry != nil {
		err := rs.registry.Validate(context.TODO())
		if err != nil {
			return errors.Wrapf(err.ToAggregate(), "validating new RefSet with configuration ref %q", rs.ConfigurationRef)
		}
	}
	_, err := ReplaceRegistryForLocalRef(rs.ConfigurationRef, rs.registry)
	if err != nil {
		return errors.Wrapf(err, "validating new RefSet with configuration ref %q", rs.ConfigurationRef)
	}

	_, err = ReplaceRegistryForContainerRuntimeRef(rs.ConfigurationRef, rs.registry)
	if err != nil {
		return errors.Wrapf(err, "validating new RefSet with configuration ref %q", rs.ConfigurationRef)
	}

	return nil
}

// LocalRef returns the ref by which this image is referenced from outside the cluster
// (e.g. by `docker build`, `docker push`, etc.)
func (rs RefSet) LocalRef() reference.Named {
	if IsEmptyRegistry(rs.registry) {
		return rs.ConfigurationRef.AsNamedOnly()
	}
	ref, err := ReplaceRegistryForLocalRef(rs.ConfigurationRef, rs.registry)
	if err != nil {
		// Validation should have caught this before now :-/
		panic(fmt.Sprintf("ERROR deriving LocalRef: %v", err))
	}

	return ref
}

// ClusterRef returns the ref by which this image will be pulled by
// the container runtime in the cluster.
//
// For example, the registry host (that the user/Tilt *push* to) might be
// something like `localhost:1234/foo`, referring to an exposed port from the
// registry Docker container. However, when the container runtime (itself
// generally running within a Docker container), won't see it on localhost,
// and will instead use a reference like `registry:5000/foo`.
//
// If HostFromContainerRuntime is not set on the registry for the RefSet, the
// Host will be used instead. This is common in cases where both the user and
// the container runtime refer to the registry in the same way.
//
// Note that this is specific to the container runtime, which might have its
// own config for the host. The local registry specification allows an
// additional "ClusterFromClusterNetwork" value, which describes a generic way
// for access from within the cluster network (e.g. via cluster provided DNS).
// Within Tilt, this value is NOT used for business logic, so sometimes "cluster
// ref" is used to refer to the container runtime ref. The API types, however,
// include both values and are labeled accurately.
//
// TODO(milas): Rename to ContainerRuntimeRef()
func (rs RefSet) ClusterRef() reference.Named {
	if IsEmptyRegistry(rs.registry) {
		return rs.LocalRef()
	}
	ref, err := ReplaceRegistryForContainerRuntimeRef(rs.ConfigurationRef, rs.registry)
	if err != nil {
		// Validation should have caught this before now :-/
		panic(fmt.Sprintf("ERROR deriving ClusterRef: %v", err))
	}
	return ref
}

// AddTagSuffix tags the references for build/deploy.
//
// In most cases, we will use the tag given as-is.
//
// If we're in the mode where we're pushing to a single image name (for ECR), we'll
// tag it with [escaped-original-name]-[suffix].
//
// When the RefSet carries worktree context (WithWorktree), a `-wt-<worktree>`
// token is appended to the composed suffix, so builds from different worktrees
// of the same image yield distinct tags (distinct ImageMaps, no cross-worktree
// image reuse). The main run (no worktree context) is unchanged.
func (rs RefSet) AddTagSuffix(suffix string) (TaggedRefs, error) {
	tag := suffix
	if rs.registry != nil && rs.registry.SingleName != "" {
		tag = fmt.Sprintf("%s-%s", escapeName(path.Base(rs.ConfigurationRef.RefFamiliarName())), tag)
	}
	if rs.worktree != "" {
		token, err := WorktreeTagSuffix(rs.worktree)
		if err != nil {
			return TaggedRefs{}, err
		}
		tag += token
	}

	localTagged, err := reference.WithTag(rs.LocalRef(), tag)
	if err != nil {
		return TaggedRefs{}, errors.Wrapf(err, "tagging localRef %s as %s", rs.LocalRef().String(), tag)
	}

	// TODO(maia): maybe TaggedRef should behave like RefSet, where clusterRef is optional
	//   and if not set, the accessor returns LocalRef instead
	clusterTagged, err := reference.WithTag(rs.ClusterRef(), tag)
	if err != nil {
		return TaggedRefs{}, errors.Wrapf(err, "tagging clusterRef %s as %s", rs.ClusterRef().String(), tag)
	}
	return TaggedRefs{
		LocalRef:   localTagged,
		ClusterRef: clusterTagged,
	}, nil
}

// TaggedRefs yielded by an image build
type TaggedRefs struct {
	// LocalRef is the image name + tag as referenced from outside cluster
	// (e.g. by the user or Tilt when pushing images).
	LocalRef reference.NamedTagged
	// ClusterRef is the image name + tag as referenced from the
	// container runtime on the cluster.
	//
	// TODO(milas): Rename to ContainerRuntimeRef
	ClusterRef reference.NamedTagged
}

// worktreeTagToken is the tag-suffix token marking an image built for a
// worktree run: `-wt-<escaped worktree name>`.
const worktreeTagToken = "-wt-"

// worktreeTagInvalidChars matches characters that can appear in a worktree
// name (a directory basename) but are invalid in a docker tag
// ([\w][\w.-]{0,127}). Anything matched is replaced with '_'.
var worktreeTagInvalidChars = regexp.MustCompile(`[^\w.-]`)

// WorktreeTagSuffix returns the tag suffix for a worktree run:
// `-wt-<escaped name>`.
//
// Worktree names come from directory basenames and may contain characters
// that are invalid in docker tags (spaces, '+', non-ASCII, leading dots...),
// so the name is escaped before it is appended. The result is always a valid
// tag fragment: the composite tag built from it round-trips through
// reference.WithTag.
func WorktreeTagSuffix(name string) (string, error) {
	if name == "" {
		return "", errors.New("worktree name is empty")
	}
	escaped := worktreeTagInvalidChars.ReplaceAllString(name, "_")
	if escaped == "" {
		return "", fmt.Errorf("worktree name %q escapes to an empty tag token", name)
	}
	// A tag must start with [\w]; a leading '.' or '-' in the name would
	// surface here once the name escapes to e.g. "-tmp" or ".tmp".
	escaped = strings.TrimLeft(escaped, "-.")
	if escaped == "" {
		return "", fmt.Errorf("worktree name %q escapes to an empty tag token", name)
	}
	return worktreeTagToken + escaped, nil
}

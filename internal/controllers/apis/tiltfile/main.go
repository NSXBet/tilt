package tiltfile

import (
	"fmt"
	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tilt-dev/tilt/internal/controllers/apis/uibutton"
	"github.com/tilt-dev/tilt/internal/ospath"
	"github.com/tilt-dev/tilt/pkg/apis"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/model"
)

// Resolve a filename to its "best" version.
// On any error, just return the original filename.
func ResolveFilename(filename string) string {
	resolved, err := ospath.RealAbs(filename)
	if err == nil {
		return resolved
	}

	resolved, err = filepath.Abs(filename)
	if err == nil {
		return resolved
	}

	return filename
}

func MainTiltfile(filename string, args []string) *v1alpha1.Tiltfile {
	name := model.MainTiltfileManifestName.String()
	fwName := apis.SanitizeName(fmt.Sprintf("%s:%s", model.TargetTypeConfigs, name))
	return &v1alpha1.Tiltfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.TiltfileSpec{
			Path: ResolveFilename(filename),
			Args: args,
			RestartOn: &v1alpha1.RestartOnSpec{
				FileWatches: []string{fwName},
			},
			StopOn: &v1alpha1.StopOnSpec{
				UIButtons: []string{uibutton.StopBuildButtonName(name)},
			},
		},
	}
}

// WorktreeTiltfile builds the Tiltfile CR for one worktree run (plan §2/§3):
// it re-executes the SAME root Tiltfile (Spec.Path = the root path), and the
// tilt.dev/worktree label tells the loader to inject the worktree context
// and re-root path resolution at the worktree checkout. The engine-internal
// name is "tiltfile:<worktree>"; the FileWatch and stop button follow it, so
// each worktree run reloads and stops independently of the main run.
func WorktreeTiltfile(worktree, rootTiltfilePath string, args []string) *v1alpha1.Tiltfile {
	name := TiltfileName(worktree)
	fwName := apis.SanitizeName(fmt.Sprintf("%s:%s", model.TargetTypeConfigs, name))
	return &v1alpha1.Tiltfile{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{v1alpha1.LabelWorktree: worktree},
		},
		Spec: v1alpha1.TiltfileSpec{
			Path: ResolveFilename(rootTiltfilePath),
			Args: args,
			RestartOn: &v1alpha1.RestartOnSpec{
				FileWatches: []string{fwName},
			},
			StopOn: &v1alpha1.StopOnSpec{
				UIButtons: []string{uibutton.StopBuildButtonName(name)},
			},
		},
	}
}

// TiltfileName is the CR name for a worktree's Tiltfile: "tiltfile:<worktree>".
func TiltfileName(worktree string) string {
	return fmt.Sprintf("tiltfile:%s", worktree)
}

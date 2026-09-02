package tiltfiles

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tilt-dev/tilt/internal/controllers/apicmp"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/model"
)

// SetTiltfileArgs updates the args on the main Tiltfile and on every worktree
// re-execution of it (plan §7.7): worktree runs execute the same root Tiltfile,
// so they must track the main run's args, or they keep reloading on stale args.
// Extension Tiltfiles carry their own args and are not touched (they never
// carry the tilt.dev/worktree label).
func SetTiltfileArgs(ctx context.Context, client ctrlclient.Client, args []string) error {
	nn := types.NamespacedName{Name: model.MainTiltfileManifestName.String()}
	var tf v1alpha1.Tiltfile
	err := client.Get(ctx, nn, &tf)
	if err != nil {
		return err
	}

	if apicmp.DeepEqual(tf.Spec.Args, args) {
		return nil
	}

	update := tf.DeepCopy()
	update.Spec.Args = args
	err = client.Update(ctx, update)
	if err != nil {
		return err
	}

	var list v1alpha1.TiltfileList
	err = client.List(ctx, &list, ctrlclient.HasLabels{v1alpha1.LabelWorktree})
	for i := range list.Items {
		wt := &list.Items[i]
		if apicmp.DeepEqual(wt.Spec.Args, args) {
			continue
		}
		update := wt.DeepCopy()
		update.Spec.Args = args
		err = client.Update(ctx, update)
		if err != nil {
			return err
		}
	}

	return nil
}

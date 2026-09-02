package tiltfiles

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/tilt-dev/tilt/internal/controllers/fake"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/model"
)

func TestSetTiltfileArgs(t *testing.T) {
	ctx := context.Background()
	client := fake.NewFakeTiltClient()

	mainTf := &v1alpha1.Tiltfile{
		ObjectMeta: metav1.ObjectMeta{Name: model.MainTiltfileManifestName.String()},
		Spec:       v1alpha1.TiltfileSpec{Args: []string{"--foo", "bar"}},
	}
	require.NoError(t, client.Create(ctx, mainTf))

	wtTf := &v1alpha1.Tiltfile{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "tiltfile:feat-auth",
			Labels: map[string]string{v1alpha1.LabelWorktree: "feat-auth"},
		},
		Spec: v1alpha1.TiltfileSpec{Args: []string{"--foo", "bar"}},
	}
	require.NoError(t, client.Create(ctx, wtTf))

	// An extension Tiltfile keeps its own args.
	extTf := &v1alpha1.Tiltfile{
		ObjectMeta: metav1.ObjectMeta{Name: "my-ext"},
		Spec:       v1alpha1.TiltfileSpec{Args: []string{"--ext"}},
	}
	require.NoError(t, client.Create(ctx, extTf))

	err := SetTiltfileArgs(ctx, client, []string{"--foo", "baz"})
	require.NoError(t, err)

	var updated v1alpha1.Tiltfile
	require.NoError(t, client.Get(ctx, types.NamespacedName{Name: model.MainTiltfileManifestName.String()}, &updated))
	assert.Equal(t, []string{"--foo", "baz"}, updated.Spec.Args)

	require.NoError(t, client.Get(ctx, types.NamespacedName{Name: "tiltfile:feat-auth"}, &updated))
	assert.Equal(t, []string{"--foo", "baz"}, updated.Spec.Args)

	require.NoError(t, client.Get(ctx, types.NamespacedName{Name: "my-ext"}, &updated))
	assert.Equal(t, []string{"--ext"}, updated.Spec.Args)
}

package ipamclaimswebhook

import (
	"context"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	k8swait "k8s.io/apimachinery/pkg/util/wait"
	k8sretry "k8s.io/client-go/util/retry"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"
)

const defaultClientTimeout = 2 * time.Second
const defaultClientInterval = 500 * time.Millisecond

// getAndRetryOnNotFound an object according to 'key' using controller runtime client.
// When the object not found it retries until found or timeout.
// This function come in handy for fetching VMIs, when VMI and associated pod
// exist but the VMI is missing from the controller-runtime client informer cache.
func getAndRetryOnNotFound(
	ctx context.Context,
	cli crclient.Client,
	key crclient.ObjectKey,
	outObj crclient.Object,
) error {
	log := crlog.FromContext(ctx)
	gvk := outObj.GetObjectKind().GroupVersionKind().String()
	b := k8swait.Backoff{
		Cap:      defaultClientTimeout,
		Duration: defaultClientInterval,
		Steps:    4,
		Factor:   1.0,
		Jitter:   0.1,
	}
	return k8sretry.OnError(b,
		func(err error) bool {
			if !k8serrors.IsNotFound(err) {
				return false
			}
			log.Info("WARNING: Object not found:", "key", key, "kind", gvk, "err", err)
			return true
		},
		func() error {
			return cli.Get(ctx, key, outObj)
		},
	)
}

package ipamclaimswebhook

import (
	"context"
	"fmt"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const defaultAPIReadInterval = time.Second
const defaultAPIReadTimeout = time.Second * 3

// Get an object according to 'key' using controller runtime client.
// In case the object is not found, query the cluster API directly (using the
// controlle-runtime API-reader client).
// To optimize handling time, when VMI not found, it immediately tries to fetch
// the object from the cluster API and then start the polling.
// This function come in handy for fetching VMIs, when VMI and associated pod
// exist but the VMI is missing from the controller-runtime client informer cache.
func Get(
	ctx context.Context,
	cli client.Client,
	apiReader client.Reader,
	key client.ObjectKey,
	outObj client.Object,
) error {
	err := cli.Get(ctx, key, outObj)
	if err == nil {
		return nil
	}

	if !k8serrors.IsNotFound(err) {
		return fmt.Errorf("failed due unexpected err: %w", err)
	}

	apiReaderCtx, cancel := context.WithTimeout(ctx, defaultAPIReadTimeout)
	defer cancel()
	var gerr error
	err = wait.PollUntilContextCancel(apiReaderCtx, defaultAPIReadInterval, true, func(context.Context) (bool, error) {
		if gerr = apiReader.Get(ctx, key, outObj); gerr != nil {
			return false, nil // obj not found, keep trying
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("%v: %w", err, gerr)
	}
	return nil
}

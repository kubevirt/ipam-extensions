package claims

import (
	"context"
	"fmt"

	apitypes "k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	ipamclaimsapi "github.com/k8snetworkplumbingwg/ipamclaims/pkg/crd/ipamclaims/v1alpha1"

	virtv1 "kubevirt.io/api/core/v1"
)

const KubevirtVMFinalizer = "kubevirt.io/persistent-ipam"

func Cleanup(c client.Client, vmiKey apitypes.NamespacedName) error {
	ipamClaims := &ipamclaimsapi.IPAMClaimList{}
	listOpts := []client.ListOption{
		client.InNamespace(vmiKey.Namespace),
		OwnedByVMLabel(vmiKey.Name),
	}
	if err := c.List(context.Background(), ipamClaims, listOpts...); err != nil {
		return fmt.Errorf("could not get list of IPAMClaims owned by VM %q: %w", vmiKey.String(), err)
	}

	for _, claim := range ipamClaims.Items {
		if controllerutil.RemoveFinalizer(&claim, KubevirtVMFinalizer) {
			if err := c.Update(context.Background(), &claim, &client.UpdateOptions{}); err != nil {
				return client.IgnoreNotFound(err)
			}
		}
	}
	return nil
}

func OwnedByVMLabel(vmiName string) client.MatchingLabels {
	return map[string]string{
		virtv1.VirtualMachineLabel: vmiName,
	}
}

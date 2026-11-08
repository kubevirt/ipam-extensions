package claimcontroller

import (
	"context"

	controllerruntime "sigs.k8s.io/controller-runtime"

	ipamclaimsapi "github.com/k8snetworkplumbingwg/ipamclaims/pkg/crd/ipamclaims/v1alpha1"
)

// IPAMClaimReconciler releases orphaned terminating IPAMClaims.
type IPAMClaimReconciler struct {
	manager controllerruntime.Manager
}

func NewClaimReconciler(manager controllerruntime.Manager) *IPAMClaimReconciler {
	return &IPAMClaimReconciler{manager: manager}
}

func (r *IPAMClaimReconciler) Reconcile(
	ctx context.Context,
	request controllerruntime.Request,
) (controllerruntime.Result, error) {
	controllerruntime.LoggerFrom(ctx).Info("Observed IPAMClaim", "claim", request.NamespacedName)
	return controllerruntime.Result{}, releaseOrphanClaim(
		ctx,
		r.manager.GetClient(),
		request.NamespacedName,
	)
}

func (r *IPAMClaimReconciler) Setup() error {
	return controllerruntime.NewControllerManagedBy(r.manager).
		Named("claimcontroller").
		For(&ipamclaimsapi.IPAMClaim{}).
		Complete(r)
}

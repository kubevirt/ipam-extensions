package claimcontroller

import (
	"context"
	"slices"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	ipamclaimsapi "github.com/k8snetworkplumbingwg/ipamclaims/pkg/crd/ipamclaims/v1alpha1"

	"github.com/kubevirt/ipam-extensions/pkg/claims"
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
		WithEventFilter(terminatingClaimFilter()).
		Complete(r)
}

// terminatingClaimFilter returns controller-runtime predicates for event filtering,
// accepting create events for terminating VM-owned IPAMClaims only.
// When the controller-runtime manager starts, it enqueues existing objects emitting
// synthetic create events, enabling the controller reconciles them when stated.
func terminatingClaimFilter() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			if c, ok := e.Object.(*ipamclaimsapi.IPAMClaim); ok && c != nil {
				return !c.DeletionTimestamp.IsZero() &&
					slices.Contains(c.Finalizers, claims.KubevirtVMFinalizer) &&
					GetOwnerRefForVMResource(c.OwnerReferences) != nil
			}
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return false
		},
		GenericFunc: func(event.GenericEvent) bool {
			return false
		},
	}
}

package claimcontroller

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	ipamclaimsapi "github.com/k8snetworkplumbingwg/ipamclaims/pkg/crd/ipamclaims/v1alpha1"
	"github.com/kubevirt/ipam-extensions/pkg/claims"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	virtv1 "kubevirt.io/api/core/v1"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// IPAMClaimReconciler release orphan claims
type IPAMClaimReconciler struct {
	ctl    client.Client
	logger logr.Logger
}

func NewClaimReconciler(ctl client.Client) *IPAMClaimReconciler {
	return &IPAMClaimReconciler{
		ctl:    ctl,
		logger: controllerruntime.Log.WithName("controllers").WithName("IPAMClaims"),
	}
}

func (r *IPAMClaimReconciler) ReconcileAll(ctx context.Context) error {
	claimList := &ipamclaimsapi.IPAMClaimList{}
	if err := r.ctl.List(ctx, claimList); err != nil {
		return fmt.Errorf("failed to list IPAMClaims: %w", err)
	}

	var terminatingClaims []ipamclaimsapi.IPAMClaim
	for _, claim := range claimList.Items {
		if !claim.ObjectMeta.DeletionTimestamp.IsZero() &&
			controllerutil.ContainsFinalizer(&claim, claims.KubevirtVMFinalizer) {
			terminatingClaims = append(terminatingClaims, claim)
		}
	}

	kvGV := virtv1.GroupVersion.String()
	vmiKind := virtv1.VirtualMachineGroupVersionKind.Kind
	vmKind := virtv1.VirtualMachineInstanceGroupVersionKind.Kind
	var vmClaims []ipamclaimsapi.IPAMClaim
	for _, terminationClaim := range terminatingClaims {
		for _, owner := range terminationClaim.GetOwnerReferences() {
			if owner.APIVersion == kvGV &&
				(owner.Kind == vmiKind || owner.Kind == vmKind) {
				vmClaims = append(vmClaims, terminationClaim)
			}
		}
	}

	var errs []error
	var orphanClaims []ipamclaimsapi.IPAMClaim
	for _, vmClaim := range vmClaims {
		for _, owner := range vmClaim.GetOwnerReferences() {
			ownerKey := types.NamespacedName{Namespace: vmClaim.Namespace, Name: owner.Name}
			// TODO: cover VMI objects
			vm := &virtv1.VirtualMachine{}
			if err := r.ctl.Get(ctx, ownerKey, vm); err != nil {
				if !k8serr.IsNotFound(err) {
					errs = append(errs, err)
				} else {
					orphanClaims = append(orphanClaims, vmClaim)
				}
			}
		}
	}
	for _, orphanClaim := range orphanClaims {
		cp := orphanClaim.DeepCopy()
		controllerutil.RemoveFinalizer(cp, claims.KubevirtVMFinalizer)
		// TODO: switch to patch
		if err := r.ctl.Update(ctx, cp); err != nil {
			if !k8serr.IsNotFound(err) {
				errs = append(errs, fmt.Errorf("failed to update claim [%s/%s] following filaizer [%s] removal: %w",
					orphanClaim.Namespace, orphanClaim.Name, claims.KubevirtVMFinalizer, err))
			}
		}
		r.logger.Info("Removed finalizer from orphan claim",
			"claim", orphanClaim.Name, "namespace", orphanClaim.Namespace)
	}

	return errors.Join(errs...)
}

package claimcontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ipamclaimsapi "github.com/k8snetworkplumbingwg/ipamclaims/pkg/crd/ipamclaims/v1alpha1"
	virtv1 "kubevirt.io/api/core/v1"

	"github.com/kubevirt/ipam-extensions/pkg/claims"
)

// releaseOrphanClaim release claim of the given key when its on terminating state and owned by VM resource (VM / VMI)
// that is no longer exist.
// Release is performed by removing the VM finalizer from the claim.
func releaseOrphanClaim(ctx context.Context, ctl client.Client, key client.ObjectKey) error {
	logger := controllerruntime.Log.WithName("ReleaseOrphanClaim")

	claim := &ipamclaimsapi.IPAMClaim{}
	if err := ctl.Get(ctx, key, claim); err != nil {
		return client.IgnoreNotFound(fmt.Errorf("failed to get claim [%s]: %w", key.String(), err))
	}

	if claim.DeletionTimestamp.IsZero() ||
		!slices.Contains(claim.Finalizers, claims.KubevirtVMFinalizer) {
		return nil // claim is not terminating
	}

	ownerRef := GetOwnerRefForVMResource(claim.OwnerReferences)
	if ownerRef == nil { // not owned by a VM reference
		return nil
	}

	shouldRelease := false
	ownerKey := client.ObjectKey{Namespace: claim.Namespace, Name: ownerRef.Name}
	ownerObj := newVMResource(ownerRef.Kind)
	if err := ctl.Get(ctx, ownerKey, ownerObj); err != nil {
		if !k8serr.IsNotFound(err) {
			return fmt.Errorf("failed to get owner [%s %s]: %w", ownerRef.Kind, ownerKey.String(), err)
		}
		// owner not found
		shouldRelease = true
	}

	if ownerRef.UID != ownerObj.GetUID() {
		// claim has owner reference to a VM who has been deleted and created
		// skipping the controller reconciliation. release the claim to enable
		// the controller reconcile the state.
		shouldRelease = true
	}

	if !shouldRelease {
		// owner not exist, or exist and aligned with the owner reference
		return nil
	}

	if err := releaseClaim(ctx, ctl, *claim); err != nil {
		logger.Error(err, "Failed to remove finalizer for claim", "claim", claim.Name, "namespace", claim.Namespace)
		return fmt.Errorf("failed to release claim %s/%s: %w", claim.Namespace, claim.Name, err)
	}
	logger.Info("Removed finalizer for claim", "claim", claim.Name, "namespace", claim.Namespace)

	return nil
}

// GetOwnerRefForVMResource return owner reference for VM resource, first occurrence found.
func GetOwnerRefForVMResource(ownerReferences []metav1.OwnerReference) *metav1.OwnerReference {
	i := slices.IndexFunc(ownerReferences, func(ref metav1.OwnerReference) bool {
		return ref.APIVersion == virtv1.GroupVersion.String() &&
			(ref.Kind == virtv1.VirtualMachineGroupVersionKind.Kind ||
				ref.Kind == virtv1.VirtualMachineInstanceGroupVersionKind.Kind)
	})
	if i == -1 {
		return nil
	}
	r := ownerReferences[i]
	return &r
}

// newVMResource returns a new client object for a supported VM or VMI kind, or nil for unknown kinds.
// Serves as an adapter between controller-runtime objects and KubeVirt concrete types
func newVMResource(kind string) client.Object {
	switch kind {
	case virtv1.VirtualMachineGroupVersionKind.Kind:
		return &virtv1.VirtualMachine{}
	case virtv1.VirtualMachineInstanceGroupVersionKind.Kind:
		return &virtv1.VirtualMachineInstance{}
	default:
		return nil
	}
}

// releaseClaim removes the ipam controller VM finalizer from the given claim.
func releaseClaim(ctx context.Context, ctl client.Client, claim ipamclaimsapi.IPAMClaim) error {
	patch, err := newRemoveFinalizerPatch(claim.ObjectMeta, claims.KubevirtVMFinalizer)
	if err != nil {
		return fmt.Errorf("failed to create JSON patch: %w", err)
	}

	if err := ctl.Patch(ctx, &claim, patch); err != nil && !k8serr.IsNotFound(err) {
		return err
	}

	return nil
}

func newRemoveFinalizerPatch(meta metav1.ObjectMeta, finalizer string) (client.Patch, error) {
	index := slices.Index(meta.Finalizers, finalizer)
	if index == -1 {
		return nil, nil
	}
	type operation struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value string `json:"value,omitempty"`
	}
	path := fmt.Sprintf("/metadata/finalizers/%d", index)
	operations := []operation{
		{Op: "test", Path: "/metadata/resourceVersion", Value: meta.ResourceVersion},
		{Op: "test", Path: path, Value: finalizer},
		{Op: "remove", Path: path},
	}
	jsonPatch, err := json.Marshal(operations)
	if err != nil {
		return nil, err
	}
	return client.RawPatch(types.JSONPatchType, jsonPatch), nil
}

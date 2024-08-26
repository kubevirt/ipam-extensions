package vminetworkscontroller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"

	"k8s.io/utils/ptr"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	ipamclaimsapi "github.com/k8snetworkplumbingwg/ipamclaims/pkg/crd/ipamclaims/v1alpha1"
	nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	virtv1 "kubevirt.io/api/core/v1"

	"github.com/kubevirt/ipam-extensions/pkg/claims"
	"github.com/kubevirt/ipam-extensions/pkg/config"
)

// VirtualMachineInstanceReconciler reconciles a VirtualMachineInstance object
type VirtualMachineInstanceReconciler struct {
	client.Client
	Log     logr.Logger
	Scheme  *runtime.Scheme
	manager controllerruntime.Manager
}

func NewVMIReconciler(manager controllerruntime.Manager) *VirtualMachineInstanceReconciler {
	return &VirtualMachineInstanceReconciler{
		Client:  manager.GetClient(),
		Log:     controllerruntime.Log.WithName("controllers").WithName("VirtualMachineInstance"),
		Scheme:  manager.GetScheme(),
		manager: manager,
	}
}

func (r *VirtualMachineInstanceReconciler) Reconcile(
	ctx context.Context,
	request controllerruntime.Request,
) (controllerruntime.Result, error) {
	vmi := &virtv1.VirtualMachineInstance{}

	contextWithTimeout, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	err := r.Client.Get(contextWithTimeout, request.NamespacedName, vmi)
	if apierrors.IsNotFound(err) {
		vmi = nil
	} else if err != nil {
		return controllerruntime.Result{}, err
	}

	vm, err := getOwningVM(ctx, r.Client, request.NamespacedName)
	if err != nil {
		return controllerruntime.Result{}, err
	}

	if shouldCleanFinalizers(vmi, vm) {
		if err := claims.Cleanup(r.Client, request.NamespacedName); err != nil {
			return controllerruntime.Result{}, fmt.Errorf("failed removing the IPAMClaims finalizer: %w", err)
		}
		return controllerruntime.Result{}, nil
	}

	if vmi == nil {
		return controllerruntime.Result{}, nil
	}

	vmiNetworks, err := r.vmiNetworksClaimingIPAM(ctx, vmi)
	if err != nil {
		return controllerruntime.Result{}, err
	}

	ownerInfo := ownerReferenceFor(vmi, vm)

	// UDN code block
	primaryUDN, err := r.vmiPrimaryUDN(ctx, vmi)
	if err != nil {
		return controllerruntime.Result{}, err
	}
	if primaryUDN != nil {
		claimKey := fmt.Sprintf("%s.%s-primary-udn", vmi.Name, primaryUDN.Name)
		udnIPAMClaim := newIPAMClaim(claimKey, vmi, ownerInfo, primaryUDN.Name)
		if err := r.ensureIPAMClaim(ctx, udnIPAMClaim, vmi, ownerInfo); err != nil {
			return controllerruntime.Result{}, fmt.Errorf(
				"failed ensuring IPAM claim for primary UDN network %q: %w",
				primaryUDN.Name,
				err,
			)
		}
	}
	// UDN code block END

	for logicalNetworkName, netConfigName := range vmiNetworks {
		claimKey := fmt.Sprintf("%s.%s", vmi.Name, logicalNetworkName)
		ipamClaim := newIPAMClaim(claimKey, vmi, ownerInfo, netConfigName)

		if err := r.ensureIPAMClaim(ctx, ipamClaim, vmi, ownerInfo); err != nil {
			return controllerruntime.Result{}, fmt.Errorf("failed ensuring IPAM claim: %w", err)
		}
	}

	return controllerruntime.Result{}, nil
}

// Setup sets up the controller with the Manager passed in the constructor.
func (r *VirtualMachineInstanceReconciler) Setup() error {
	return controllerruntime.NewControllerManagedBy(r.manager).
		For(&virtv1.VirtualMachineInstance{}).
		WithEventFilter(onVMIPredicates()).
		Complete(r)
}

func onVMIPredicates() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(createEvent event.CreateEvent) bool {
			return true
		},
		DeleteFunc: func(event.DeleteEvent) bool {
			return true
		},
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			return true
		},
		GenericFunc: func(event.GenericEvent) bool {
			return false
		},
	}
}

func (r *VirtualMachineInstanceReconciler) vmiNetworksClaimingIPAM(
	ctx context.Context,
	vmi *virtv1.VirtualMachineInstance,
) (map[string]string, error) {
	vmiNets := make(map[string]string)
	for _, net := range vmi.Spec.Networks {
		if net.Pod != nil {
			continue
		}

		if net.Multus != nil && !net.Multus.Default {
			nadName := net.Multus.NetworkName
			namespace := vmi.Namespace
			namespaceAndName := strings.Split(nadName, "/")
			if len(namespaceAndName) == 2 {
				namespace = namespaceAndName[0]
				nadName = namespaceAndName[1]
			}

			contextWithTimeout, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			nad := &nadv1.NetworkAttachmentDefinition{}
			if err := r.Client.Get(
				contextWithTimeout,
				apitypes.NamespacedName{Namespace: namespace, Name: nadName},
				nad,
			); err != nil {
				if apierrors.IsNotFound(err) {
					return nil, err
				}
			}

			nadConfig, err := config.NewConfig(nad.Spec.Config)
			if err != nil {
				r.Log.Error(err, "failed extracting the relevant NAD configuration", "NAD name", nadName)
				return nil, fmt.Errorf("failed to extract the relevant NAD information")
			}

			if nadConfig.AllowPersistentIPs {
				vmiNets[net.Name] = nadConfig.Name
			}
		}
	}
	return vmiNets, nil
}

func shouldCleanFinalizers(vmi *virtv1.VirtualMachineInstance, vm *virtv1.VirtualMachine) bool {
	if vm != nil {
		// VMI is gone and VM is marked for deletion
		return vmi == nil && vm.DeletionTimestamp != nil
	} else {
		// VMI is gone or VMI is marked for deletion, virt-launcher is gone
		return vmi == nil || (vmi.DeletionTimestamp != nil && len(vmi.Status.ActivePods) == 0)
	}
}

func ownerReferenceFor(vmi *virtv1.VirtualMachineInstance, vm *virtv1.VirtualMachine) metav1.OwnerReference {
	var obj client.Object
	if vm != nil {
		obj = vm
	} else {
		obj = vmi
	}

	aPIVersion := obj.GetObjectKind().GroupVersionKind().Group + "/" + obj.GetObjectKind().GroupVersionKind().Version
	return metav1.OwnerReference{
		APIVersion:         aPIVersion,
		Kind:               obj.GetObjectKind().GroupVersionKind().Kind,
		Name:               obj.GetName(),
		UID:                obj.GetUID(),
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}
}

// Gets the owning VM if any. for simplicity it just try to fetch the VM,
// even when the VMI exists, instead of parsing ownerReferences and handling differently the nil VMI case.
func getOwningVM(ctx context.Context, c client.Client, name apitypes.NamespacedName) (*virtv1.VirtualMachine, error) {
	contextWithTimeout, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	vm := &virtv1.VirtualMachine{}
	if err := c.Get(contextWithTimeout, name, vm); err == nil {
		return vm, nil
	} else if apierrors.IsNotFound(err) {
		return nil, nil
	} else {
		return nil, fmt.Errorf("failed getting VM %q: %w", name, err)
	}
}

func (r *VirtualMachineInstanceReconciler) vmiPrimaryUDN(
	ctx context.Context,
	vmi *virtv1.VirtualMachineInstance,
) (*config.RelevantConfig, error) {
	const (
		NetworkRolePrimary   = "primary"
		NetworkRoleSecondary = "secondary"
	)

	var namespaceNads nadv1.NetworkAttachmentDefinitionList
	if err := r.List(ctx, &namespaceNads, &client.ListOptions{}); err != nil {
		return nil, fmt.Errorf("failed to list the NADs on namespace %q: %v", vmi.Namespace, err)
	}

	for _, nad := range namespaceNads.Items {
		networkConfig, err := config.NewConfig(nad.Spec.Config)
		if err != nil {
			r.Log.Error(
				err,
				"failed extracting the relevant NAD configuration",
				"NAD name",
				nad.Name,
				"NAD namespace",
				nad.Namespace,
			)
			return nil, fmt.Errorf("failed to extract the relevant NAD information")
		}

		if networkConfig.Role == NetworkRolePrimary {
			return networkConfig, nil
		}
	}
	return nil, nil
}

func newIPAMClaim(
	claimKey string,
	vmi *virtv1.VirtualMachineInstance,
	ownerInfo metav1.OwnerReference,
	netConfigName string,
) *ipamclaimsapi.IPAMClaim {
	return &ipamclaimsapi.IPAMClaim{
		ObjectMeta: controllerruntime.ObjectMeta{
			Name:            claimKey,
			Namespace:       vmi.Namespace,
			OwnerReferences: []metav1.OwnerReference{ownerInfo},
			Finalizers:      []string{claims.KubevirtVMFinalizer},
			Labels:          claims.OwnedByVMLabel(vmi.Name),
		},
		Spec: ipamclaimsapi.IPAMClaimSpec{
			Network: netConfigName,
		},
	}
}

func (r *VirtualMachineInstanceReconciler) ensureIPAMClaim(
	ctx context.Context,
	ipamClaim *ipamclaimsapi.IPAMClaim,
	vmi *virtv1.VirtualMachineInstance,
	ownerInfo metav1.OwnerReference,
) error {
	claimKey := ipamClaim.Name
	if err := r.Client.Create(ctx, ipamClaim, &client.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			claimKey := apitypes.NamespacedName{
				Namespace: vmi.Namespace,
				Name:      claimKey,
			}

			existingIPAMClaim := &ipamclaimsapi.IPAMClaim{}
			if err := r.Client.Get(ctx, claimKey, existingIPAMClaim); err != nil {
				return fmt.Errorf("let us be on the safe side and retry later")
			}

			if len(existingIPAMClaim.OwnerReferences) == 1 && existingIPAMClaim.OwnerReferences[0].UID == ownerInfo.UID {
				r.Log.Info("found existing IPAMClaim belonging to this VM/VMI, nothing to do", "UID", ownerInfo.UID)
				return nil
			} else {
				err := fmt.Errorf("failed since it found an existing IPAMClaim for %q", claimKey.Name)
				r.Log.Error(err, "leaked IPAMClaim found", "existing owner", existingIPAMClaim.UID)
				return err
			}
		}
		r.Log.Error(err, "failed to create the IPAMClaim")
		return err
	}
	return nil
}

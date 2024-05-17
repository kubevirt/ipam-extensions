package vmnetworkscontroller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"

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

	"github.com/maiqueb/kubevirt-ipam-claims/pkg/config"
)

// VirtualMachineReconciler reconciles a VirtualMachineInstance object
type VirtualMachineReconciler struct {
	client.Client
	Log     logr.Logger
	Scheme  *runtime.Scheme
	manager controllerruntime.Manager
}

func NewVMReconciler(manager controllerruntime.Manager) *VirtualMachineReconciler {
	return &VirtualMachineReconciler{
		Client:  manager.GetClient(),
		Log:     controllerruntime.Log.WithName("controllers").WithName("VirtualMachineInstance"),
		Scheme:  manager.GetScheme(),
		manager: manager,
	}
}

func (r *VirtualMachineReconciler) Reconcile(
	ctx context.Context,
	request controllerruntime.Request,
) (controllerruntime.Result, error) {
	vmi := &virtv1.VirtualMachineInstance{}

	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	err := r.Client.Get(ctx, request.NamespacedName, vmi)
	if apierrors.IsNotFound(err) {
		r.Log.Error(err, "Error retrieving VMI")
		// Error reading the object - requeue the request.
		return controllerruntime.Result{}, err
	}

	var ownerInfo metav1.OwnerReference
	vm := &virtv1.VirtualMachine{}
	if err := r.Client.Get(ctx, request.NamespacedName, vm); apierrors.IsNotFound(err) {
		r.Log.Info("Corresponding VM not found", "vm", request.NamespacedName)
		ownerInfo = metav1.OwnerReference{APIVersion: vmi.APIVersion, Kind: vmi.Kind, Name: vmi.Name, UID: vmi.UID}
	} else if err == nil {
		ownerInfo = metav1.OwnerReference{APIVersion: vm.APIVersion, Kind: vm.Kind, Name: vm.Name, UID: vm.UID}
	} else {
		return controllerruntime.Result{}, fmt.Errorf(
			"error to get VMI %q corresponding VM: %w",
			request.NamespacedName,
			err,
		)
	}

	vmiSpec := vmi.Spec
	for _, net := range vmiSpec.Networks {
		if net.Pod != nil {
			continue
		}

		if net.Multus != nil {
			nadName := net.Multus.NetworkName
			namespace := vmi.Namespace
			namespaceAndName := strings.Split(nadName, "/")
			if len(namespaceAndName) == 2 {
				namespace = namespaceAndName[0]
				nadName = namespaceAndName[1]
			}

			ctx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			nad := &nadv1.NetworkAttachmentDefinition{}
			if err := r.Client.Get(
				ctx,
				apitypes.NamespacedName{Namespace: namespace, Name: nadName},
				nad,
			); err != nil {
				if apierrors.IsNotFound(err) {
					return controllerruntime.Result{}, err
				}
			}

			nadConfig, err := config.NewConfig(nad.Spec.Config)
			if err != nil {
				r.Log.Error(err, "failed extracting the relevant NAD configuration", "NAD name", nadName)
				return controllerruntime.Result{}, fmt.Errorf("failed to extract the relevant NAD information")
			}

			if nadConfig.AllowPersistentIPs {
				claimKey := fmt.Sprintf("%s.%s", vmi.Name, net.Name)
				ipamClaim := &ipamclaimsapi.IPAMClaim{
					ObjectMeta: controllerruntime.ObjectMeta{
						Name:            claimKey,
						Namespace:       vmi.Namespace,
						OwnerReferences: []metav1.OwnerReference{ownerInfo},
					},
					Spec: ipamclaimsapi.IPAMClaimSpec{
						Network: nadConfig.Name,
					}}

				if err := r.Client.Create(ctx, ipamClaim, &client.CreateOptions{}); err != nil {
					if apierrors.IsAlreadyExists(err) {
						claimKey := apitypes.NamespacedName{
							Namespace: vmi.Namespace,
							Name:      claimKey,
						}

						existingIPAMClaim := &ipamclaimsapi.IPAMClaim{}
						if err := r.Client.Get(ctx, claimKey, existingIPAMClaim); err != nil {
							if apierrors.IsNotFound(err) {
								// we assume it had already cleaned up in the few miliseconds it took to get here ...
								// TODO does this make sense? ... It's pretty much just for completeness.
								continue
							} else if err != nil {
								return controllerruntime.Result{}, fmt.Errorf("let us be on the safe side and retry later")
							}
						}
						if len(existingIPAMClaim.OwnerReferences) == 1 && existingIPAMClaim.OwnerReferences[0].UID == vm.UID {
							r.Log.Info("found existing IPAMClaim belonging to this VM, nothing to do", "VM UID", vm.UID)
							continue
						} else {
							err := fmt.Errorf("failed since it found an existing IPAMClaim for %q", claimKey.Name)
							r.Log.Error(err, "leaked IPAMClaim found", "existing owner", existingIPAMClaim.UID)
							return controllerruntime.Result{}, err
						}
					}
					r.Log.Error(err, "failed to create the IPAMClaim")
					return controllerruntime.Result{}, err
				}
			}
		}
	}

	return controllerruntime.Result{}, nil
}

// Setup sets up the controller with the Manager passed in the constructor.
func (r *VirtualMachineReconciler) Setup() error {
	return controllerruntime.NewControllerManagedBy(r.manager).
		For(&virtv1.VirtualMachineInstance{}).
		WithEventFilter(onVMPredicates()).
		Complete(r)
}

func onVMPredicates() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(createEvent event.CreateEvent) bool {
			return true
		},
		DeleteFunc: func(event.DeleteEvent) bool {
			return false
		},
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			return false
		},
		GenericFunc: func(event.GenericEvent) bool {
			return false
		},
	}
}

package vmnetworkscontroller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/kubevirt/ipam-extensions/pkg/claims"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	virtv1 "kubevirt.io/api/core/v1"
)

// VirtualMachineReconciler reconciles a VirtualMachine object
type VirtualMachineReconciler struct {
	client.Client
	Log     logr.Logger
	Scheme  *runtime.Scheme
	manager controllerruntime.Manager
}

func NewVMReconciler(manager controllerruntime.Manager) *VirtualMachineReconciler {
	return &VirtualMachineReconciler{
		Client:  manager.GetClient(),
		Log:     controllerruntime.Log.WithName("controllers").WithName("VirtualMachine"),
		Scheme:  manager.GetScheme(),
		manager: manager,
	}
}

func (r *VirtualMachineReconciler) Reconcile(
	ctx context.Context,
	request controllerruntime.Request,
) (controllerruntime.Result, error) {
	vm := &virtv1.VirtualMachine{}
	contextWithTimeout, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	shouldRemoveFinalizer := false
	err := r.Client.Get(contextWithTimeout, request.NamespacedName, vm)
	if apierrors.IsNotFound(err) {
		shouldRemoveFinalizer = true
	} else if err != nil {
		return controllerruntime.Result{}, err
	}

	if vm.DeletionTimestamp != nil {
		vmi := &virtv1.VirtualMachineInstance{}
		contextWithTimeout, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		err := r.Client.Get(contextWithTimeout, request.NamespacedName, vmi)
		if apierrors.IsNotFound(err) {
			shouldRemoveFinalizer = true
		} else if err != nil {
			return controllerruntime.Result{}, err
		}
	}

	if shouldRemoveFinalizer {
		if err := claims.Cleanup(r.Client, request.NamespacedName); err != nil {
			return controllerruntime.Result{}, fmt.Errorf("failed removing the IPAMClaims finalizer: %w", err)
		}
	}

	return controllerruntime.Result{}, nil
}

// Setup sets up the controller with the Manager passed in the constructor.
func (r *VirtualMachineReconciler) Setup() error {
	return controllerruntime.NewControllerManagedBy(r.manager).
		For(&virtv1.VirtualMachine{}).
		WithEventFilter(onVMPredicates()).
		Complete(r)
}

func onVMPredicates() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(createEvent event.CreateEvent) bool {
			return false
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

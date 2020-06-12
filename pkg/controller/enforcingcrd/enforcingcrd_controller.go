package enforcingcrd

import (
	"context"
	errs "errors"

	examplev1alpha1 "github.com/redhat-cop/operator-utils/pkg/apis/example/v1alpha1"
	"github.com/redhat-cop/operator-utils/pkg/util"
	"github.com/redhat-cop/operator-utils/pkg/util/lockedresourcecontroller"
	"github.com/redhat-cop/operator-utils/pkg/util/lockedresourcecontroller/lockedpatch"
	"github.com/redhat-cop/operator-utils/pkg/util/lockedresourcecontroller/lockedresource"
	"github.com/scylladb/go-set/strset"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var controllerName = "enforcingcrd_controller"
var log = logf.Log.WithName(controllerName)

// ReconcileEnforcingCRD reconciles a EnforcingCRD object
type ReconcileEnforcingCRD struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	lockedresourcecontroller.EnforcingReconciler
}

/**
* USER ACTION REQUIRED: This is a scaffold file intended for the user to modify with their own Controller
* business logic.  Delete these comments after modifying this file.*
 */

// Add creates a new EnforcingCRD Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileEnforcingCRD{
		EnforcingReconciler: lockedresourcecontroller.NewEnforcingReconciler(mgr.GetClient(), mgr.GetScheme(), mgr.GetConfig(), mgr.GetEventRecorderFor(controllerName)),
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("enforcingcrd-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource EnforcingCRD
	err = c.Watch(&source.Kind{Type: &examplev1alpha1.EnforcingCRD{
		TypeMeta: metav1.TypeMeta{
			Kind: "EnforcingCRD",
		}}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	//if interested in updates from the managed resources
	// watch for changes in status in the locked resources
	enforcingReconciler, ok := r.(*ReconcileEnforcingCRD)
	if !ok {
		err := errs.New("unable to convert reconciler to ReconcileEnforcingCRD")
		log.Error(err, "unable to convert to ReconcileEnforcingCRD", "reconciler", r)
		return err
	}
	err = c.Watch(
		&source.Channel{Source: enforcingReconciler.GetStatusChangeChannel()},
		&handler.EnqueueRequestForObject{},
	)
	if err != nil {
		return err
	}

	if ok, err := enforcingReconciler.IsAPIResourceAvailable(schema.GroupVersionKind{
		Group:   "example.io",
		Version: "v1alpha1",
		Kind:    "EnforcingCRD",
	}); !ok || err != nil {
		log.Info("unable to lookup own API")
		if err != nil {
			return err
		}
		return errs.New("unable to lookup own API")
	}

	return nil
}

// blank assignment to verify that ReconcileEnforcingCRD implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileEnforcingCRD{}

// Reconcile reads that state of the cluster for a EnforcingCRD object and makes changes based on the state read
// and what is in the EnforcingCRD.Spec
// TODO(user): Modify this Reconcile function to implement your Controller logic.  This example creates
// a Pod as an example
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileEnforcingCRD) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("Reconciling EnforcingCRD")

	// Fetch the EnforcingCRD instance
	instance := &examplev1alpha1.EnforcingCRD{}
	err := r.GetClient().Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	if ok := r.IsInitialized(instance); !ok {
		err := r.GetClient().Update(context.TODO(), instance)
		if err != nil {
			log.Error(err, "unable to update instance", "instance", instance)
			return r.ManageError(instance, err)
		}
		return reconcile.Result{}, nil
	}

	if util.IsBeingDeleted(instance) {
		if !util.HasFinalizer(instance, controllerName) {
			return reconcile.Result{}, nil
		}
		err := r.manageCleanUpLogic(instance)
		if err != nil {
			log.Error(err, "unable to delete instance", "instance", instance)
			return r.ManageError(instance, err)
		}
		util.RemoveFinalizer(instance, controllerName)
		err = r.GetClient().Update(context.TODO(), instance)
		if err != nil {
			log.Error(err, "unable to update instance", "instance", instance)
			return r.ManageError(instance, err)
		}
		return reconcile.Result{}, nil
	}

	lockedResources, err := lockedresource.GetLockedResources(instance.Spec.Resources)
	if err != nil {
		log.Error(err, "unable to get locked resources")
		return r.ManageError(instance, err)
	}
	err = r.UpdateLockedResources(instance, lockedResources, []lockedpatch.LockedPatch{})
	if err != nil {
		log.Error(err, "unable to update locked resources")
		return r.ManageError(instance, err)
	}

	return r.ManageSuccess(instance)
}

func (r *ReconcileEnforcingCRD) manageCleanUpLogic(instance *examplev1alpha1.EnforcingCRD) error {
	err := r.Terminate(instance, true)
	if err != nil {
		log.Error(err, "unable to terminate enforcing reconciler for", "instance", instance)
		return err
	}
	return nil
}

// IsInitialized can be used to check if isntance is correctlty initialuzed.
// returns false it it's not.
func (r *ReconcileEnforcingCRD) IsInitialized(instance *examplev1alpha1.EnforcingCRD) bool {
	needsUpdate := true
	for i := range instance.Spec.Resources {
		currentSet := strset.New(instance.Spec.Resources[i].ExcludedPaths...)
		if !currentSet.IsEqual(strset.Union(lockedresource.DefaultExcludedPathsSet, currentSet)) {
			instance.Spec.Resources[i].ExcludedPaths = strset.Union(lockedresource.DefaultExcludedPathsSet, currentSet).List()
			needsUpdate = false
		}
	}
	if len(instance.Spec.Resources) > 0 && !util.HasFinalizer(instance, controllerName) {
		util.AddFinalizer(instance, controllerName)
		needsUpdate = false
	}
	if len(instance.Spec.Resources) == 0 && util.HasFinalizer(instance, controllerName) {
		util.RemoveFinalizer(instance, controllerName)
		needsUpdate = false
	}
	return needsUpdate
}

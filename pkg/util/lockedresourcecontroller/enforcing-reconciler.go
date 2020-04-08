package lockedresourcecontroller

import (
	"context"
	"errors"

	"github.com/redhat-cop/operator-utils/pkg/util"
	"github.com/redhat-cop/operator-utils/pkg/util/apis"
	"github.com/redhat-cop/operator-utils/pkg/util/lockedresourcecontroller/lockedresource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// EnforcingReconciler is a reconciler designed to as a base type to extend for those operators that compute a set of resources that then need to be kept in place (i.e. enforced)
// the enforcing piece is taken care for, an implementor would just neeed to take care of the logic that computes the resorces to be enforced.
type EnforcingReconciler struct {
	util.ReconcilerBase
	lockedResourceManagers map[string]*LockedResourceManager
	statusChange           chan event.GenericEvent
}

//NewEnforcingReconciler creates a new EnforcingReconciler
func NewEnforcingReconciler(client client.Client, scheme *runtime.Scheme, restConfig *rest.Config, recorder record.EventRecorder) EnforcingReconciler {
	return EnforcingReconciler{
		ReconcilerBase:         util.NewReconcilerBase(client, scheme, restConfig, recorder),
		lockedResourceManagers: map[string]*LockedResourceManager{},
		statusChange:           make(chan event.GenericEvent),
	}
}

//GetStatusChangeChannel returns the channel thoughr which status change events can be received
func (er *EnforcingReconciler) GetStatusChangeChannel() <-chan event.GenericEvent {
	return er.statusChange
}

func (er *EnforcingReconciler) getLockedResourceManager(parent metav1.Object) (*LockedResourceManager, error) {
	lockedResourceManager, ok := er.lockedResourceManagers[apis.GetKeyShort(parent)]
	if !ok {
		lockedResourceManager, err := NewLockedResourceManager(er.GetRestConfig(), manager.Options{}, parent, er.statusChange)
		if err != nil {
			log.Error(err, "unable to create LockedResourceManager")
			return &LockedResourceManager{}, err
		}
		er.lockedResourceManagers[apis.GetKeyShort(parent)] = &lockedResourceManager
		return &lockedResourceManager, nil
	}
	return lockedResourceManager, nil
}

// UpdateLockedResources will do the following:
// 1. initialize or retrieve the LockedResourceManager related to the passed parent resource
// 2. compare the currently enfrced resources with the one passed as parameters and then
//    a. return immediately if they are the same
//    b. restart the LockedResourceManager if they don't match
func (er *EnforcingReconciler) UpdateLockedResources(parent metav1.Object, lockedResources []lockedresource.LockedResource) error {
	lockedResourceManager, err := er.getLockedResourceManager(parent)
	if err != nil {
		log.Error(err, "unable to get LockedResourceManager")
		return err
	}
	same, leftDifference, _, _ := lockedResourceManager.IsSameResources(lockedResources)
	if !same {
		lockedResourceManager.Restart(lockedResources, false)
		err := er.DeleteUnstructuredResources(lockedresource.AsListOfUnstructured(leftDifference))
		if err != nil {
			log.Error(err, "unable to delete unmanaged", "resources", leftDifference)
			return err
		}
	}
	return nil
}

//ManageError manage error sets an error status in the CR and fires an event, finally it returns the error so the operator can re-attempt
func (er *EnforcingReconciler) ManageError(obj metav1.Object, issue error) (reconcile.Result, error) {
	runtimeObj, ok := (obj).(runtime.Object)
	if !ok {
		log.Error(errors.New("not a runtime.Object"), "passed object was not a runtime.Object", "object", obj)
		return reconcile.Result{}, nil
	}
	er.GetRecorder().Event(runtimeObj, "Warning", "ProcessingError", issue.Error())
	if enforcingReconcileStatusAware, updateStatus := (obj).(apis.EnforcingReconcileStatusAware); updateStatus {
		status := apis.EnforcingReconcileStatus{
			ReconcileStatus: apis.ReconcileStatus{
				LastUpdate: metav1.Now(),
				Reason:     issue.Error(),
				Status:     "Failure",
			},
			LockedResourceStatuses: er.GetLockedResourceStatuses(obj),
		}
		enforcingReconcileStatusAware.SetEnforcingReconcileStatus(status)
		err := er.GetClient().Status().Update(context.Background(), runtimeObj)
		if err != nil {
			log.Error(err, "unable to update status for", "object", runtimeObj)
			return reconcile.Result{}, err
		}
	} else {
		log.Info("object is not RecocileStatusAware, not setting status")
	}
	return reconcile.Result{}, issue
}

// ManageSuccess will update the status of the CR and return a successful reconcile result
func (er *EnforcingReconciler) ManageSuccess(obj metav1.Object) (reconcile.Result, error) {
	runtimeObj, ok := (obj).(runtime.Object)
	if !ok {
		err := errors.New("not a runtime.Object")
		log.Error(err, "passed object was not a runtime.Object", "object", obj)
		return reconcile.Result{}, err
	}
	if enforcingReconcileStatusAware, updateStatus := (obj).(apis.EnforcingReconcileStatusAware); updateStatus {
		status := apis.EnforcingReconcileStatus{
			ReconcileStatus: apis.ReconcileStatus{
				LastUpdate: metav1.Now(),
				Reason:     "",
				Status:     "Success",
			},
			LockedResourceStatuses: er.GetLockedResourceStatuses(obj),
		}
		enforcingReconcileStatusAware.SetEnforcingReconcileStatus(status)
		err := er.GetClient().Status().Update(context.Background(), runtimeObj)
		if err != nil {
			log.Error(err, "unable to update status for", "object", runtimeObj)
			return reconcile.Result{}, err
		}
	} else {
		log.Info("object is not RecocileStatusAware, not setting status")
	}
	return reconcile.Result{}, nil
}

// GetLockedResourceStatuses returns the status for all LockedResources
func (er *EnforcingReconciler) GetLockedResourceStatuses(obj metav1.Object) map[string]apis.ReconcileStatus {
	lockedResourceManager, err := er.getLockedResourceManager(obj)
	if err != nil {
		log.Info("unable to get locked resource manager for", "parent", obj)
		return map[string]apis.ReconcileStatus{}
	}
	lockedResourceReconcileStatuses := map[string]apis.ReconcileStatus{}
	for _, lockedResourceReconciler := range lockedResourceManager.GetResourceReconcilers() {
		lockedResourceReconcileStatuses[apis.GetKeyLong(&lockedResourceReconciler.Resource)] = lockedResourceReconciler.GetStatus()
	}
	return lockedResourceReconcileStatuses
}

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	nlbv1 "github.com/chrisliu1995/AlibabaCloud-NLB-Operator/pkg/apis/nlboperator/v1"
	"github.com/chrisliu1995/AlibabaCloud-NLB-Operator/pkg/provider"
)

const (
	sgRequeueShort      = 5 * time.Second
	sgRequeueDeletion   = 5 * time.Second
	sgRequeueThrottling = 60 * time.Second
	sgRequeueError      = 5 * time.Second

	cloudSGStatusAvailable = "Available"
)

// ServerGroupReconciler reconciles a ServerGroup CR with its cloud counterpart.
type ServerGroupReconciler struct {
	client.Client
	Scheme                  *runtime.Scheme
	Recorder                record.EventRecorder
	NLBClient               *provider.NLBClient
	MaxConcurrentReconciles int
}

// +kubebuilder:rbac:groups=nlboperator.alibabacloud.com,resources=servergroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nlboperator.alibabacloud.com,resources=servergroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nlboperator.alibabacloud.com,resources=servergroups/finalizers,verbs=update
// +kubebuilder:rbac:groups=nlboperator.alibabacloud.com,resources=listeners,verbs=get;list;watch

// Reconcile handles ServerGroup lifecycle.
func (r *ServerGroupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := klog.FromContext(ctx).WithValues("servergroup", req.NamespacedName)

	sg := &nlbv1.ServerGroup{}
	if err := r.Get(ctx, req.NamespacedName, sg); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get ServerGroup")
		return ctrl.Result{}, err
	}

	if !sg.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, sg)
	}

	if !controllerutil.ContainsFinalizer(sg, nlbv1.ServerGroupFinalizer) {
		controllerutil.AddFinalizer(sg, nlbv1.ServerGroupFinalizer)
		if err := r.Update(ctx, sg); err != nil {
			log.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	return r.handleCreateOrSync(ctx, sg)
}

// handleCreateOrSync drives the Pending -> Creating -> Active state machine.
func (r *ServerGroupReconciler) handleCreateOrSync(ctx context.Context, sg *nlbv1.ServerGroup) (ctrl.Result, error) {
	log := klog.FromContext(ctx)

	switch sg.Status.Phase {
	case "", nlbv1.ServerGroupPending:
		// Idempotency: try to find an existing SG in the cloud by VPC + name first.
		existingId, err := r.NLBClient.ListServerGroups(ctx, sg.Spec.VpcId, sg.Spec.ServerGroupName)
		if err != nil {
			r.Recorder.Eventf(sg, corev1.EventTypeWarning, "ListServerGroupsFailed",
				"Failed to query existing ServerGroup: %v", err)
			return r.requeueOnAPIError(err), nil
		}
		if existingId != "" {
			log.Info("Found existing cloud ServerGroup, adopting it", "serverGroupId", existingId)
			sg.Status.ServerGroupId = existingId
			sg.Status.Phase = nlbv1.ServerGroupActive
			sg.Status.Message = "Adopted existing cloud ServerGroup"
			if err := r.Status().Update(ctx, sg); err != nil {
				return ctrl.Result{}, err
			}
			r.Recorder.Eventf(sg, corev1.EventTypeNormal, "Adopted",
				"Adopted existing cloud ServerGroup %s", existingId)
			return ctrl.Result{}, nil
		}

		// Create new SG.
		log.Info("Creating cloud ServerGroup", "name", sg.Spec.ServerGroupName)
		newId, err := r.NLBClient.CreateServerGroup(ctx, sg)
		if err != nil {
			r.Recorder.Eventf(sg, corev1.EventTypeWarning, "CreateFailed",
				"Failed to create ServerGroup: %v", err)
			sg.Status.Phase = nlbv1.ServerGroupPending
			sg.Status.Message = fmt.Sprintf("create failed: %v", err)
			_ = r.Status().Update(ctx, sg)
			return r.requeueOnAPIError(err), nil
		}
		sg.Status.ServerGroupId = newId
		sg.Status.Phase = nlbv1.ServerGroupCreating
		sg.Status.Message = "ServerGroup creation submitted"
		if err := r.Status().Update(ctx, sg); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Eventf(sg, corev1.EventTypeNormal, "Creating",
			"Submitted CreateServerGroup, id=%s", newId)
		return ctrl.Result{RequeueAfter: sgRequeueShort}, nil

	case nlbv1.ServerGroupCreating:
		if sg.Status.ServerGroupId == "" {
			sg.Status.Phase = nlbv1.ServerGroupPending
			if err := r.Status().Update(ctx, sg); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
		attr, err := r.NLBClient.GetServerGroupAttribute(ctx, sg.Status.ServerGroupId)
		if err != nil {
			r.Recorder.Eventf(sg, corev1.EventTypeWarning, "GetAttributeFailed",
				"Failed to query ServerGroup %s: %v", sg.Status.ServerGroupId, err)
			return r.requeueOnAPIError(err), nil
		}
		if attr == nil {
			// Cloud SG vanished - reset to Pending so the next reconcile recreates it.
			log.Info("Cloud ServerGroup not found while in Creating, resetting to Pending",
				"serverGroupId", sg.Status.ServerGroupId)
			sg.Status.ServerGroupId = ""
			sg.Status.Phase = nlbv1.ServerGroupPending
			sg.Status.Message = "Cloud ServerGroup disappeared, will recreate"
			if err := r.Status().Update(ctx, sg); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
		if attr.ServerGroupStatus == cloudSGStatusAvailable {
			sg.Status.Phase = nlbv1.ServerGroupActive
			sg.Status.Message = "ServerGroup is available"
			if err := r.Status().Update(ctx, sg); err != nil {
				return ctrl.Result{}, err
			}
			r.Recorder.Eventf(sg, corev1.EventTypeNormal, "Active",
				"ServerGroup %s is now Available", sg.Status.ServerGroupId)
			return ctrl.Result{}, nil
		}
		log.V(2).Info("ServerGroup not yet available", "cloudStatus", attr.ServerGroupStatus)
		return ctrl.Result{RequeueAfter: sgRequeueShort}, nil

	case nlbv1.ServerGroupActive:
		if sg.Status.ServerGroupId == "" {
			sg.Status.Phase = nlbv1.ServerGroupPending
			_ = r.Status().Update(ctx, sg)
			return ctrl.Result{Requeue: true}, nil
		}
		// Reconcile complete: no further requeue, no health check.
		return ctrl.Result{}, nil

	default:
		// Unknown phase - reset to Pending.
		log.Info("Resetting ServerGroup to Pending from unknown phase", "phase", sg.Status.Phase)
		sg.Status.Phase = nlbv1.ServerGroupPending
		if err := r.Status().Update(ctx, sg); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
}

// handleDeletion drives the SG removal flow.
func (r *ServerGroupReconciler) handleDeletion(ctx context.Context, sg *nlbv1.ServerGroup) (ctrl.Result, error) {
	log := klog.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(sg, nlbv1.ServerGroupFinalizer) {
		return ctrl.Result{}, nil
	}

	// 1. If we never created a cloud SG, just drop the finalizer.
	if sg.Status.ServerGroupId == "" {
		controllerutil.RemoveFinalizer(sg, nlbv1.ServerGroupFinalizer)
		if err := r.Update(ctx, sg); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// 2. Block until no Listener CR still references this SG.
	listenerList := &nlbv1.ListenerList{}
	if err := r.List(ctx, listenerList); err != nil {
		log.Error(err, "Failed to list Listeners while deleting ServerGroup")
		return ctrl.Result{}, err
	}
	var refCount int
	for _, lsn := range listenerList.Items {
		if lsn.Spec.ServerGroupRef == sg.Name {
			refCount++
		}
	}
	if refCount > 0 {
		r.Recorder.Eventf(sg, corev1.EventTypeNormal, "WaitingForListenerDeletion",
			"Waiting for %d Listener(s) referencing ServerGroup %s to be deleted",
			refCount, sg.Name)
		log.Info("Waiting for Listener CRs to be deleted before deleting ServerGroup",
			"servergroup", sg.Name, "referencingListeners", refCount)
		return ctrl.Result{RequeueAfter: sgRequeueDeletion}, nil
	}

	// 3. Confirm cloud SG state.
	attr, err := r.NLBClient.GetServerGroupAttribute(ctx, sg.Status.ServerGroupId)
	if err != nil {
		r.Recorder.Eventf(sg, corev1.EventTypeWarning, "GetAttributeFailed",
			"Failed to query ServerGroup %s during deletion: %v", sg.Status.ServerGroupId, err)
		return r.requeueOnAPIError(err), nil
	}

	// 4. Cloud SG already gone -> drop finalizer.
	if attr == nil {
		log.Info("Cloud ServerGroup already gone, removing finalizer",
			"serverGroupId", sg.Status.ServerGroupId)
		controllerutil.RemoveFinalizer(sg, nlbv1.ServerGroupFinalizer)
		if err := r.Update(ctx, sg); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// 5. Mark Deleting and call DeleteServerGroup.
	if sg.Status.Phase != nlbv1.ServerGroupDeleting {
		sg.Status.Phase = nlbv1.ServerGroupDeleting
		sg.Status.Message = "Submitting DeleteServerGroup"
		if err := r.Status().Update(ctx, sg); err != nil {
			log.Error(err, "Failed to update SG status to Deleting")
		}
	}

	if err := r.NLBClient.DeleteServerGroup(ctx, sg.Status.ServerGroupId); err != nil {
		r.Recorder.Eventf(sg, corev1.EventTypeWarning, "DeleteFailed",
			"Failed to delete ServerGroup %s: %v", sg.Status.ServerGroupId, err)
		return r.requeueOnAPIError(err), nil
	}

	r.Recorder.Eventf(sg, corev1.EventTypeNormal, "Deleting",
		"Submitted DeleteServerGroup for %s", sg.Status.ServerGroupId)

	// 6. Requeue to confirm cloud-side completion before removing finalizer.
	return ctrl.Result{RequeueAfter: sgRequeueDeletion}, nil
}

func (r *ServerGroupReconciler) requeueOnAPIError(err error) ctrl.Result {
	if provider.IsThrottlingError(err) {
		return ctrl.Result{RequeueAfter: sgRequeueThrottling}
	}
	return ctrl.Result{RequeueAfter: sgRequeueError}
}

// SetupWithManager wires the controller into the manager.
func (r *ServerGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	maxConcurrent := r.MaxConcurrentReconciles
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&nlbv1.ServerGroup{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: maxConcurrent,
		}).
		Complete(r)
}

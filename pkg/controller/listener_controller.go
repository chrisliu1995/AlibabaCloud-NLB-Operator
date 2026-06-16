package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	listenerRequeueShort      = 30 * time.Second
	listenerRequeueThrottling = 60 * time.Second
	listenerRequeueError      = 5 * time.Second

	cloudListenerStatusRunning = "Running"
)

// ListenerReconciler reconciles a Listener CR with its cloud counterpart.
type ListenerReconciler struct {
	client.Client
	Scheme                  *runtime.Scheme
	Recorder                record.EventRecorder
	NLBClient               *provider.NLBClient
	MaxConcurrentReconciles int
}

// +kubebuilder:rbac:groups=nlboperator.alibabacloud.com,resources=listeners,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nlboperator.alibabacloud.com,resources=listeners/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nlboperator.alibabacloud.com,resources=listeners/finalizers,verbs=update
// +kubebuilder:rbac:groups=nlboperator.alibabacloud.com,resources=nlbs,verbs=get;list;watch
// +kubebuilder:rbac:groups=nlboperator.alibabacloud.com,resources=servergroups,verbs=get;list;watch

// Reconcile handles Listener lifecycle.
func (r *ListenerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := klog.FromContext(ctx).WithValues("listener", req.NamespacedName)

	lsn := &nlbv1.Listener{}
	if err := r.Get(ctx, req.NamespacedName, lsn); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get Listener")
		return ctrl.Result{}, err
	}

	if !lsn.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, lsn)
	}

	if !controllerutil.ContainsFinalizer(lsn, nlbv1.ListenerFinalizer) {
		controllerutil.AddFinalizer(lsn, nlbv1.ListenerFinalizer)
		if err := r.Update(ctx, lsn); err != nil {
			log.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	return r.handleCreateOrSync(ctx, lsn)
}

// handleCreateOrSync drives the Pending -> Creating -> Running state machine.
func (r *ListenerReconciler) handleCreateOrSync(ctx context.Context, lsn *nlbv1.Listener) (ctrl.Result, error) {
	log := klog.FromContext(ctx)

	switch lsn.Status.Phase {
	case "", nlbv1.ListenerPending:
		// Validate prerequisites: NLB CR Active and ServerGroup CR Active.
		nlbId, sgId, ready, msg, err := r.resolveRefs(ctx, lsn)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !ready {
			r.Recorder.Eventf(lsn, corev1.EventTypeNormal, "WaitingForDependencies", msg)
			lsn.Status.Phase = nlbv1.ListenerPending
			lsn.Status.Message = msg
			_ = r.Status().Update(ctx, lsn)
			return ctrl.Result{RequeueAfter: listenerRequeueShort}, nil
		}

		// ID short-circuit: if ListenerId already known from a previous Create,
		// skip ListListeners and Create — go straight to GetListenerAttribute.
		if lsn.Status.ListenerId != "" {
			attr, err := r.NLBClient.GetListenerAttribute(ctx, lsn.Status.ListenerId)
			if err != nil {
				return r.requeueOnAPIError(err), nil
			}
			if attr == nil {
				// Cloud listener disappeared (GetListenerAttribute returns nil for NotFound).
				log.Info("Cloud listener disappeared, will recreate", "listenerId", lsn.Status.ListenerId)
				lsn.Status.ListenerId = ""
				lsn.Status.Message = "Cloud listener disappeared, will recreate"
				_ = r.Status().Update(ctx, lsn)
				return ctrl.Result{Requeue: true}, nil
			}
			// Found on cloud — transition based on status.
			if attr.ListenerStatus == cloudListenerStatusRunning {
				lsn.Status.Phase = nlbv1.ListenerRunning
				lsn.Status.Message = "Listener is running"
				if err := r.Status().Update(ctx, lsn); err != nil {
					return ctrl.Result{}, err
				}
				r.Recorder.Eventf(lsn, corev1.EventTypeNormal, "Running",
					"Listener %s is now Running", lsn.Status.ListenerId)
				return ctrl.Result{}, nil
			}
			// Still creating on cloud side.
			lsn.Status.Phase = nlbv1.ListenerCreating
			lsn.Status.Message = "Listener is being created"
			if err := r.Status().Update(ctx, lsn); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: listenerRequeueShort}, nil
		}

		// Optimistic create: directly call CreateNLBListener without prior ListListeners.
		log.Info("Creating cloud Listener (optimistic)", "nlbId", nlbId, "port", lsn.Spec.ListenerPort,
			"protocol", lsn.Spec.ListenerProtocol)
		newId, err := r.NLBClient.CreateNLBListener(ctx, nlbId, sgId,
			lsn.Spec.ListenerPort, lsn.Spec.ListenerProtocol)
		if err != nil {
			// Local rate limit: requeue quickly without cloud call.
			if provider.IsLocalRateLimited(err) {
				return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
			}
			// Fallback: if "already exists" error, use ListListeners to Adopt.
			if provider.IsResourceAlreadyExistsError(err) {
				r.Recorder.Eventf(lsn, corev1.EventTypeNormal, "AlreadyExists",
					"Listener already exists on cloud, falling back to ListListeners for adopt")
				existingId, listErr := r.NLBClient.ListListeners(ctx, nlbId, lsn.Spec.ListenerPort)
				if listErr != nil {
					return r.requeueOnAPIError(listErr), nil
				}
				if existingId != "" {
					log.Info("Adopted existing cloud Listener after AlreadyExists error", "listenerId", existingId)
					lsn.Status.ListenerId = existingId
					lsn.Status.Phase = nlbv1.ListenerRunning
					lsn.Status.Message = "Adopted existing cloud Listener"
					if err := r.Status().Update(ctx, lsn); err != nil {
						return ctrl.Result{}, err
					}
					r.Recorder.Eventf(lsn, corev1.EventTypeNormal, "Adopted",
						"Adopted existing cloud Listener %s", existingId)
					return ctrl.Result{}, nil
				}
			}
			r.Recorder.Eventf(lsn, corev1.EventTypeWarning, "CreateFailed",
				"Failed to create Listener: %v", err)
			lsn.Status.Phase = nlbv1.ListenerPending
			lsn.Status.Message = fmt.Sprintf("create failed: %v", err)
			_ = r.Status().Update(ctx, lsn)
			return r.requeueOnAPIError(err), nil
		}

		// Create succeeded — record ID and transition to Creating.
		lsn.Status.ListenerId = newId
		lsn.Status.Phase = nlbv1.ListenerCreating
		lsn.Status.Message = "Listener creation submitted"
		if err := r.Status().Update(ctx, lsn); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Eventf(lsn, corev1.EventTypeNormal, "Creating",
			"Submitted CreateListener, id=%s", newId)
		return ctrl.Result{RequeueAfter: listenerRequeueShort}, nil

	case nlbv1.ListenerCreating:
		if lsn.Status.ListenerId == "" {
			lsn.Status.Phase = nlbv1.ListenerPending
			if err := r.Status().Update(ctx, lsn); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
		attr, err := r.NLBClient.GetListenerAttribute(ctx, lsn.Status.ListenerId)
		if err != nil {
			if provider.IsLocalRateLimited(err) {
				return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
			}
			r.Recorder.Eventf(lsn, corev1.EventTypeWarning, "GetAttributeFailed",
				"Failed to query Listener %s: %v", lsn.Status.ListenerId, err)
			return r.requeueOnAPIError(err), nil
		}
		if attr == nil {
			log.Info("Cloud Listener not found while in Creating, resetting to Pending",
				"listenerId", lsn.Status.ListenerId)
			lsn.Status.ListenerId = ""
			lsn.Status.Phase = nlbv1.ListenerPending
			lsn.Status.Message = "Cloud Listener disappeared, will recreate"
			if err := r.Status().Update(ctx, lsn); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
		if attr.ListenerStatus == cloudListenerStatusRunning {
			lsn.Status.Phase = nlbv1.ListenerRunning
			lsn.Status.Message = "Listener is running"
			if err := r.Status().Update(ctx, lsn); err != nil {
				return ctrl.Result{}, err
			}
			r.Recorder.Eventf(lsn, corev1.EventTypeNormal, "Running",
				"Listener %s is now Running", lsn.Status.ListenerId)
			return ctrl.Result{}, nil
		}
		log.V(2).Info("Listener not yet running", "cloudStatus", attr.ListenerStatus)
		return ctrl.Result{RequeueAfter: listenerRequeueShort}, nil

	case nlbv1.ListenerRunning:
		if lsn.Status.ListenerId == "" {
			lsn.Status.Phase = nlbv1.ListenerPending
			_ = r.Status().Update(ctx, lsn)
			return ctrl.Result{Requeue: true}, nil
		}
		// Reconcile complete: no further requeue, no health check.
		return ctrl.Result{}, nil

	default:
		log.Info("Resetting Listener to Pending from unknown phase", "phase", lsn.Status.Phase)
		lsn.Status.Phase = nlbv1.ListenerPending
		if err := r.Status().Update(ctx, lsn); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
}

// resolveRefs reads referenced NLB and ServerGroup CRs and returns
// the resolved cloud IDs. ready=false means we should requeue.
func (r *ListenerReconciler) resolveRefs(ctx context.Context, lsn *nlbv1.Listener) (string, string, bool, string, error) {
	nlb := &nlbv1.NLB{}
	nlbKey := types.NamespacedName{Namespace: lsn.Namespace, Name: lsn.Spec.LoadBalancerRef}
	if err := r.Get(ctx, nlbKey, nlb); err != nil {
		if errors.IsNotFound(err) {
			return "", "", false, fmt.Sprintf("NLB CR %s not found", lsn.Spec.LoadBalancerRef), nil
		}
		return "", "", false, "", err
	}
	if nlb.Status.LoadBalancerId == "" || nlb.Status.LoadBalancerStatus != provider.LoadBalancerStatusActive {
		return "", "", false,
			fmt.Sprintf("NLB %s not ready (status=%s, id=%q)", nlb.Name, nlb.Status.LoadBalancerStatus, nlb.Status.LoadBalancerId),
			nil
	}

	sg := &nlbv1.ServerGroup{}
	sgKey := types.NamespacedName{Namespace: lsn.Namespace, Name: lsn.Spec.ServerGroupRef}
	if err := r.Get(ctx, sgKey, sg); err != nil {
		if errors.IsNotFound(err) {
			return "", "", false, fmt.Sprintf("ServerGroup CR %s not found", lsn.Spec.ServerGroupRef), nil
		}
		return "", "", false, "", err
	}
	if sg.Status.ServerGroupId == "" || sg.Status.Phase != nlbv1.ServerGroupActive {
		return "", "", false,
			fmt.Sprintf("ServerGroup %s not ready (phase=%s, id=%q)", sg.Name, sg.Status.Phase, sg.Status.ServerGroupId),
			nil
	}

	return nlb.Status.LoadBalancerId, sg.Status.ServerGroupId, true, "", nil
}

// handleDeletion drives the Listener removal flow.
func (r *ListenerReconciler) handleDeletion(ctx context.Context, lsn *nlbv1.Listener) (ctrl.Result, error) {
	log := klog.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(lsn, nlbv1.ListenerFinalizer) {
		return ctrl.Result{}, nil
	}

	// 1. Never created -> drop finalizer.
	if lsn.Status.ListenerId == "" {
		controllerutil.RemoveFinalizer(lsn, nlbv1.ListenerFinalizer)
		if err := r.Update(ctx, lsn); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// 2. Confirm cloud state.
	attr, err := r.NLBClient.GetListenerAttribute(ctx, lsn.Status.ListenerId)
	if err != nil {
		r.Recorder.Eventf(lsn, corev1.EventTypeWarning, "GetAttributeFailed",
			"Failed to query Listener %s during deletion: %v", lsn.Status.ListenerId, err)
		return r.requeueOnAPIError(err), nil
	}

	// 3. Already gone -> drop finalizer.
	if attr == nil {
		log.Info("Cloud Listener already gone, removing finalizer",
			"listenerId", lsn.Status.ListenerId)
		controllerutil.RemoveFinalizer(lsn, nlbv1.ListenerFinalizer)
		if err := r.Update(ctx, lsn); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// 4. Mark Deleting and call DeleteListener.
	if lsn.Status.Phase != nlbv1.ListenerDeleting {
		lsn.Status.Phase = nlbv1.ListenerDeleting
		lsn.Status.Message = "Submitting DeleteListener"
		if err := r.Status().Update(ctx, lsn); err != nil {
			log.Error(err, "Failed to update Listener status to Deleting")
		}
	}

	if err := r.NLBClient.DeleteNLBListener(ctx, lsn.Status.ListenerId); err != nil {
		r.Recorder.Eventf(lsn, corev1.EventTypeWarning, "DeleteFailed",
			"Failed to delete Listener %s: %v", lsn.Status.ListenerId, err)
		return r.requeueOnAPIError(err), nil
	}

	r.Recorder.Eventf(lsn, corev1.EventTypeNormal, "Deleting",
		"Submitted DeleteListener for %s", lsn.Status.ListenerId)

	// 5. Requeue to confirm cloud-side completion before removing finalizer.
	return ctrl.Result{RequeueAfter: listenerRequeueShort}, nil
}

func (r *ListenerReconciler) requeueOnAPIError(err error) ctrl.Result {
	if provider.IsThrottlingError(err) {
		return ctrl.Result{RequeueAfter: listenerRequeueThrottling}
	}
	return ctrl.Result{RequeueAfter: listenerRequeueError}
}

// SetupWithManager wires the controller into the manager.
func (r *ListenerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	maxConcurrent := r.MaxConcurrentReconciles
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&nlbv1.Listener{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: maxConcurrent,
		}).
		Complete(r)
}

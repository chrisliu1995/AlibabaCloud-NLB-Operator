package controller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	NLBFinalizer = "nlboperator.alibabacloud.com/finalizer"

	ConditionTypeReady = "Ready"
	ConditionTypeError = "Error"

	ReasonReconcileSuccess = "ReconcileSuccess"
	ReasonReconcileError   = "ReconcileError"
	ReasonDeletionSuccess  = "DeletionSuccess"
	ReasonDeletionError    = "DeletionError"
)

// NLBReconciler reconciles an NLB object
type NLBReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Recorder  record.EventRecorder
	NLBClient *provider.NLBClient
}

// +kubebuilder:rbac:groups=nlboperator.alibabacloud.com,resources=nlbs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nlboperator.alibabacloud.com,resources=nlbs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nlboperator.alibabacloud.com,resources=nlbs/finalizers,verbs=update

// Reconcile handles the reconciliation of NLB resources
func (r *NLBReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := klog.FromContext(ctx)
	log.Info("Reconciling NLB", "name", req.Name, "namespace", req.Namespace)

	// Fetch the NLB instance
	nlb := &nlbv1.NLB{}
	if err := r.Get(ctx, req.NamespacedName, nlb); err != nil {
		if errors.IsNotFound(err) {
			log.Info("NLB resource not found, ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get NLB resource")
		return ctrl.Result{}, err
	}

	// Check if the NLB is being deleted
	if !nlb.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, nlb)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(nlb, NLBFinalizer) {
		controllerutil.AddFinalizer(nlb, NLBFinalizer)
		if err := r.Update(ctx, nlb); err != nil {
			log.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
	}

	// Handle NLB creation or update
	return r.handleCreateOrUpdate(ctx, nlb)
}

// handleCreateOrUpdate handles the creation or update of NLB resources
func (r *NLBReconciler) handleCreateOrUpdate(ctx context.Context, nlb *nlbv1.NLB) (ctrl.Result, error) {
	log := klog.FromContext(ctx)

	// Check if LoadBalancer already exists
	if nlb.Status.LoadBalancerId == "" {
		// Create new NLB
		log.Info("Creating new NLB instance")
		lbId, err := r.NLBClient.CreateLoadBalancer(ctx, nlb)
		if err != nil {
			r.Recorder.Event(nlb, "Warning", ReasonReconcileError, fmt.Sprintf("Failed to create NLB: %v", err))
			r.updateCondition(ctx, nlb, ConditionTypeError, metav1.ConditionTrue, ReasonReconcileError, err.Error())
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}

		// Update status immediately with LoadBalancerId and initial status
		nlb.Status.LoadBalancerId = lbId
		nlb.Status.LoadBalancerStatus = "Provisioning"
		r.updateCondition(ctx, nlb, ConditionTypeReady, metav1.ConditionFalse, "Provisioning", "NLB instance is being created")
		if err := r.Status().Update(ctx, nlb); err != nil {
			log.Error(err, "Failed to update NLB status")
			return ctrl.Result{}, err
		}

		// Wait for the load balancer to become active
		if err := r.NLBClient.WaitLoadBalancerActive(ctx, lbId); err != nil {
			r.Recorder.Event(nlb, "Warning", ReasonReconcileError, fmt.Sprintf("Failed to wait for NLB to be active: %v", err))
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}

		// Get load balancer details
		lb, err := r.NLBClient.GetLoadBalancer(ctx, lbId)
		if err != nil {
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}

		// Update status with final details
		nlb.Status.DNSName = *lb.DNSName
		nlb.Status.LoadBalancerStatus = *lb.LoadBalancerStatus

		r.Recorder.Event(nlb, "Normal", ReasonReconcileSuccess, fmt.Sprintf("Successfully created NLB: %s", lbId))
		log.Info("Successfully created NLB", "loadBalancerId", lbId)
	} else {
		log.Info("NLB already exists", "loadBalancerId", nlb.Status.LoadBalancerId)

		// Verify the load balancer still exists
		lb, err := r.NLBClient.GetLoadBalancer(ctx, nlb.Status.LoadBalancerId)
		if err != nil {
			r.Recorder.Event(nlb, "Warning", ReasonReconcileError, fmt.Sprintf("Failed to get NLB: %v", err))
			r.updateCondition(ctx, nlb, ConditionTypeError, metav1.ConditionTrue, ReasonReconcileError, err.Error())
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}

		if lb == nil {
			// Load balancer was deleted externally, reset status
			log.Info("Load balancer was deleted externally, will recreate")
			nlb.Status.LoadBalancerId = ""
			nlb.Status.DNSName = ""
			nlb.Status.LoadBalancerStatus = ""
			if err := r.Status().Update(ctx, nlb); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}

		// Update status
		nlb.Status.DNSName = *lb.DNSName
		nlb.Status.LoadBalancerStatus = *lb.LoadBalancerStatus
	}

	// Handle security groups
	if err := r.handleSecurityGroups(ctx, nlb); err != nil {
		r.Recorder.Event(nlb, "Warning", ReasonReconcileError, fmt.Sprintf("Failed to handle security groups: %v", err))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Handle listeners
	if err := r.handleListeners(ctx, nlb); err != nil {
		r.Recorder.Event(nlb, "Warning", ReasonReconcileError, fmt.Sprintf("Failed to handle listeners: %v", err))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Update status condition
	r.updateCondition(ctx, nlb, ConditionTypeReady, metav1.ConditionTrue, ReasonReconcileSuccess, "NLB reconciled successfully")

	// Update status
	if err := r.Status().Update(ctx, nlb); err != nil {
		log.Error(err, "Failed to update NLB status")
		return ctrl.Result{}, err
	}

	r.Recorder.Event(nlb, "Normal", ReasonReconcileSuccess, "Successfully reconciled NLB")
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// handleDeletion handles the deletion of NLB resources
func (r *NLBReconciler) handleDeletion(ctx context.Context, nlb *nlbv1.NLB) (ctrl.Result, error) {
	log := klog.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(nlb, NLBFinalizer) {
		return ctrl.Result{}, nil
	}

	log.Info("Deleting NLB", "loadBalancerId", nlb.Status.LoadBalancerId)

	// Update status to show deletion is in progress
	if nlb.Status.LoadBalancerStatus != "Deleting" {
		nlb.Status.LoadBalancerStatus = "Deleting"
		r.updateCondition(ctx, nlb, ConditionTypeReady, metav1.ConditionFalse, "Deleting", "NLB is being deleted")
		if err := r.Status().Update(ctx, nlb); err != nil {
			log.Error(err, "Failed to update NLB status to Deleting")
			// Continue with deletion even if status update fails
		}
	}

	// Delete listeners first
	for _, listener := range nlb.Status.ListenerStatus {
		if listener.ListenerId != "" {
			log.Info("Deleting listener", "listenerId", listener.ListenerId)
			if err := r.NLBClient.DeleteListener(ctx, listener.ListenerId); err != nil {
				r.Recorder.Event(nlb, "Warning", ReasonDeletionError, fmt.Sprintf("Failed to delete listener: %v", err))
				return ctrl.Result{RequeueAfter: 30 * time.Second}, err
			}
		}
	}

	// Delete the load balancer
	if nlb.Status.LoadBalancerId != "" {
		if err := r.NLBClient.DeleteLoadBalancer(ctx, nlb.Status.LoadBalancerId); err != nil {
			r.Recorder.Event(nlb, "Warning", ReasonDeletionError, fmt.Sprintf("Failed to delete NLB: %v", err))
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}
		r.Recorder.Event(nlb, "Normal", ReasonDeletionSuccess, fmt.Sprintf("Successfully deleted NLB: %s", nlb.Status.LoadBalancerId))
		log.Info("Successfully deleted NLB", "loadBalancerId", nlb.Status.LoadBalancerId)
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(nlb, NLBFinalizer)
	if err := r.Update(ctx, nlb); err != nil {
		log.Error(err, "Failed to remove finalizer")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// handleSecurityGroups handles security group operations
func (r *NLBReconciler) handleSecurityGroups(ctx context.Context, nlb *nlbv1.NLB) error {
	if len(nlb.Spec.SecurityGroupIds) == 0 {
		return nil
	}

	log := klog.FromContext(ctx)
	log.Info("Joining security groups", "securityGroupIds", nlb.Spec.SecurityGroupIds)

	return r.NLBClient.JoinSecurityGroup(ctx, nlb.Status.LoadBalancerId, nlb.Spec.SecurityGroupIds)
}

// handleListeners handles listener creation and updates
func (r *NLBReconciler) handleListeners(ctx context.Context, nlb *nlbv1.NLB) error {
	log := klog.FromContext(ctx)

	// Track existing listeners
	existingListeners := make(map[int32]string)
	for _, ls := range nlb.Status.ListenerStatus {
		existingListeners[ls.ListenerPort] = ls.ListenerId
	}

	var newListenerStatus []nlbv1.ListenerStatus

	// Create or verify listeners
	for _, listenerSpec := range nlb.Spec.Listeners {
		if listenerId, exists := existingListeners[listenerSpec.ListenerPort]; exists {
			// Listener already exists
			log.V(5).Info("Listener already exists", "port", listenerSpec.ListenerPort, "listenerId", listenerId)
			newListenerStatus = append(newListenerStatus, nlbv1.ListenerStatus{
				ListenerPort: listenerSpec.ListenerPort,
				ListenerId:   listenerId,
				Status:       "Active",
			})
		} else {
			// Create new listener
			log.Info("Creating listener", "port", listenerSpec.ListenerPort)
			listenerId, err := r.NLBClient.CreateListener(ctx, nlb.Status.LoadBalancerId, &listenerSpec)
			if err != nil {
				return fmt.Errorf("failed to create listener on port %d: %v", listenerSpec.ListenerPort, err)
			}

			newListenerStatus = append(newListenerStatus, nlbv1.ListenerStatus{
				ListenerPort: listenerSpec.ListenerPort,
				ListenerId:   listenerId,
				Status:       "Active",
			})
		}
	}

	// Update listener status
	nlb.Status.ListenerStatus = newListenerStatus

	return nil
}

// updateCondition updates the condition of the NLB resource
func (r *NLBReconciler) updateCondition(ctx context.Context, nlb *nlbv1.NLB, conditionType string, status metav1.ConditionStatus, reason, message string) {
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: nlb.Generation,
	}

	// Find and update existing condition or append new one
	found := false
	for i, c := range nlb.Status.Conditions {
		if c.Type == conditionType {
			nlb.Status.Conditions[i] = condition
			found = true
			break
		}
	}

	if !found {
		nlb.Status.Conditions = append(nlb.Status.Conditions, condition)
	}
}

// SetupWithManager sets up the controller with the Manager
func (r *NLBReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&nlbv1.NLB{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
		}).
		Complete(r)
}

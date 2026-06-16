package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/alibabacloud-go/tea/tea"
	corev1 "k8s.io/api/core/v1"
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

// isNotFoundError reports whether err indicates the cloud NLB resource no longer exists.
// Provider layer wraps the SDK error; substring match against the SDK error code suffices.
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "ResourceNotFound")
}

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
	Scheme                  *runtime.Scheme
	Recorder                record.EventRecorder
	NLBClient               *provider.NLBClient
	MaxConcurrentReconciles int
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
// V4: Only manages NLB instance lifecycle (create/sync status). Listeners and ServerGroups are managed by NLBPool Operator.
func (r *NLBReconciler) handleCreateOrUpdate(ctx context.Context, nlb *nlbv1.NLB) (ctrl.Result, error) {
	log := klog.FromContext(ctx)

	// Check if LoadBalancer already exists
	if nlb.Status.LoadBalancerId == "" {
		// Create new NLB
		log.Info("Creating new NLB instance")
		lbId, err := r.NLBClient.CreateLoadBalancer(ctx, nlb)
		if err != nil {
			r.Recorder.Event(nlb, "Warning", ReasonReconcileError, fmt.Sprintf("Failed to create NLB: %v", err))
			r.updateCondition(nlb, ConditionTypeError, metav1.ConditionTrue, ReasonReconcileError, err.Error())
			if statusErr := r.Status().Update(ctx, nlb); statusErr != nil {
				log.Error(statusErr, "Failed to update NLB status after create error")
			}
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}

		// Update status immediately with LoadBalancerId and initial status
		nlb.Status.LoadBalancerId = lbId
		nlb.Status.LoadBalancerStatus = "Provisioning"
		r.updateCondition(nlb, ConditionTypeReady, metav1.ConditionFalse, "Provisioning", "NLB instance is being created")
		if err := r.Status().Update(ctx, nlb); err != nil {
			log.Error(err, "Failed to update NLB status")
			return ctrl.Result{}, err
		}

		r.Recorder.Event(nlb, "Normal", ReasonReconcileSuccess, fmt.Sprintf("Successfully created NLB: %s", lbId))
		log.Info("Successfully created NLB", "loadBalancerId", lbId)

		// Requeue to wait for NLB to become Active
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// NLB already exists, sync its status
	log.Info("Syncing NLB status", "loadBalancerId", nlb.Status.LoadBalancerId)

	lb, err := r.NLBClient.GetLoadBalancer(ctx, nlb.Status.LoadBalancerId)
	if err != nil {
		r.Recorder.Event(nlb, "Warning", ReasonReconcileError, fmt.Sprintf("Failed to get NLB: %v", err))
		r.updateCondition(nlb, ConditionTypeError, metav1.ConditionTrue, ReasonReconcileError, err.Error())
		if statusErr := r.Status().Update(ctx, nlb); statusErr != nil {
			log.Error(statusErr, "Failed to update NLB status after get error")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	if lb == nil {
		// Load balancer was deleted externally, reset status
		log.Info("Load balancer was deleted externally, will recreate")
		nlb.Status.LoadBalancerId = ""
		nlb.Status.DNSName = ""
		nlb.Status.LoadBalancerStatus = ""
		nlb.Status.Eips = nil
		if err := r.Status().Update(ctx, nlb); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Update status from cloud
	nlb.Status.DNSName = tea.StringValue(lb.DNSName)
	nlb.Status.LoadBalancerStatus = tea.StringValue(lb.LoadBalancerStatus)

	// Fill EIP information from ZoneMappings
	nlb.Status.Eips = nil
	if lb.ZoneMappings != nil {
		for _, zm := range lb.ZoneMappings {
			if zm == nil {
				continue
			}
			eipInfo := nlbv1.EIPInfo{
				ZoneId: tea.StringValue(zm.ZoneId),
			}
			if zm.LoadBalancerAddresses != nil && len(zm.LoadBalancerAddresses) > 0 && zm.LoadBalancerAddresses[0] != nil {
				eipInfo.IP = tea.StringValue(zm.LoadBalancerAddresses[0].PublicIPv4Address)
			}
			nlb.Status.Eips = append(nlb.Status.Eips, eipInfo)
		}
	}

	// Handle security groups
	if err := r.handleSecurityGroups(ctx, nlb); err != nil {
		r.Recorder.Event(nlb, "Warning", ReasonReconcileError, fmt.Sprintf("Failed to handle security groups: %v", err))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// If NLB is not yet Active, requeue to check again
	if tea.StringValue(lb.LoadBalancerStatus) != "Active" {
		r.updateCondition(nlb, ConditionTypeReady, metav1.ConditionFalse, "Provisioning", fmt.Sprintf("NLB status: %s", tea.StringValue(lb.LoadBalancerStatus)))
		if err := r.Status().Update(ctx, nlb); err != nil {
			log.Error(err, "Failed to update NLB status")
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// NLB is Active
	r.updateCondition(nlb, ConditionTypeReady, metav1.ConditionTrue, ReasonReconcileSuccess, "NLB reconciled successfully")

	if err := r.Status().Update(ctx, nlb); err != nil {
		log.Error(err, "Failed to update NLB status")
		return ctrl.Result{}, err
	}

	r.Recorder.Event(nlb, "Normal", ReasonReconcileSuccess, "Successfully reconciled NLB")
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// handleDeletion handles the deletion of NLB resources.
// 删除流程必须在云端真正消失之后才移除 finalizer，避免 CR 消失但云端 NLB 残留：
//  1. 若从未创建成功（LoadBalancerId 为空），直接放行；
//  2. 检查是否仍有 Listener CR 引用此 NLB，存在则等待；
//  3. 调 GetLoadBalancer 确认云端状态：
//     - NotFound  -> 移除 finalizer 完成删除；
//     - Deleting  -> Requeue 等待；
//     - 其它状态 -> 调 DeleteLoadBalancer 后 Requeue 等待下一轮 Get 再确认。
func (r *NLBReconciler) handleDeletion(ctx context.Context, nlb *nlbv1.NLB) (ctrl.Result, error) {
	log := klog.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(nlb, NLBFinalizer) {
		return ctrl.Result{}, nil
	}

	log.Info("Deleting NLB", "loadBalancerId", nlb.Status.LoadBalancerId)

	// 1. 从未创建成功，直接放行
	if nlb.Status.LoadBalancerId == "" {
		controllerutil.RemoveFinalizer(nlb, NLBFinalizer)
		if err := r.Update(ctx, nlb); err != nil {
			log.Error(err, "Failed to remove finalizer for never-created NLB")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// 2. 检查是否还有 Listener CR 引用此 NLB，若存在则等待其全部删除
	// 即使 Listener 处于 Deleting 状态，其 finalizer 可能还需要 NLB 存在才能完成 DeleteListener API 调用
	listenerList := &nlbv1.ListenerList{}
	if err := r.List(ctx, listenerList); err != nil {
		log.Error(err, "Failed to list Listeners before deleting NLB")
		return ctrl.Result{}, err
	}

	var referencingListeners int
	for _, lsn := range listenerList.Items {
		if lsn.Spec.LoadBalancerRef == nlb.Name {
			referencingListeners++
		}
	}

	if referencingListeners > 0 {
		r.Recorder.Eventf(nlb, corev1.EventTypeNormal, "WaitingForListeners",
			"Waiting for %d Listener(s) to be deleted before deleting NLB %s",
			referencingListeners, nlb.Name)
		log.Info("Waiting for Listeners to be deleted before deleting NLB",
			"nlb", nlb.Name, "referencingListeners", referencingListeners)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Update status to show deletion is in progress
	if nlb.Status.LoadBalancerStatus != "Deleting" {
		nlb.Status.LoadBalancerStatus = "Deleting"
		r.updateCondition(nlb, ConditionTypeReady, metav1.ConditionFalse, "Deleting", "NLB is being deleted")
		if err := r.Status().Update(ctx, nlb); err != nil {
			log.Error(err, "Failed to update NLB status to Deleting")
			// Continue with deletion even if status update fails
		}
	}

	// 3. 调 GetLoadBalancer 确认云端状态
	cloudNLB, err := r.NLBClient.GetLoadBalancer(ctx, nlb.Status.LoadBalancerId)
	if err != nil {
		if isNotFoundError(err) {
			// provider 层 NotFound 通常会被转为 (nil, nil)，这里是兜底
			controllerutil.RemoveFinalizer(nlb, NLBFinalizer)
			if uerr := r.Update(ctx, nlb); uerr != nil {
				log.Error(uerr, "Failed to remove finalizer after NotFound")
				return ctrl.Result{}, uerr
			}
			return ctrl.Result{}, nil
		}
		r.Recorder.Event(nlb, "Warning", ReasonDeletionError, fmt.Sprintf("Failed to get NLB during deletion: %v", err))
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
	}

	// 云端不存在（GetLoadBalancer 在 ResourceNotFound 时返回 nil, nil）
	if cloudNLB == nil {
		r.Recorder.Event(nlb, "Normal", ReasonDeletionSuccess,
			fmt.Sprintf("NLB %s already gone in cloud, removing finalizer", nlb.Status.LoadBalancerId))
		log.Info("Cloud NLB no longer exists, removing finalizer", "loadBalancerId", nlb.Status.LoadBalancerId)
		controllerutil.RemoveFinalizer(nlb, NLBFinalizer)
		if err := r.Update(ctx, nlb); err != nil {
			log.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// 云端仍存在，根据状态决定动作
	cloudStatus := tea.StringValue(cloudNLB.LoadBalancerStatus)
	if cloudStatus == "Deleting" {
		log.Info("Cloud NLB is in Deleting state, waiting", "loadBalancerId", nlb.Status.LoadBalancerId)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// 非 Deleting 状态，调用 Delete
	if err := r.NLBClient.DeleteLoadBalancer(ctx, nlb.Status.LoadBalancerId); err != nil {
		if isNotFoundError(err) {
			controllerutil.RemoveFinalizer(nlb, NLBFinalizer)
			if uerr := r.Update(ctx, nlb); uerr != nil {
				log.Error(uerr, "Failed to remove finalizer after Delete NotFound")
				return ctrl.Result{}, uerr
			}
			return ctrl.Result{}, nil
		}
		r.Recorder.Event(nlb, "Warning", ReasonDeletionError, fmt.Sprintf("Failed to delete NLB: %v", err))
		return ctrl.Result{RequeueAfter: 10 * time.Second}, err
	}

	// Delete 调用成功（可能是异步），不立即移除 finalizer，
	// 等下一轮 Reconcile 再 Get 确认云端真正消失后才移除。
	r.Recorder.Event(nlb, "Normal", ReasonDeletionSuccess,
		fmt.Sprintf("Issued DeleteLoadBalancer for NLB: %s, waiting cloud confirmation", nlb.Status.LoadBalancerId))
	log.Info("Issued DeleteLoadBalancer, waiting for cloud to disappear",
		"loadBalancerId", nlb.Status.LoadBalancerId)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
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

// updateCondition updates the condition of the NLB resource
func (r *NLBReconciler) updateCondition(nlb *nlbv1.NLB, conditionType string, status metav1.ConditionStatus, reason, message string) {
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
	maxConcurrent := r.MaxConcurrentReconciles
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&nlbv1.NLB{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: maxConcurrent,
		}).
		Complete(r)
}

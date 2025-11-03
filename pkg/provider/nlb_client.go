package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	nlbsdk "github.com/alibabacloud-go/nlb-20220430/v4/client"
	"github.com/alibabacloud-go/tea/tea"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	nlbv1 "github.com/chrisliu1995/AlibabaCloud-NLB-Operator/pkg/apis/nlboperator/v1"
)

const (
	LoadBalancerStatusActive       = "Active"
	LoadBalancerStatusProvisioning = "Provisioning"
)

// NLBClient provides methods to interact with Alibaba Cloud NLB OpenAPI
type NLBClient struct {
	client *nlbsdk.Client
}

// NewNLBClient creates a new NLBClient
func NewNLBClient(endpoint, accessKeyId, accessKeySecret, regionId string) (*NLBClient, error) {
	config := &openapi.Config{
		AccessKeyId:     tea.String(accessKeyId),
		AccessKeySecret: tea.String(accessKeySecret),
		RegionId:        tea.String(regionId),
	}
	if endpoint != "" {
		config.Endpoint = tea.String(endpoint)
	}

	client, err := nlbsdk.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create NLB client: %v", err)
	}

	return &NLBClient{client: client}, nil
}

// CreateLoadBalancer creates a new NLB instance
func (c *NLBClient) CreateLoadBalancer(ctx context.Context, nlb *nlbv1.NLB) (string, error) {
	req := &nlbsdk.CreateLoadBalancerRequest{
		LoadBalancerName: tea.String(nlb.Spec.LoadBalancerName),
		AddressType:      tea.String(nlb.Spec.AddressType),
		VpcId:            tea.String(nlb.Spec.VpcId),
		ZoneMappings:     []*nlbsdk.CreateLoadBalancerRequestZoneMappings{},
	}

	if nlb.Spec.AddressIpVersion != "" {
		req.AddressIpVersion = tea.String(nlb.Spec.AddressIpVersion)
	}

	if nlb.Spec.ResourceGroupId != "" {
		req.ResourceGroupId = tea.String(nlb.Spec.ResourceGroupId)
	}

	if nlb.Spec.BandwidthPackageId != "" {
		req.BandwidthPackageId = tea.String(nlb.Spec.BandwidthPackageId)
	}

	// Zone mappings
	for _, zm := range nlb.Spec.ZoneMappings {
		mapping := &nlbsdk.CreateLoadBalancerRequestZoneMappings{
			VSwitchId: tea.String(zm.VSwitchId),
			ZoneId:    tea.String(zm.ZoneId),
		}
		if zm.AllocationId != "" {
			mapping.AllocationId = tea.String(zm.AllocationId)
		}
		if zm.PrivateIPv4Address != "" {
			mapping.PrivateIPv4Address = tea.String(zm.PrivateIPv4Address)
		}
		req.ZoneMappings = append(req.ZoneMappings, mapping)
	}

	// Deletion protection
	if nlb.Spec.DeletionProtection != nil {
		req.DeletionProtectionConfig = &nlbsdk.CreateLoadBalancerRequestDeletionProtectionConfig{
			Enabled: tea.Bool(nlb.Spec.DeletionProtection.Enabled),
		}
		if nlb.Spec.DeletionProtection.Reason != "" {
			req.DeletionProtectionConfig.Reason = tea.String(nlb.Spec.DeletionProtection.Reason)
		}
	}

	// Modification protection
	if nlb.Spec.ModificationProtection != nil {
		req.ModificationProtectionConfig = &nlbsdk.CreateLoadBalancerRequestModificationProtectionConfig{
			Status: tea.String(nlb.Spec.ModificationProtection.Status),
		}
		if nlb.Spec.ModificationProtection.Reason != "" {
			req.ModificationProtectionConfig.Reason = tea.String(nlb.Spec.ModificationProtection.Reason)
		}
	}

	// Tags
	if len(nlb.Spec.Tags) > 0 {
		var tags []*nlbsdk.CreateLoadBalancerRequestTag
		for _, t := range nlb.Spec.Tags {
			tags = append(tags, &nlbsdk.CreateLoadBalancerRequestTag{
				Key:   tea.String(t.Key),
				Value: tea.String(t.Value),
			})
		}
		req.Tag = tags
	}

	resp, err := c.client.CreateLoadBalancer(req)
	if err != nil {
		return "", fmt.Errorf("failed to create load balancer: %v", err)
	}

	if resp == nil || resp.Body == nil || resp.Body.LoadbalancerId == nil {
		return "", fmt.Errorf("invalid response from CreateLoadBalancer API")
	}

	lbId := tea.StringValue(resp.Body.LoadbalancerId)
	klog.Infof("Successfully created NLB instance: %s, RequestId: %s", lbId, tea.StringValue(resp.Body.RequestId))

	return lbId, nil
}

// DeleteLoadBalancer deletes an NLB instance
func (c *NLBClient) DeleteLoadBalancer(ctx context.Context, lbId string) error {
	// Try to check if the load balancer exists
	// If we get temporary errors (like GetXipFailed), we'll still try to delete
	lb, err := c.GetLoadBalancer(ctx, lbId)
	if err != nil {
		// If it's a temporary error, log it but continue with deletion
		if !strings.Contains(err.Error(), "ResourceNotFound") {
			klog.Warningf("Failed to check load balancer existence (will try to delete anyway): %v", err)
		}
	}

	// If load balancer doesn't exist, it's already deleted
	if lb == nil && err == nil {
		klog.Infof("Load balancer %s not found, assuming already deleted", lbId)
		return nil
	}

	// Try to disable deletion protection
	// If the resource is not found or there's a temporary error, we'll ignore it
	protErr := c.UpdateLoadBalancerProtection(ctx, lbId, false, "")
	if protErr != nil {
		if strings.Contains(protErr.Error(), "ResourceNotFound") {
			klog.Infof("Load balancer %s not found when disabling protection, assuming already deleted", lbId)
			return nil
		}
		// For other errors (including GetXipFailed), log but continue
		klog.Warningf("Failed to disable deletion protection (will try to delete anyway): %v", protErr)
	}

	req := &nlbsdk.DeleteLoadBalancerRequest{
		LoadBalancerId: tea.String(lbId),
	}

	resp, err := c.client.DeleteLoadBalancer(req)
	if err != nil {
		// If resource not found, consider it as already deleted
		if strings.Contains(err.Error(), "ResourceNotFound") {
			klog.Infof("Load balancer %s not found, assuming already deleted", lbId)
			return nil
		}
		return fmt.Errorf("failed to delete load balancer: %v", err)
	}

	if resp == nil || resp.Body == nil {
		return fmt.Errorf("invalid response from DeleteLoadBalancer API")
	}

	klog.Infof("Successfully deleted NLB instance: %s, RequestId: %s", lbId, tea.StringValue(resp.Body.RequestId))

	// Wait for the job to complete
	if resp.Body.JobId != nil {
		return c.waitJobFinish(tea.StringValue(resp.Body.JobId))
	}

	return nil
}

// GetLoadBalancer retrieves NLB instance details
func (c *NLBClient) GetLoadBalancer(ctx context.Context, lbId string) (*nlbsdk.GetLoadBalancerAttributeResponseBody, error) {
	req := &nlbsdk.GetLoadBalancerAttributeRequest{
		LoadBalancerId: tea.String(lbId),
	}

	resp, err := c.client.GetLoadBalancerAttribute(req)
	if err != nil {
		// Resource not found is not an error, return nil
		if strings.Contains(err.Error(), "ResourceNotFound") {
			return nil, nil
		}
		// For GetXipFailed or other temporary errors, return the error for retry
		return nil, fmt.Errorf("failed to get load balancer: %v", err)
	}

	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("invalid response from GetLoadBalancerAttribute API")
	}

	return resp.Body, nil
}

// UpdateLoadBalancerProtection updates deletion protection for NLB
func (c *NLBClient) UpdateLoadBalancerProtection(ctx context.Context, lbId string, enabled bool, reason string) error {
	req := &nlbsdk.UpdateLoadBalancerProtectionRequest{
		LoadBalancerId:            tea.String(lbId),
		DeletionProtectionEnabled: tea.Bool(enabled),
	}

	if reason != "" {
		req.DeletionProtectionReason = tea.String(reason)
	}

	resp, err := c.client.UpdateLoadBalancerProtection(req)
	if err != nil {
		return fmt.Errorf("failed to update load balancer protection: %v", err)
	}

	if resp == nil || resp.Body == nil {
		return fmt.Errorf("invalid response from UpdateLoadBalancerProtection API")
	}

	klog.V(5).Infof("Successfully updated NLB protection: %s, RequestId: %s", lbId, tea.StringValue(resp.Body.RequestId))
	return nil
}

// JoinSecurityGroup adds security groups to NLB
func (c *NLBClient) JoinSecurityGroup(ctx context.Context, lbId string, securityGroupIds []string) error {
	if len(securityGroupIds) == 0 {
		return nil
	}

	req := &nlbsdk.LoadBalancerJoinSecurityGroupRequest{
		LoadBalancerId:   tea.String(lbId),
		SecurityGroupIds: tea.StringSlice(securityGroupIds),
	}

	resp, err := c.client.LoadBalancerJoinSecurityGroup(req)
	if err != nil {
		return fmt.Errorf("failed to join security group: %v", err)
	}

	if resp == nil || resp.Body == nil {
		return fmt.Errorf("invalid response from LoadBalancerJoinSecurityGroup API")
	}

	klog.V(5).Infof("Successfully joined security groups for NLB: %s, RequestId: %s", lbId, tea.StringValue(resp.Body.RequestId))

	// Wait for the job to complete
	if resp.Body.JobId != nil {
		return c.waitJobFinish(tea.StringValue(resp.Body.JobId))
	}

	return nil
}

// CreateListener creates a listener for the NLB instance
func (c *NLBClient) CreateListener(ctx context.Context, lbId string, listener *nlbv1.ListenerSpec) (string, error) {
	req := &nlbsdk.CreateListenerRequest{
		LoadBalancerId:   tea.String(lbId),
		ListenerProtocol: tea.String(listener.ListenerProtocol),
		ListenerPort:     tea.Int32(listener.ListenerPort),
		ServerGroupId:    tea.String(listener.ServerGroupId),
	}

	if listener.ListenerDescription != "" {
		req.ListenerDescription = tea.String(listener.ListenerDescription)
	}

	if listener.IdleTimeout > 0 {
		req.IdleTimeout = tea.Int32(listener.IdleTimeout)
	}

	if listener.SecurityPolicyId != "" {
		req.SecurityPolicyId = tea.String(listener.SecurityPolicyId)
	}

	if len(listener.CertificateIds) > 0 {
		req.CertificateIds = tea.StringSlice(listener.CertificateIds)
	}

	if len(listener.CaCertificateIds) > 0 {
		req.CaCertificateIds = tea.StringSlice(listener.CaCertificateIds)
	}

	if listener.CaEnabled != nil {
		req.CaEnabled = listener.CaEnabled
	}

	if listener.ProxyProtocolEnabled != nil {
		req.ProxyProtocolEnabled = listener.ProxyProtocolEnabled
	}

	resp, err := c.client.CreateListener(req)
	if err != nil {
		return "", fmt.Errorf("failed to create listener: %v", err)
	}

	if resp == nil || resp.Body == nil || resp.Body.ListenerId == nil {
		return "", fmt.Errorf("invalid response from CreateListener API")
	}

	listenerId := tea.StringValue(resp.Body.ListenerId)
	klog.Infof("Successfully created listener: %s for NLB: %s, RequestId: %s", listenerId, lbId, tea.StringValue(resp.Body.RequestId))

	return listenerId, nil
}

// DeleteListener deletes a listener
func (c *NLBClient) DeleteListener(ctx context.Context, listenerId string) error {
	req := &nlbsdk.DeleteListenerRequest{
		ListenerId: tea.String(listenerId),
	}

	resp, err := c.client.DeleteListener(req)
	if err != nil {
		// If resource not found, consider it as already deleted
		if strings.Contains(err.Error(), "ResourceNotFound") {
			klog.Infof("Listener %s not found, assuming already deleted", listenerId)
			return nil
		}
		return fmt.Errorf("failed to delete listener: %v", err)
	}

	if resp == nil || resp.Body == nil {
		return fmt.Errorf("invalid response from DeleteListener API")
	}

	klog.Infof("Successfully deleted listener: %s, RequestId: %s", listenerId, tea.StringValue(resp.Body.RequestId))

	// Wait for the job to complete
	if resp.Body.JobId != nil {
		return c.waitJobFinish(tea.StringValue(resp.Body.JobId))
	}

	return nil
}

// waitJobFinish waits for an async job to complete
func (c *NLBClient) waitJobFinish(jobId string) error {
	return wait.PollImmediate(3*time.Second, 3*time.Minute, func() (bool, error) {
		req := &nlbsdk.GetJobStatusRequest{
			JobId: tea.String(jobId),
		}

		resp, err := c.client.GetJobStatus(req)
		if err != nil {
			return false, fmt.Errorf("failed to get job status: %v", err)
		}

		if resp == nil || resp.Body == nil {
			return false, fmt.Errorf("invalid response from GetJobStatus API")
		}

		status := tea.StringValue(resp.Body.Status)
		switch status {
		case "Succeeded":
			klog.V(5).Infof("Job %s succeeded", jobId)
			return true, nil
		case "Failed":
			return false, fmt.Errorf("job %s failed", jobId)
		default:
			klog.V(5).Infof("Job %s status: %s", jobId, status)
			return false, nil
		}
	})
}

// WaitLoadBalancerActive waits for the load balancer to become active
func (c *NLBClient) WaitLoadBalancerActive(ctx context.Context, lbId string) error {
	return wait.PollImmediate(10*time.Second, 5*time.Minute, func() (bool, error) {
		lb, err := c.GetLoadBalancer(ctx, lbId)
		if err != nil {
			return false, err
		}

		if lb == nil {
			return false, fmt.Errorf("load balancer %s not found", lbId)
		}

		status := tea.StringValue(lb.LoadBalancerStatus)
		if status == LoadBalancerStatusActive {
			klog.V(5).Infof("Load balancer %s is active", lbId)
			return true, nil
		}

		klog.V(5).Infof("Waiting for load balancer %s to be active, current status: %s", lbId, status)
		return false, nil
	})
}

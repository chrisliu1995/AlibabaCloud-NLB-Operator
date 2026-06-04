package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	nlbsdk "github.com/alibabacloud-go/nlb-20220430/v4/client"
	"github.com/alibabacloud-go/tea/tea"
	"k8s.io/klog/v2"

	nlbv1 "github.com/chrisliu1995/AlibabaCloud-NLB-Operator/pkg/apis/nlboperator/v1"
)

// ErrLocalRateLimited is returned when a local (in-process) rate limiter
// rejects an outbound API call before it is sent to the cloud. Controllers
// should treat this as a fast, cheap signal to requeue with a short delay.
var ErrLocalRateLimited = fmt.Errorf("local rate limited: GetListenerAttribute")

// ErrCreateListenerRateLimited is returned when the CreateListener local rate
// limiter rejects the call.
var ErrCreateListenerRateLimited = fmt.Errorf("local rate limited: CreateListener")

// IsLocalRateLimited reports whether err is (or wraps) any local rate limit error.
func IsLocalRateLimited(err error) bool {
	return errors.Is(err, ErrLocalRateLimited) || errors.Is(err, ErrCreateListenerRateLimited)
}

// ServerGroupAttribute is a thin abstraction over the cloud server group attributes
// that are relevant for the controller.
type ServerGroupAttribute struct {
	ServerGroupId     string
	ServerGroupName   string
	ServerGroupStatus string
	VpcId             string
}

// ListenerAttribute is a thin abstraction over the cloud listener attributes
// that are relevant for the controller.
type ListenerAttribute struct {
	ListenerId       string
	ListenerStatus   string
	ListenerPort     int32
	ListenerProtocol string
	LoadBalancerId   string
	ServerGroupId    string
}

// IsNotFoundError returns true when the underlying Aliyun OpenAPI error indicates
// that the requested resource does not exist.
func IsNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "ResourceNotFound") ||
		strings.Contains(msg, "ServerNotFound") ||
		strings.Contains(msg, "InvalidListenerId.NotFound") ||
		strings.Contains(msg, "InvalidServerGroupId.NotFound") ||
		strings.Contains(msg, "NotFound")
}

// IsThrottlingError returns true when the underlying Aliyun OpenAPI error
// indicates the request was throttled.
func IsThrottlingError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Throttling") ||
		strings.Contains(msg, "RequestLimitExceeded") ||
		strings.Contains(msg, "ServiceUnavailable")
}

// IsResourceAlreadyExistsError returns true when the underlying Aliyun OpenAPI error
// indicates that the resource already exists (used for optimistic create fallback).
func IsResourceAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "ResourceAlreadyExists") ||
		strings.Contains(msg, "ListenerAlreadyExists") ||
		strings.Contains(msg, "DuplicateListener") ||
		strings.Contains(msg, "ResourceInUse")
}

// CreateServerGroup creates a backend server group on Alibaba Cloud NLB.
func (c *NLBClient) CreateServerGroup(ctx context.Context, sg *nlbv1.ServerGroup) (string, error) {
	req := &nlbsdk.CreateServerGroupRequest{
		ServerGroupName: tea.String(sg.Spec.ServerGroupName),
		VpcId:           tea.String(sg.Spec.VpcId),
		ServerGroupType: tea.String(sg.Spec.ServerGroupType),
		Protocol:        tea.String(sg.Spec.Protocol),
	}

	if sg.Spec.Region != "" {
		req.RegionId = tea.String(sg.Spec.Region)
	}

	if sg.Spec.Scheduler != "" {
		req.Scheduler = tea.String(sg.Spec.Scheduler)
	}

	if sg.Spec.HealthCheck != nil {
		hc := &nlbsdk.CreateServerGroupRequestHealthCheckConfig{
			HealthCheckEnabled: tea.Bool(sg.Spec.HealthCheck.Enabled),
		}
		if sg.Spec.HealthCheck.HealthCheckConnectPort > 0 {
			hc.HealthCheckConnectPort = tea.Int32(sg.Spec.HealthCheck.HealthCheckConnectPort)
		}
		if sg.Spec.HealthCheck.HealthCheckConnectTimeout > 0 {
			hc.HealthCheckConnectTimeout = tea.Int32(sg.Spec.HealthCheck.HealthCheckConnectTimeout)
		}
		if sg.Spec.HealthCheck.HealthyThreshold > 0 {
			hc.HealthyThreshold = tea.Int32(sg.Spec.HealthCheck.HealthyThreshold)
		}
		if sg.Spec.HealthCheck.UnhealthyThreshold > 0 {
			hc.UnhealthyThreshold = tea.Int32(sg.Spec.HealthCheck.UnhealthyThreshold)
		}
		if sg.Spec.HealthCheck.HealthCheckInterval > 0 {
			hc.HealthCheckInterval = tea.Int32(sg.Spec.HealthCheck.HealthCheckInterval)
		}
		req.HealthCheckConfig = hc
	}

	// ClientToken bound to CR UID to ensure idempotent create even on retries.
	if sg.UID != "" {
		req.ClientToken = tea.String(fmt.Sprintf("sg-%s", string(sg.UID)))
	}

	resp, err := c.client.CreateServerGroup(req)
	if err != nil {
		return "", fmt.Errorf("failed to create server group: %v", err)
	}
	if resp == nil || resp.Body == nil || resp.Body.ServerGroupId == nil {
		return "", fmt.Errorf("invalid response from CreateServerGroup API")
	}

	sgId := tea.StringValue(resp.Body.ServerGroupId)
	klog.Infof("Successfully created NLB ServerGroup: %s, name: %s, RequestId: %s",
		sgId, sg.Spec.ServerGroupName, tea.StringValue(resp.Body.RequestId))
	return sgId, nil
}

// GetServerGroupAttribute fetches a server group's current attributes by ID.
// Returns (nil, nil) when the server group does not exist on the cloud.
func (c *NLBClient) GetServerGroupAttribute(ctx context.Context, sgId string) (*ServerGroupAttribute, error) {
	if sgId == "" {
		return nil, nil
	}

	req := &nlbsdk.ListServerGroupsRequest{
		ServerGroupIds: tea.StringSlice([]string{sgId}),
	}
	resp, err := c.client.ListServerGroups(req)
	if err != nil {
		if IsNotFoundError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list server groups by id %s: %v", sgId, err)
	}
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("invalid response from ListServerGroups API")
	}
	for _, sg := range resp.Body.ServerGroups {
		if sg == nil {
			continue
		}
		if tea.StringValue(sg.ServerGroupId) == sgId {
			return &ServerGroupAttribute{
				ServerGroupId:     tea.StringValue(sg.ServerGroupId),
				ServerGroupName:   tea.StringValue(sg.ServerGroupName),
				ServerGroupStatus: tea.StringValue(sg.ServerGroupStatus),
				VpcId:             tea.StringValue(sg.VpcId),
			}, nil
		}
	}
	return nil, nil
}

// DeleteServerGroup deletes a backend server group by ID.
// Returns nil if the server group does not exist (already deleted).
func (c *NLBClient) DeleteServerGroup(ctx context.Context, sgId string) error {
	if sgId == "" {
		return nil
	}
	req := &nlbsdk.DeleteServerGroupRequest{
		ServerGroupId: tea.String(sgId),
	}
	resp, err := c.client.DeleteServerGroup(req)
	if err != nil {
		if IsNotFoundError(err) {
			klog.Infof("ServerGroup %s not found, assuming already deleted", sgId)
			return nil
		}
		return fmt.Errorf("failed to delete server group %s: %v", sgId, err)
	}
	if resp == nil || resp.Body == nil {
		return fmt.Errorf("invalid response from DeleteServerGroup API")
	}
	klog.Infof("Successfully called DeleteServerGroup: %s, RequestId: %s",
		sgId, tea.StringValue(resp.Body.RequestId))
	return nil
}

// ListServerGroups looks up a server group ID by VPC and name (idempotency check).
// Returns "" when no matching server group exists.
func (c *NLBClient) ListServerGroups(ctx context.Context, vpcId, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	req := &nlbsdk.ListServerGroupsRequest{
		ServerGroupNames: tea.StringSlice([]string{name}),
	}
	if vpcId != "" {
		req.VpcId = tea.String(vpcId)
	}

	for {
		resp, err := c.client.ListServerGroups(req)
		if err != nil {
			if IsNotFoundError(err) {
				return "", nil
			}
			return "", fmt.Errorf("failed to list server groups by name %s: %v", name, err)
		}
		if resp == nil || resp.Body == nil {
			return "", fmt.Errorf("invalid response from ListServerGroups API")
		}
		for _, sg := range resp.Body.ServerGroups {
			if sg == nil {
				continue
			}
			if tea.StringValue(sg.ServerGroupName) != name {
				continue
			}
			if vpcId != "" && tea.StringValue(sg.VpcId) != vpcId {
				continue
			}
			return tea.StringValue(sg.ServerGroupId), nil
		}
		next := tea.StringValue(resp.Body.NextToken)
		if next == "" {
			return "", nil
		}
		req.NextToken = tea.String(next)
	}
}

// CreateNLBListener creates a TCP/UDP/TCPSSL listener bound to the given NLB and ServerGroup.
func (c *NLBClient) CreateNLBListener(ctx context.Context, nlbId, sgId string, port int32, protocol string) (string, error) {
	if c.CreateListenerLimiter != nil && !c.CreateListenerLimiter.Allow() {
		return "", ErrCreateListenerRateLimited
	}
	if nlbId == "" || sgId == "" {
		return "", fmt.Errorf("nlbId and serverGroupId are required to create listener")
	}
	req := &nlbsdk.CreateListenerRequest{
		LoadBalancerId:   tea.String(nlbId),
		ListenerProtocol: tea.String(protocol),
		ListenerPort:     tea.Int32(port),
		ServerGroupId:    tea.String(sgId),
	}

	// ClientToken bound to business key (NLB ID + Port + Protocol) for idempotent create.
	// Do NOT bind to CR UID as CR may be recreated.
	clientToken := fmt.Sprintf("lsn-%s-%d-%s", nlbId, port, protocol)
	if len(clientToken) > 64 {
		h := sha256.Sum256([]byte(clientToken))
		clientToken = hex.EncodeToString(h[:])[:64]
	}
	req.ClientToken = tea.String(clientToken)
	req.DryRun = tea.Bool(false)

	resp, err := c.client.CreateListener(req)
	if err != nil {
		return "", fmt.Errorf("failed to create listener (nlb=%s, port=%d, protocol=%s): %v",
			nlbId, port, protocol, err)
	}
	if resp == nil || resp.Body == nil || resp.Body.ListenerId == nil {
		return "", fmt.Errorf("invalid response from CreateListener API")
	}

	listenerId := tea.StringValue(resp.Body.ListenerId)
	klog.Infof("Successfully created NLB Listener: %s (nlb=%s, port=%d, protocol=%s), RequestId: %s",
		listenerId, nlbId, port, protocol, tea.StringValue(resp.Body.RequestId))
	return listenerId, nil
}

// GetListenerAttribute fetches a listener's current attributes by ID.
// Returns (nil, nil) when the listener does not exist on the cloud.
// Returns (nil, ErrLocalRateLimited) when the local token bucket rejects
// the call before any SDK request is issued.
func (c *NLBClient) GetListenerAttribute(ctx context.Context, listenerId string) (*ListenerAttribute, error) {
	if listenerId == "" {
		return nil, nil
	}
	if c.GetListenerLimiter != nil && !c.GetListenerLimiter.Allow() {
		return nil, ErrLocalRateLimited
	}
	req := &nlbsdk.GetListenerAttributeRequest{
		ListenerId: tea.String(listenerId),
	}
	resp, err := c.client.GetListenerAttribute(req)
	if err != nil {
		if IsNotFoundError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get listener %s: %v", listenerId, err)
	}
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("invalid response from GetListenerAttribute API")
	}
	body := resp.Body
	return &ListenerAttribute{
		ListenerId:       tea.StringValue(body.ListenerId),
		ListenerStatus:   tea.StringValue(body.ListenerStatus),
		ListenerPort:     tea.Int32Value(body.ListenerPort),
		ListenerProtocol: tea.StringValue(body.ListenerProtocol),
		LoadBalancerId:   tea.StringValue(body.LoadBalancerId),
		ServerGroupId:    tea.StringValue(body.ServerGroupId),
	}, nil
}

// DeleteNLBListener deletes a listener by ID. Returns nil if already deleted.
func (c *NLBClient) DeleteNLBListener(ctx context.Context, listenerId string) error {
	if listenerId == "" {
		return nil
	}
	req := &nlbsdk.DeleteListenerRequest{
		ListenerId: tea.String(listenerId),
	}
	resp, err := c.client.DeleteListener(req)
	if err != nil {
		if IsNotFoundError(err) {
			klog.Infof("Listener %s not found, assuming already deleted", listenerId)
			return nil
		}
		return fmt.Errorf("failed to delete listener %s: %v", listenerId, err)
	}
	if resp == nil || resp.Body == nil {
		return fmt.Errorf("invalid response from DeleteListener API")
	}
	klog.Infof("Successfully called DeleteListener: %s, RequestId: %s",
		listenerId, tea.StringValue(resp.Body.RequestId))
	return nil
}

// ListListeners looks up a listener ID by NLB and listener port (idempotency check).
// Returns "" when no matching listener exists.
func (c *NLBClient) ListListeners(ctx context.Context, nlbId string, port int32) (string, error) {
	if nlbId == "" {
		return "", nil
	}
	req := &nlbsdk.ListListenersRequest{
		LoadBalancerIds: tea.StringSlice([]string{nlbId}),
	}

	for {
		resp, err := c.client.ListListeners(req)
		if err != nil {
			if IsNotFoundError(err) {
				return "", nil
			}
			return "", fmt.Errorf("failed to list listeners on nlb %s: %v", nlbId, err)
		}
		if resp == nil || resp.Body == nil {
			return "", fmt.Errorf("invalid response from ListListeners API")
		}
		for _, lsn := range resp.Body.Listeners {
			if lsn == nil {
				continue
			}
			if tea.StringValue(lsn.LoadBalancerId) != nlbId {
				continue
			}
			if tea.Int32Value(lsn.ListenerPort) == port {
				return tea.StringValue(lsn.ListenerId), nil
			}
		}
		next := tea.StringValue(resp.Body.NextToken)
		if next == "" {
			return "", nil
		}
		req.NextToken = tea.String(next)
	}
}

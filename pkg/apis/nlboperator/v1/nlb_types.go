package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	SchemeBuilder.Register(&NLB{}, &NLBList{})
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// NLB is the Schema for the nlbs API
type NLB struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NLBSpec   `json:"spec,omitempty"`
	Status NLBStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// NLBList contains a list of NLB
type NLBList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NLB `json:"items"`
}

// NLBSpec defines the desired state of NLB
type NLBSpec struct {
	// LoadBalancerName is the name of the NLB instance
	// +optional
	LoadBalancerName string `json:"loadBalancerName,omitempty"`

	// AddressType is the network type of the NLB instance
	// Valid values: Internet, Intranet
	// +kubebuilder:validation:Enum=Internet;Intranet
	// +kubebuilder:default=Internet
	AddressType string `json:"addressType"`

	// AddressIpVersion is the IP version of the NLB instance
	// Valid values: ipv4, DualStack
	// +kubebuilder:validation:Enum=ipv4;DualStack
	// +kubebuilder:default=ipv4
	// +optional
	AddressIpVersion string `json:"addressIpVersion,omitempty"`

	// VpcId is the VPC ID where the NLB instance resides
	VpcId string `json:"vpcId"`

	// ZoneMappings specifies the zones and vSwitches for the NLB instance
	// +kubebuilder:validation:MinItems=2
	ZoneMappings []ZoneMapping `json:"zoneMappings"`

	// ResourceGroupId is the resource group ID
	// +optional
	ResourceGroupId string `json:"resourceGroupId,omitempty"`

	// SecurityGroupIds specifies the security group IDs
	// +optional
	SecurityGroupIds []string `json:"securityGroupIds,omitempty"`

	// BandwidthPackageId is the bandwidth package ID for Internet NLB
	// +optional
	BandwidthPackageId string `json:"bandwidthPackageId,omitempty"`

	// DeletionProtection specifies whether to enable deletion protection
	// +optional
	DeletionProtection *DeletionProtectionConfig `json:"deletionProtection,omitempty"`

	// ModificationProtection specifies whether to enable modification protection
	// +optional
	ModificationProtection *ModificationProtectionConfig `json:"modificationProtection,omitempty"`

	// Tags are the tags to be added to the NLB instance
	// +optional
	Tags []Tag `json:"tags,omitempty"`

	// Listeners specifies the listeners for the NLB instance
	// +optional
	Listeners []ListenerSpec `json:"listeners,omitempty"`
}

// ZoneMapping defines the zone and vSwitch configuration
type ZoneMapping struct {
	// ZoneId is the zone ID
	ZoneId string `json:"zoneId"`

	// VSwitchId is the vSwitch ID
	VSwitchId string `json:"vSwitchId"`

	// AllocationId is the EIP allocation ID for Internet NLB
	// +optional
	AllocationId string `json:"allocationId,omitempty"`

	// PrivateIPv4Address is the private IP address
	// +optional
	PrivateIPv4Address string `json:"privateIPv4Address,omitempty"`
}

// DeletionProtectionConfig defines the deletion protection configuration
type DeletionProtectionConfig struct {
	// Enabled specifies whether deletion protection is enabled
	Enabled bool `json:"enabled"`

	// Reason is the reason for enabling deletion protection
	// +optional
	Reason string `json:"reason,omitempty"`
}

// ModificationProtectionConfig defines the modification protection configuration
type ModificationProtectionConfig struct {
	// Status specifies the modification protection status
	// Valid values: ConsoleProtection, NonProtection
	// +kubebuilder:validation:Enum=ConsoleProtection;NonProtection
	Status string `json:"status"`

	// Reason is the reason for modification protection
	// +optional
	Reason string `json:"reason,omitempty"`
}

// Tag defines a tag for the NLB instance
type Tag struct {
	// Key is the tag key
	Key string `json:"key"`

	// Value is the tag value
	Value string `json:"value"`
}

// ListenerSpec defines the listener configuration
type ListenerSpec struct {
	// ListenerProtocol is the protocol of the listener
	// Valid values: TCP, UDP, TCPSSL
	// +kubebuilder:validation:Enum=TCP;UDP;TCPSSL
	ListenerProtocol string `json:"listenerProtocol"`

	// ListenerPort is the listening port
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	ListenerPort int32 `json:"listenerPort"`

	// ServerGroupId is the backend server group ID
	ServerGroupId string `json:"serverGroupId"`

	// ListenerDescription is the description of the listener
	// +optional
	ListenerDescription string `json:"listenerDescription,omitempty"`

	// IdleTimeout is the idle connection timeout in seconds (1-900)
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=900
	// +kubebuilder:default=900
	// +optional
	IdleTimeout int32 `json:"idleTimeout,omitempty"`

	// SecurityPolicyId is the security policy ID for TCPSSL
	// +optional
	SecurityPolicyId string `json:"securityPolicyId,omitempty"`

	// CertificateIds are the certificate IDs for TCPSSL
	// +optional
	CertificateIds []string `json:"certificateIds,omitempty"`

	// CaCertificateIds are the CA certificate IDs
	// +optional
	CaCertificateIds []string `json:"caCertificateIds,omitempty"`

	// CaEnabled specifies whether CA verification is enabled
	// +optional
	CaEnabled *bool `json:"caEnabled,omitempty"`

	// ProxyProtocolEnabled specifies whether proxy protocol is enabled
	// +optional
	ProxyProtocolEnabled *bool `json:"proxyProtocolEnabled,omitempty"`
}

// NLBStatus defines the observed state of NLB
type NLBStatus struct {
	// LoadBalancerId is the ID of the NLB instance
	// +optional
	LoadBalancerId string `json:"loadBalancerId,omitempty"`

	// DNSName is the DNS name of the NLB instance
	// +optional
	DNSName string `json:"dnsName,omitempty"`

	// LoadBalancerStatus is the status of the NLB instance
	// Valid values: Provisioning, Active, Configuring, CreateFailed
	// +optional
	LoadBalancerStatus string `json:"loadBalancerStatus,omitempty"`

	// Conditions represent the latest available observations of the NLB's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ListenerStatus contains the status of each listener
	// +optional
	ListenerStatus []ListenerStatus `json:"listenerStatus,omitempty"`
}

// ListenerStatus defines the status of a listener
type ListenerStatus struct {
	// ListenerPort is the listening port
	ListenerPort int32 `json:"listenerPort"`

	// ListenerId is the ID of the listener
	// +optional
	ListenerId string `json:"listenerId,omitempty"`

	// Status is the status of the listener
	// +optional
	Status string `json:"status,omitempty"`
}

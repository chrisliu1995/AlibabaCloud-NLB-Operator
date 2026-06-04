package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

func init() {
	SchemeBuilder.Register(&ServerGroup{}, &ServerGroupList{})
}

// ServerGroupPhase 定义 ServerGroup 的状态
type ServerGroupPhase string

const (
	ServerGroupPending  ServerGroupPhase = "Pending"
	ServerGroupCreating ServerGroupPhase = "Creating"
	ServerGroupActive   ServerGroupPhase = "Active"
	ServerGroupDeleting ServerGroupPhase = "Deleting"
	ServerGroupFailed   ServerGroupPhase = "Failed"
)

// ServerGroupFinalizer 用于清理云端 ServerGroup 资源
const ServerGroupFinalizer = "nlboperator.alibabacloud.com/servergroup-finalizer"

// ServerGroupSpec defines the desired state of ServerGroup
type ServerGroupSpec struct {
	// Region 阿里云区域
	Region string `json:"region"`
	// VpcId VPC ID
	VpcId string `json:"vpcId"`
	// ServerGroupName 云端 ServerGroup 名称
	ServerGroupName string `json:"serverGroupName"`
	// ServerGroupType 类型: Ip / Instance
	ServerGroupType string `json:"serverGroupType"`
	// Protocol SG级别协议: TCP / UDP / TCPSSL
	Protocol string `json:"protocol"`
	// Scheduler 调度算法: Wrr / Rr / Sch / Tch
	// +optional
	Scheduler string `json:"scheduler,omitempty"`
	// HealthCheck 健康检查配置
	// +optional
	HealthCheck *HealthCheckConfig `json:"healthCheck,omitempty"`
}

// HealthCheckConfig 健康检查配置
type HealthCheckConfig struct {
	// Enabled 是否启用健康检查
	Enabled bool `json:"enabled"`
	// HealthCheckConnectPort 健康检查端口
	// +optional
	HealthCheckConnectPort int32 `json:"healthCheckConnectPort,omitempty"`
	// HealthCheckConnectTimeout 超时时间(秒)
	// +optional
	HealthCheckConnectTimeout int32 `json:"healthCheckConnectTimeout,omitempty"`
	// HealthyThreshold 健康判定阈值
	// +optional
	HealthyThreshold int32 `json:"healthyThreshold,omitempty"`
	// UnhealthyThreshold 不健康判定阈值
	// +optional
	UnhealthyThreshold int32 `json:"unhealthyThreshold,omitempty"`
	// HealthCheckInterval 检查间隔(秒)
	// +optional
	HealthCheckInterval int32 `json:"healthCheckInterval,omitempty"`
}

// ServerGroupStatus defines the observed state of ServerGroup
type ServerGroupStatus struct {
	// ServerGroupId 云端 ServerGroup ID
	// +optional
	ServerGroupId string `json:"serverGroupId,omitempty"`
	// Phase 当前阶段
	// +optional
	Phase ServerGroupPhase `json:"phase,omitempty"`
	// Message 附加诊断信息
	// +optional
	Message string `json:"message,omitempty"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="ServerGroupId",type=string,JSONPath=`.status.serverGroupId`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:resource:shortName=sg

// ServerGroup is the Schema for the servergroups API
type ServerGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ServerGroupSpec   `json:"spec,omitempty"`
	Status ServerGroupStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true

// ServerGroupList contains a list of ServerGroup
type ServerGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ServerGroup `json:"items"`
}

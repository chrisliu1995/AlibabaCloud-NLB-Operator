package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

func init() {
	SchemeBuilder.Register(&Listener{}, &ListenerList{})
}

// ListenerPhase 定义 Listener 的状态
type ListenerPhase string

const (
	ListenerPending  ListenerPhase = "Pending"
	ListenerCreating ListenerPhase = "Creating"
	ListenerRunning  ListenerPhase = "Running"
	ListenerDeleting ListenerPhase = "Deleting"
	ListenerFailed   ListenerPhase = "Failed"
)

// ListenerFinalizer 用于清理云端 Listener 资源
const ListenerFinalizer = "nlboperator.alibabacloud.com/listener-finalizer"

// ListenerSpec defines the desired state of Listener
type ListenerSpec struct {
	// Region 阿里云区域
	Region string `json:"region"`
	// LoadBalancerRef 引用 NLB CR name (同namespace)
	LoadBalancerRef string `json:"loadBalancerRef"`
	// ListenerPort NLB 上的监听端口
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	ListenerPort int32 `json:"listenerPort"`
	// ListenerProtocol 监听协议: TCP / UDP / TCPSSL
	// +kubebuilder:validation:Enum=TCP;UDP;TCPSSL
	ListenerProtocol string `json:"listenerProtocol"`
	// ServerGroupRef 引用 ServerGroup CR name (跨 NLB 共享)
	ServerGroupRef string `json:"serverGroupRef"`
}

// ListenerStatus defines the observed state of Listener
type ListenerStatus struct {
	// ListenerId 云端 Listener ID
	// +optional
	ListenerId string `json:"listenerId,omitempty"`
	// Phase 当前阶段
	// +optional
	Phase ListenerPhase `json:"phase,omitempty"`
	// Message 附加诊断信息
	// +optional
	Message string `json:"message,omitempty"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Port",type=integer,JSONPath=`.spec.listenerPort`
// +kubebuilder:printcolumn:name="Protocol",type=string,JSONPath=`.spec.listenerProtocol`
// +kubebuilder:printcolumn:name="ListenerId",type=string,JSONPath=`.status.listenerId`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:resource:shortName=lsn

// Listener is the Schema for the listeners API
type Listener struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ListenerSpec   `json:"spec,omitempty"`
	Status ListenerStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true

// ListenerList contains a list of Listener
type ListenerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Listener `json:"items"`
}

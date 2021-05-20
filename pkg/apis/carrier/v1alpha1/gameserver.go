// Copyright 2021 The OCGI Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// GameServer is the data structure for a GameServer resource.
type GameServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GameServerSpec   `json:"spec"`
	Status GameServerStatus `json:"status"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// GameServerList is a list of GameServer resources
type GameServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []GameServer `json:"items"`
}

// GameServerTemplateSpec is a template for GameServers
type GameServerTemplateSpec struct {
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              GameServerSpec `json:"spec"`
}

// GameServerSpec is the spec for a GameServer resource.
type GameServerSpec struct {
	// Ports are the array of ports that can be exposed via the GameServer.
	Ports []GameServerPort `json:"ports"`
	// Scheduling strategy. Defaults to "MostAllocated".
	Scheduling SchedulingStrategy `json:"scheduling,omitempty"`
	// SdkServer specifies parameters for the Carrier SDK Server sidecar container.
	SdkServer SdkServer `json:"sdkServer,omitempty"`
	// Health configures health checking.
	Health Health `json:"health,omitempty"`
	// Template describes the Pod that will be created for the GameServer.
	Template corev1.PodTemplateSpec `json:"template"`

	// Constraints describes the constraints of GameServer.
	// This filed may be added or changed by controller or manually.
	// If anyone of them is `NotInService` and Effective is `True`,
	// GameServer container should not continue serving and should set `Retired` true, HasPlayer false
	// to conditions when it has closed the connection to players.
	Constraints []Constraint `json:"constraints,omitempty"`

	// If specified, all readiness gates will be evaluated for GameServer readiness.
	// A GameServer is ready when all its pod are ready AND
	// all conditions specified in the readiness gates have status equal to "True"
	// +optional
	ReadinessGates []string `json:"readinessGates,omitempty"`

	// If specified, all deletable gates will be evaluated for GameServer deletable.
	// A GameServer is deletable when all its pod are deletable AND
	// all conditions specified in the deletable gates have status equal to "True"
	// +optional
	DeletableGates []string `json:"deletableGates,omitempty"`
}

// SchedulingStrategy is the strategy that a Squad & GameServers will use
// when scheduling GameServers' Pods across a cluster.
type SchedulingStrategy string

const (
	// MostAllocated strategy will allocate GameServers
	// on Nodes with the most allocated by inject PodInterAffinity.
	// For dynamic clusters, this Strategy will may cause less GameServers migration when clusters auto scale.
	MostAllocated SchedulingStrategy = "MostAllocated"

	// LeastAllocated strategy will prioritise allocating GameServers
	// on Nodes with the least allocated(scheduler default).
	// For dynamic clusters, this Strategy will may cause more GameServers migration when clusters auto scale.
	LeastAllocated SchedulingStrategy = "LeastAllocated"
)

// PortPolicy is the port policy for the GameServer
type PortPolicy string

const (
	// Static PortPolicy means that the user defines the hostPort to be used
	// in the configuration.
	Static PortPolicy = "Static"
	// Dynamic PortPolicy means that the system will choose an open
	// port for the GameServer in question
	Dynamic PortPolicy = "Dynamic"
	// Passthrough dynamically sets the `containerPort` to the same value as the dynamically selected hostPort.
	// This will mean that users will need to lookup what port has been opened through the server side SDK.
	Passthrough PortPolicy = "Passthrough"
)

// GameServerPort defines a set of Ports that.
// are to be exposed via the GameServer.
type GameServerPort struct {
	// Name is the descriptive name of the port.
	Name string `json:"name,omitempty"`
	// ContainerPort is the port that is being opened on the specified container's process.
	ContainerPort *int32 `json:"containerPort,omitempty"`
	// ContainerPortRange is the port range that is being opened on the specified container's process.
	ContainerPortRange *PortRange `json:"containerPortRange,omitempty"`
	// PortPolicy defines the policy for how the HostPort is populated.
	// Dynamic port will allocate a HostPort within the selected MIN_PORT and MAX_PORT range passed to the controller
	// at installation time.
	// When `Static` portPolicy is specified, `HostPort` or `HostPortRange` is required, to specify the port that game clients will
	// connect to.
	PortPolicy PortPolicy `json:"portPolicy,omitempty"`
	// HostPort the port exposed on the host for clients to connect to.
	HostPort *int32 `json:"hostPort,omitempty"`
	// HostPortRange is the port range that exposed on the host for clients to connect to.
	HostPortRange *PortRange `json:"hostPortRange,omitempty"`
	// Protocol is the network protocol being used. Defaults to UDP. TCP and TCPUDP are other options.
	Protocol corev1.Protocol `json:"protocol,omitempty"`
}

// PortRange define a range of ports.
// It requires both the start and end to be defined.
type PortRange struct {
	// MinPort is the start of the range, inclusive.
	MinPort int32 `json:"minPort"`
	// MaxPort is the end of the range, inclusive.
	MaxPort int32 `json:"maxPort"`
}

// SdkServerLogLevel is the log level for SDK server (sidecar) logs.
type SdkServerLogLevel string

const (
	// SdkServerLogLevelInfo will cause the SDK server to output all messages except for debug messages.
	SdkServerLogLevelInfo SdkServerLogLevel = "Info"
	// SdkServerLogLevelDebug will cause the SDK server to output all messages including debug messages.
	SdkServerLogLevelDebug SdkServerLogLevel = "Debug"
	// SdkServerLogLevelError will cause the SDK server to only output error messages.
	SdkServerLogLevelError SdkServerLogLevel = "Error"
)

// SdkServer specifies parameters for the SDK Server sidecar container.
type SdkServer struct {
	// LogLevel for SDK server (sidecar) logs. Defaults to "Info".
	LogLevel SdkServerLogLevel `json:"logLevel,omitempty"`
	// GRPCPort is the port on which the SDK Server binds the gRPC server to accept incoming connections.
	GRPCPort int32 `json:"grpcPort,omitempty"`
	// HTTPPort is the port on which the SDK Server binds the HTTP gRPC gateway server to accept incoming connections.
	HTTPPort int32 `json:"httpPort,omitempty"`
}

// Health configures health checking on the GameServer
type Health struct {
	// Disabled is whether health checking is disabled or not
	Disabled bool `json:"disabled,omitempty"`
	// PeriodSeconds is the number of seconds each health ping has to occur in
	PeriodSeconds int32 `json:"periodSeconds,omitempty"`
	// FailureThreshold how many failures in a row constitutes unhealthy
	FailureThreshold int32 `json:"failureThreshold,omitempty"`
	// InitialDelaySeconds initial delay before checking health
	InitialDelaySeconds int32 `json:"initialDelaySeconds,omitempty"`
}

// ConstraintType describes the constraint name
type ConstraintType string

// NotInService is one of the ConstraintTypes, which marks GameServer should close the connection.
const NotInService ConstraintType = `NotInService`

// Constraint describes the constraint info of GameServer.
type Constraint struct {
	// Type is the ConstraintType name, e.g. NotInService.
	Type ConstraintType `json:"type"`
	// Effective describes whether the constraint is effective.
	Effective *bool `json:"effective,omitempty"`
	// Message explains why this constraint is added.
	Message string `json:"message,omitempty"`
	// TimeAdded describes when it is added.
	TimeAdded *metav1.Time `json:"timeAdded,omitempty"`
}

// GameServerState is the state of a GameServer at the current time.
type GameServerState string

// These are the valid state of GameServer.
const (
	// GameServerStarting means the pod phase of GameServer is Pending or pod not existing
	GameServerStarting GameServerState = "Starting"
	// GameServerRunning means the pod phase of GameServer is Running
	GameServerRunning GameServerState = "Running"
	// GameServerExited means GameServer has exited
	GameServerExited GameServerState = "Exited"
	// GameServerFailed means the pod phase of GameServer is Failed
	GameServerFailed GameServerState = "Failed"
	// GameServerUnknown means the pod phase of GameServer is Unkown
	GameServerUnknown GameServerState = "Unknown"
)

// GameServerStatus is the status for a GameServer resource.
type GameServerStatus struct {
	// GameServerState is the current state of a GameServer, e.g. Pending, Running, Succeeded, etc
	State GameServerState `json:"state"`
	// Conditions represent GameServer conditions
	Conditions []GameServerCondition `json:"conditions,omitempty"`
	// Address is the IP address of GameServer
	Address string `json:"address"`
	// NodeName is the K8s node name
	NodeName string `json:"nodeName"`
	// LoadBalancerStatus is the load-balancer status
	LoadBalancerStatus *LoadBalancerStatus `json:"loadBalancerStatus,omitempty"`
}

// GameServerConditionType is a valid value for GameServerCondition.Type
type GameServerConditionType string

// These are valid conditions of GameServer.
const (
	// GameServerReady means the server is able to service request.
	GameServerReady GameServerConditionType = "Ready"
	// GameServerHasPlayer means has player on the server, and can not be deleted by the controller.
	GameServerHasPlayer GameServerConditionType = "HasPlayer"
	// GameServerRetired means the server will not service new request, and can be deleted by the controller.
	GameServerRetired GameServerConditionType = "Retired"
	// GameServerFilled means the server run out of all resource, and will not accept more players.
	GameServerFilled GameServerConditionType = "Filled"
	// GameServerUnhealthy is when the GameServer has failed its health checks.
	GameServerUnhealthy GameServerConditionType = "Unhealthy"
)

type ConditionStatus string

// These are valid condition statuses. "ConditionTrue" means a resource is in the condition.
// "ConditionFalse" means a resource is not in the condition.
const (
	ConditionTrue  ConditionStatus = "True"
	ConditionFalse ConditionStatus = "False"
)

// GameServerCondition contains details for the current condition of this GameServer.
type GameServerCondition struct {
	// Type is the type of the condition.
	Type GameServerConditionType `json:"type"`
	// Status is the status of the condition.
	// Can be True, False.
	Status ConditionStatus `json:"status"`
	// Last time we probed the condition.
	LastProbeTime metav1.Time `json:"lastProbeTime"`
	// Last time the condition transitioned from one status to another.
	LastTransitionTime metav1.Time `json:"lastTransitionTime"`
}

// LoadBalancerStatus represents the status of a load-balancer.
type LoadBalancerStatus struct {
	// Ingress is a list containing ingress points for the load-balancer.
	Ingress []LoadBalancerIngress `json:"ingress,omitempty"`
}

// LoadBalancerIngress represents the status of a load-balancer ingress point.
type LoadBalancerIngress struct {
	// IP is the IP of load-balancer.
	IP string `json:"ip"`
	// Ports  are the array of ports that can be exposed via the load-balancer for the GameServer.
	Ports []LoadBalancerPort `json:"ports"`
}

// LoadBalancerPort describes load balancer info
type LoadBalancerPort struct {
	// Name is the load balancer name
	Name string `json:"name,omitempty"`
	// ContainerPort is the port that is being opened on the container's process.
	ContainerPort *int32 `json:"containerPort,omitempty"`
	// ExternalPort is the load-balancer port that exposed to client.
	ExternalPort *int32 `json:"externalPort,omitempty"`
	// ContainerPortRange is a range of port that opened on the container's process.
	ContainerPortRange *PortRange `json:"containerPortRange,omitempty"`
	// ExternalPortRange is a range of load-balancer port that exposed to client.
	ExternalPortRange *PortRange `json:"externalPortRange,omitempty"`
	// Protocol is the network protocol being used. Defaults to UDP. TCP and TCPUDP are other options.
	Protocol corev1.Protocol `json:"protocol,omitempty"`
}

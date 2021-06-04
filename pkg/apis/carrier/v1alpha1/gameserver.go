// Copyright 2021 The OCGI Authors.
// Copyright 2018 Google LLC All Rights Reserved.
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

	// Scheduling strategy, including "LeastAllocated, MostAllocated, Default". Defaults to "MostAllocated".
	Scheduling SchedulingStrategy `json:"scheduling,omitempty"`

	// Template describes the Pod that will be created for the GameServer.
	Template corev1.PodTemplateSpec `json:"template"`

	// Constraints describes the constraints of GameServer.
	// This filed may be added or changed by controller or manually.
	// If anyone of them is `NotInService` and Effective is `True`,
	// GameServer container should not continue serving and should set `Retired` true, HasNoPlayer true
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
	// on Nodes with the most allocated by inject PodAffinity.
	MostAllocated SchedulingStrategy = "MostAllocated"

	// LeastAllocated strategy will prioritise allocating GameServers
	// on Nodes with the most allocated by inject PodAntiAffinity
	LeastAllocated SchedulingStrategy = "LeastAllocated"

	// Default will use scheduler default policy
	Default SchedulingStrategy = "Default"
)

// PortPolicy is the port policy for the GameServer
type PortPolicy string

const (
	// Static PortPolicy will use the port defined in the Pod spec or GameServerSpec.
	Static PortPolicy = "Static"
	// Dynamic PortPolicy will dynamically allocated host Ports.
	Dynamic PortPolicy = "Dynamic"
	// LoaderBalancer PortPolicy will apply the port allocated from external load balacner.
	LoaderBalancer PortPolicy = "LoaderBalancer"
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
	// PortPolicy describes the policy to allocate ports. Dynamic is currently not implemented.
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

// ConditionStatus includes True or False
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
	// Message enables setting more information to a GameServer.
	// For example:
	// When a GameServer starts up, set number of allocatable players,
	// then if a player connects, change the number, which makes
	// developing a gpa webhook based on online players possible.
	Message string `json:"message,omitempty"`
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

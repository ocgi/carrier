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
// +genclient:method=GetScale,verb=get,subresource=scale,result=k8s.io/api/autoscaling/v1.Scale
// +genclient:method=UpdateScale,verb=update,subresource=scale,input=k8s.io/api/autoscaling/v1.Scale,result=k8s.io/api/autoscaling/v1.Scale
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// GameServerSet is the data structure for a set of GameServers.
type GameServerSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GameServerSetSpec   `json:"spec"`
	Status GameServerSetStatus `json:"status"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// GameServerSetList is a list of GameServerSet resources
type GameServerSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []GameServerSet `json:"items"`
}

// GameServerSetSpec the specification for GameServerSet
type GameServerSetSpec struct {
	// Replicas are the number of GameServers that should be in this set
	Replicas int32 `json:"replicas"`
	// Scheduling strategy. Defaults to "MostAllocated".
	Scheduling SchedulingStrategy `json:"scheduling,omitempty"`
	// Template the GameServer template to apply for this GameServerSet
	Template GameServerTemplateSpec `json:"template"`
	// Selector is a label query over pods that should match the replica count.
	// Label keys and values that must match in order to be controlled by this replica set.
	// It must match the pod template's labels.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#label-selectors
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
	// ExcludeConstraints describes if we should exclude GameServer with constraints
	// when computing replicas
	ExcludeConstraints *bool `json:"excludeConstraints,omitempty"`
}

// GameServerSetStatus is the status of a GameServerSet
type GameServerSetStatus struct {
	// Replicas is the total number of current GameServer replicas
	Replicas int32 `json:"replicas"`
	// ReadyReplicas is the number of Ready GameServer replicas
	ReadyReplicas int32 `json:"readyReplicas"`
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration"`
	// Represents the latest available observations of a GameServerSet's current state.
	Conditions []GameServerSetCondition `json:"conditions,omitempty"`
	// Selector is a string format, which is for scale
	Selector string `json:"selector,omitempty"`
}

type GameServerSetConditionType string

// These are valid conditions of a GameServerSet.
const (
	// GameServerSetReplicaFailure is added in a GameServerSet when one of its GameServers fails to be created
	// due to insufficient quota, limit ranges, GameServer security policy, node selectors, etc. or deleted
	// due to kubelet being down or finalizers are failing.
	GameServerSetReplicaFailure GameServerSetConditionType = "ReplicaFailure"
	// GameServerSetScalingInProgress is the state that GameServerSet is scaling. This would be added the condition of
	// GameServerSet, this condition should be set by squad controller and would be removed when GameServerSet
	// finishes scaling.
	GameServerSetScalingInProgress GameServerSetConditionType = "ScalingInProgress"
)

// GameServerSetCondition describes the state of a GameServerSet at a certain point.
type GameServerSetCondition struct {
	// Type of GameServerSet condition.
	Type GameServerSetConditionType `json:"type"`
	// Status of the condition, one of True, False, Unknown.
	Status corev1.ConditionStatus `json:"status"`
	// The last time the condition transitioned from one status to another.
	LastTransitionTime metav1.Time `json:"lastTransitionTime"`
	// The reason for the condition's last transition.
	Reason string `json:"reason"`
	// A human readable message indicating details about the transition.
	Message string `json:"message"`
}

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
	"k8s.io/apimachinery/pkg/util/intstr"
)

// +genclient
// +genclient:method=GetScale,verb=get,subresource=scale,result=k8s.io/api/extensions/v1beta1.Scale
// +genclient:method=UpdateScale,verb=update,subresource=scale,input=k8s.io/api/extensions/v1beta1.Scale,result=k8s.io/api/extensions/v1beta1.Scale
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Squad is the data structure for a Squad resource
type Squad struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SquadSpec   `json:"spec"`
	Status SquadStatus `json:"status"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SquadList is a list of Squad resources
type SquadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Squad `json:"items"`
}

// SquadSpec is the spec for a Squad
type SquadSpec struct {
	// Replicas are the number of GameServers that should be in this set. Defaults to 0.
	Replicas int32 `json:"replicas"`
	// Squad strategy,one of ReCreate, RollingUpdate, CanaryUpdate.
	Strategy SquadStrategy `json:"strategy,omitempty"`
	// Scheduling strategy. Defaults to "MostAllocated".
	Scheduling SchedulingStrategy `json:"scheduling,omitempty"`
	// Template the GameServer template to apply for this Squad
	Template GameServerTemplateSpec `json:"template"`
	// The number of old GameServerSets to retain to allow rollback.
	// This is a pointer to distinguish between explicit zero and not specified.
	RevisionHistoryLimit *int32 `json:"revisionHistoryLimit,omitempty"`
	// Indicates that the Squad is paused and will not be processed by the
	// Squad controller.
	Paused bool `json:"paused,omitempty"`
	// The config this Squad is rolling back to. Will be cleared after rollback is done.
	RollbackTo *RollbackConfig `json:"rollbackTo,omitempty"`
	// Selector is a label query over pods that should match the replica count.
	// Label keys and values that must match in order to be controlled by this replica set.
	// It must match the pod template's labels.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#label-selectors
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
	// ExcludeConstraints describes if we should exclude GameServer with constraints
	// when computing replicas, default false.
	ExcludeConstraints *bool `json:"excludeConstraints,omitempty"`
}

type RollbackConfig struct {
	// The revision to rollback to. If set to 0, rollback to the last revision.
	Revision int64 `json:"revision"`
}

// SquadStrategy is the strategy for a Squad
type SquadStrategy struct {
	// Type of Squad. Can be "Recreate" or "RollingUpdate" or "CanaryUpdate". Default is RollingUpdate.
	Type SquadStrategyType `json:"type,omitempty"`
	// Rolling update config params. Present only if SquadStrategyType = RollingUpdate.
	RollingUpdate *RollingUpdateSquad `json:"rollingUpdate,omitempty"`
	// Canary update config params. Present only if SquadStrategyType = CanaryUpdate.
	CanaryUpdate *CanaryUpdateSquad `json:"canaryUpdate,omitempty"`
	// Inplace update config params. Present only if SquadStrategyType = InplaceUpdate.
	InplaceUpdate *InplaceUpdateSquad `json:"inplaceUpdate,omitempty"`
}

// RollingUpdateSquad controls the desired behavior of rolling update.
type RollingUpdateSquad struct {
	// The maximum number of GameServers that can be unavailable during the update.
	// Value can be an absolute number (ex: 5) or a percentage of total GameServers at the start of update (ex: 10%).
	// Absolute number is calculated from percentage by rounding down.
	// This can not be 0 if MaxSurge is 0.
	// By default, a fixed value of 1 is used.
	// Example: when this is set to 30%, the old RC can be scaled down by 30%
	// immediately when the rolling update starts. Once new GameServers are ready, old RC
	// can be scaled down further, followed by scaling up the new RC, ensuring
	// that at least 70% of original number of GameServers are available at all times
	// during the update.
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable"`
	// The maximum number of GameServers that can be scheduled above the original number of
	// GameServers.
	// Value can be an absolute number (ex: 5) or a percentage of total GameServers at
	// the start of the update (ex: 10%). This can not be 0 if MaxUnavailable is 0.
	// Absolute number is calculated from percentage by rounding up.
	// By default, a value of 1 is used.
	// Example: when this is set to 30%, the new RC can be scaled up by 30%
	// immediately when the rolling update starts. Once old GameServers have been killed,
	// new RC can be scaled up further, ensuring that total number of GameServers running
	// at any time during the update is atmost 130% of original GameServers.
	MaxSurge *intstr.IntOrString `json:"maxSurge"`
}

// CanaryUpdateSquad controls the desired behavior of canary update.
type CanaryUpdateSquad struct {
	// Type of update GameServer. Can be "deleteFirst" or "createFirst" or "inplace". Default is "createFirst".
	Type GameServerStrategyType `json:"type"`
	// The number of GameServers than can be updated
	// Value can be an absolute number(ex: 5) or a percentage of total GameServers at
	// the start of the update (ex: 10%)
	Threshold *intstr.IntOrString `json:"threshold"`
}

// InplaceUpdateSquad to control the desired behavior of inplace update.
type InplaceUpdateSquad struct {
	// The number of GameServers than can be updated
	// Value can be an absolute number(ex: 5) or a percentage of total GameServers at
	// the start of the update (ex: 10%)
	Threshold *intstr.IntOrString `json:"threshold"`
}

type GameServerStrategyType string

const (
	// DeleteFirstGameServerStrategyType Kill GameServer before creating new ones
	DeleteFirstGameServerStrategyType GameServerStrategyType = "deleteFirst"
	// CreateFirstGameServerStrategyType Create new GameServer before kill the old ones
	CreateFirstGameServerStrategyType GameServerStrategyType = "createFirst"
)

type SquadStrategyType string

const (
	// RecreateSquadStrategyType Kill all existing GameServers before creating new ones.
	RecreateSquadStrategyType SquadStrategyType = "Recreate"
	// RollingUpdateSquadStrategyType Replace the old GameServerSets by new one using rolling update
	// i.e gradually scale down the old GameServerSets and scale up the new one.
	RollingUpdateSquadStrategyType SquadStrategyType = "RollingUpdate"
	// CanaryUpdateSquadStrategyType Replace the old GameServerSets by new one using canary update, you can specify the updated threshold
	CanaryUpdateSquadStrategyType SquadStrategyType = "CanaryUpdate"
	// InplaceUpdateSquadStrategyType Replace the old GameServerSets by new one using inplace update, you can specify the updated threshold
	InplaceUpdateSquadStrategyType SquadStrategyType = "InplaceUpdate"
)

// SquadStatus is the status of a Squad
type SquadStatus struct {
	// Replicas the total number of current GameServer replicas
	Replicas int32 `json:"replicas"`
	// ReadyReplicas are the number of Ready GameServer replicas
	ReadyReplicas int32 `json:"readyReplicas"`
	// Total number of non-terminated GameServers targeted by this Squad that have the desired template spec.
	UpdatedReplicas int32 `json:"updatedReplicas"`
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration"`
	// Represents the latest available observations of a Squad's current state.
	Conditions []SquadCondition `json:"conditions,omitempty"`
	// Selector is a string format, which is for scale
	Selector string `json:"selector,omitempty"`
}

type SquadConditionType string

// These are valid conditions of a Squad.
const (
	// SquadProgressing means the Squad is progressing. Progress for a Squad is
	// considered when a new GameServer set is created or adopted, and when new GameServers scale
	// up or old GameServers scale down. Progress is not estimated for paused Squad or
	// when progressDeadlineSeconds is not specified.
	SquadProgressing SquadConditionType = "Progressing"
	// SquadReplicaFailure is added in a Squad when one of its GameServers fails to be created
	// or deleted.
	SquadReplicaFailure SquadConditionType = "ReplicaFailure"
)

// SquadCondition describes the state of a Squad at a certain point.
type SquadCondition struct {
	// Type of Squad condition.
	Type SquadConditionType `json:"type"`
	// Status of the condition, one of True, False, Unknown.
	Status corev1.ConditionStatus `json:"status"`
	// The last time this condition was updated.
	LastUpdateTime metav1.Time `json:"lastUpdateTime"`
	// Last time the condition transitioned from one status to another.
	LastTransitionTime metav1.Time `json:"lastTransitionTime"`
	// The reason for the condition's last transition.
	Reason string `json:"reason"`
	// A human readable message indicating details about the transition.
	Message string `json:"message"`
}

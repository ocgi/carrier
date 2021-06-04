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

package util

import (
	"github.com/ocgi/carrier/pkg/apis/carrier"
)

const (
	// RoleLabelKey is the key we will add when create, value is `gameserver`
	RoleLabelKey = carrier.GroupName + "/role"
	// GameServerLabelRoleValue is the GameServer label value for RoleLabelKey
	GameServerLabelRoleValue = "gameserver"
	// GameServerPodLabelKey default if group + gameserver
	GameServerPodLabelKey = carrier.GroupName + "/gameserver"
	// GameServerSetLabelKey default if group + gameserverset
	GameServerSetLabelKey = carrier.GroupName + "/gameserverset"
	// SquadNameLabelKey default if group + squad
	SquadNameLabelKey = carrier.GroupName + "/squad"
	// GameServerContainerName default is server
	GameServerContainerName = "server"

	// RevisionAnnotation is the revision annotation of a squad's gameserverset which records its rollout sequence
	RevisionAnnotation = carrier.GroupName + "/revision"
	// RevisionHistoryAnnotation maintains the history of all old revisions that a gameserverset has served for a squad.
	RevisionHistoryAnnotation = carrier.GroupName + "/revision-history"
	// DesiredReplicasAnnotation is the desired replicas for a squad recorded as an annotation
	// in its gameserverset. Helps in separating scaling events from the rollout process and for
	// determining if the new gameserverset for a squad is really saturated.
	DesiredReplicasAnnotation = carrier.GroupName + "/desired-replicas"
	// MaxReplicasAnnotation is the maximum replicas a squad can have at a given point, which
	// is squad.spec.replicas + maxSurge. Used by the underlying gameserverset to estimate their
	// proportions in case the Squad has surge replicas.
	MaxReplicasAnnotation = carrier.GroupName + "/max-replicas"
	// FoundNewGSSetReason is added in a squad when it adopts an existing gameserverset.
	FoundNewGSSetReason = "FoundNewGameServerSet"
	// GameServerSetUpdatedReason is added in a squad when one of its gameserverset is updated as part
	// of the rollout process.
	GameServerSetUpdatedReason = "GameServerSetUpdated"
	// FailedGSSetCreateReason is added in a squad when it cannot create a new gameserverset.
	FailedGSSetCreateReason = "GameServerSetCreateError"
	// NewGameServerSetReason is added in a squad when it creates a new gameserverset.
	NewGameServerSetReason = "NewGameServerSetCreated"
	// NewGSSetReadyReason is added in a squad when its newest gameserverset is made ready
	NewGSSetReadyReason = "NewGameServerSetReady"
	// PausedDeployReason is added in a squad when it is paused. Lack of progress shouldn't be
	// estimated once a squad is paused.
	PausedDeployReason = "SquadPaused"
	// ResumedDeployReason is added in a squad when it is resumed. Useful for not failing accidentally
	// Squad that paused amidst a rollout and are bounded by a deadline.
	ResumedDeployReason = "SquadResumed"

	// RollbackRevisionNotFound is not found rollback event reason
	RollbackRevisionNotFound = "SquadRollbackRevisionNotFound"
	// RollbackTemplateUnchanged is the template unchanged rollback event reason
	RollbackTemplateUnchanged = "SquadRollbackTemplateUnchanged"
	// RollbackDone is the done rollback event reason
	RollbackDone = "SquadRollback"
	// ScalingReplicasAnnotation marks squad is scaling
	ScalingReplicasAnnotation = carrier.GroupName + "/scaling"
	// GracefulUpdateAnnotation describes wait for the game server to exit before updating
	GracefulUpdateAnnotation = carrier.GroupName + "/graceful-update"
	// GameServerDeletionCost can be used to set to an int64 that represent the cost of deleting
	// a pod compared to other game servers belonging to the same ReplicaSet. GameServers with lower
	// deletion cost are preferred to be deleted before pods with higher deletion cost.
	// Note that this is honored on a best-effort basis, and so it does not offer guarantees on
	// game server deletion order.
	// The implicit deletion cost for game servers that don't set the annotation is int64 max, negative values are permitted.
	GameServerDeletionCost = "carrier.ocgi.dev/gs-deletion-cost"
	// GameServerDeletionMetrics is the metric name used by cost-server when sorting the candidate game servers
	GameServerDeletionMetrics = "carrier.ocgi.dev/gs-cost-metrics-name"
	// GameServerHash describes the pod spec hash of game server, it will be add to gameserver set's and gameserver's label
	GameServerHash = "carrier.ocgi.dev/gameserver-template-hash"
	// GameServerInPlaceUpdateAnnotation describes gameserver in place update info
	GameServerInPlaceUpdateAnnotation = "carrier.ocgi.dev/inplace-update-threshold"
	// GameServerInPlaceUpdatedReplicasAnnotation describes in place updated game server number
	GameServerInPlaceUpdatedReplicasAnnotation = "carrier.ocgi.dev/inplace-updated-replicas"
	// GameServerInPlaceUpdatingAnnotation describes in place updateing is doning("true", false)
	GameServerInPlaceUpdatingAnnotation = "carrier.ocgi.dev/inplace-updating"
	// GameServerDynamicPortAllocated port allocated for dynamic policy.
	GameServerDynamicPortAllocated = "carrier.ocgi.dev/dynamic-port-allocated"
)

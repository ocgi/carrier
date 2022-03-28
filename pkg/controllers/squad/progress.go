// Copyright 2019 The Kubernetes Authors.
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

package squad

import (
	"context"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog"

	carrierv1alpha1 "github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/util"
)

// syncRolloutStatus updates the status of a Squad during a rollout. There are
// cases this helper will run that cannot be prevented from the scaling detection,
// for example a resync of the Squad after it was scaled up. In those cases,
// we shouldn't try to estimate any progress.
func (c *Controller) syncRolloutStatus(
	allGSSets []*carrierv1alpha1.GameServerSet,
	newGSSet *carrierv1alpha1.GameServerSet,
	squad *carrierv1alpha1.Squad) error {
	newStatus := calculateStatus(allGSSets, newGSSet, squad)
	klog.V(4).Infof("sync squad status: name: %v, spec: %v, status: %+v",
		squad.ObjectMeta, squad.Spec, newStatus)
	// If there is only one GameServerSet that is active then that means we are not running
	// a new rollout and this is a resync where we don't need to estimate any progress.
	// In such a case, we should simply not estimate any progress for this Squad.
	currentCond := GetSquadCondition(squad.Status, carrierv1alpha1.SquadProgressing)
	isCompleteSquad := newStatus.Replicas == newStatus.UpdatedReplicas &&
		currentCond != nil &&
		currentCond.Reason == util.NewGSSetReadyReason
	// Check for progress only if there is a progress deadline set and the latest rollout
	// hasn't completed yet.
	if !isCompleteSquad {
		switch {
		case SquadComplete(squad, &newStatus):
			// Update the Squad conditions with a message for the new GameServerSet that
			// was successfully deployed. If the condition already exists, we ignore this update.
			msg := fmt.Sprintf("Squad %q has successfully progressed.", squad.Name)
			if newGSSet != nil {
				msg = fmt.Sprintf("GameServerSet %q has successfully progressed.", newGSSet.Name)
			}
			condition := NewSquadCondition(carrierv1alpha1.SquadProgressing,
				corev1.ConditionTrue, util.NewGSSetReadyReason, msg)
			SetSquadCondition(&newStatus, *condition)
		case SquadProgressing(squad, &newStatus):
			// If there is any progress made, continue by not checking if the Squad failed. This
			// behavior emulates the rolling updater progressDeadline check.
			msg := fmt.Sprintf("Squad %q is progressing.", squad.Name)
			if newGSSet != nil {
				msg = fmt.Sprintf("GameServerSet %q is progressing.", newGSSet.Name)
			}
			condition := NewSquadCondition(
				carrierv1alpha1.SquadProgressing,
				corev1.ConditionTrue,
				util.GameServerSetUpdatedReason,
				msg)
			if currentCond != nil {
				if currentCond.Status == corev1.ConditionTrue {
					condition.LastTransitionTime = currentCond.LastTransitionTime
				}
				RemoveSquadCondition(&newStatus, carrierv1alpha1.SquadProgressing)
			}
			SetSquadCondition(&newStatus, *condition)
		}
	}

	// Move failure conditions of all GameServerSets in Squad conditions. For now,
	// only one failure condition is returned from getReplicaFailures.
	if replicaFailureCond := c.getReplicaFailures(allGSSets, newGSSet); len(replicaFailureCond) > 0 {
		// There will be only one ReplicaFailure condition on the GameServerSet.
		SetSquadCondition(&newStatus, replicaFailureCond[0])
	} else {
		RemoveSquadCondition(&newStatus, carrierv1alpha1.SquadReplicaFailure)
	}

	// Do not update if there is nothing new to add.
	if reflect.DeepEqual(squad.Status, newStatus) {
		return nil
	}

	newSquad := squad
	newSquad.Status = newStatus
	_, err := c.squadGetter.Squads(newSquad.Namespace).UpdateStatus(context.TODO(), newSquad, metav1.UpdateOptions{})
	return err
}

// getReplicaFailures will convert replica failure conditions from GameServerSets
// to Squad conditions.
func (c *Controller) getReplicaFailures(
	allGSSets []*carrierv1alpha1.GameServerSet,
	newGSSet *carrierv1alpha1.GameServerSet) []carrierv1alpha1.SquadCondition {
	var conditions []carrierv1alpha1.SquadCondition
	if newGSSet != nil {
		for _, c := range newGSSet.Status.Conditions {
			if c.Type != carrierv1alpha1.GameServerSetReplicaFailure {
				continue
			}
			conditions = append(conditions, GameServerSetToSquadCondition(c))
		}
	}

	// Return failures for the new GameServerSet over failures from old GameServerSets.
	if len(conditions) > 0 {
		return conditions
	}

	for i := range allGSSets {
		gsSet := allGSSets[i]
		if gsSet == nil {
			continue
		}

		for _, c := range gsSet.Status.Conditions {
			if c.Type != carrierv1alpha1.GameServerSetReplicaFailure {
				continue
			}
			conditions = append(conditions, GameServerSetToSquadCondition(c))
		}
	}
	return conditions
}

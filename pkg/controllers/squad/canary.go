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
	"fmt"
	"sort"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/klog"
	"k8s.io/utils/integer"

	carrierv1alpha1 "github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/util/kube"
)

// rolloutCanary implements the logic for canary update a GameServerSet.
func (c *Controller) rolloutCanary(squad *carrierv1alpha1.Squad, gsSetList []*carrierv1alpha1.GameServerSet) error {
	if squad.Spec.Strategy.CanaryUpdate == nil {
		return errors.Errorf("Squad %v CanaryUpdate is null", squad.ObjectMeta)
	}
	switch squad.Spec.Strategy.CanaryUpdate.Type {
	case carrierv1alpha1.CreateFirstGameServerStrategyType:
		return c.createFirst(squad, gsSetList)
	case carrierv1alpha1.DeleteFirstGameServerStrategyType:
		return c.deleteFirst(squad, gsSetList)
	}
	return errors.Errorf("No GameServer strategy type found for squad: %v", squad.ObjectMeta)
}

// createFirst scale up the new GameServerSet first
func (c *Controller) createFirst(squad *carrierv1alpha1.Squad, gsSetList []*carrierv1alpha1.GameServerSet) error {
	newGSSet, oldGSSets, err := c.getAllGameServerSetsAndSyncRevision(squad, gsSetList, true)
	if err != nil {
		return err
	}
	allGSSets := append(oldGSSets, newGSSet)
	// Scale up, if we can.
	scaledUp, err := c.scaleUpNewGameServerSetForCanary(allGSSets, newGSSet, squad)
	if err != nil {
		return err
	}
	if scaledUp {
		return c.syncRolloutStatus(allGSSets, newGSSet, squad)
	}
	// Scale down, if we can.
	scaledDown, err := c.scaleDownOldGameServerSetsForCanary(
		allGSSets,
		FilterActiveGameServerSets(oldGSSets),
		newGSSet,
		squad)
	if err != nil {
		return err
	}
	if scaledDown {
		// Update SquadStatus
		return c.syncRolloutStatus(allGSSets, newGSSet, squad)
	}
	if SquadComplete(squad, &squad.Status) {
		if err := c.cleanupSquad(oldGSSets, squad); err != nil {
			return err
		}
		if err := c.clearThreshold(squad); err != nil {
			return err
		}
	}

	// Sync Squad status
	return c.syncRolloutStatus(allGSSets, newGSSet, squad)
}

// deleteFirst scale down the old GameServerSet first
func (c *Controller) deleteFirst(squad *carrierv1alpha1.Squad, gsSetList []*carrierv1alpha1.GameServerSet) error {
	// Don't create a new GameServerSet if not already existed, so that we avoid scaling up before scaling down.
	newGSSet, oldGSSets, err := c.getAllGameServerSetsAndSyncRevision(squad, gsSetList, false)
	if err != nil {
		return err
	}
	allGSSets := append(oldGSSets, newGSSet)
	activeOldGSSets := FilterActiveGameServerSets(oldGSSets)
	// scale down old GameServerSets.
	scaledDown, err := c.scaleDownOldGameServerSetsForCanary(allGSSets, activeOldGSSets, newGSSet, squad)
	if err != nil {
		return err
	}
	if scaledDown {
		// Update SquadStatus.
		return c.syncRolloutStatus(allGSSets, newGSSet, squad)
	}

	// If we need to create a new GameServerSet, create it now.
	if newGSSet == nil {
		newGSSet, oldGSSets, err = c.getAllGameServerSetsAndSyncRevision(squad, gsSetList, true)
		if err != nil {
			return err
		}
		allGSSets = append(oldGSSets, newGSSet)
	}
	// scale up new GameServerSet.
	if _, err := c.scaleUpNewGameServerSetForCanary(allGSSets, newGSSet, squad); err != nil {
		return err
	}
	if SquadComplete(squad, &squad.Status) {
		if err := c.cleanupSquad(oldGSSets, squad); err != nil {
			return err
		}
		if err := c.clearThreshold(squad); err != nil {
			return err
		}
	}

	// Sync Squad status.
	return c.syncRolloutStatus(allGSSets, newGSSet, squad)

}

// scaleUpNewGameServerSetForCanary scales up new GameServerSet when squad strategy is "CanaryUpdate".
func (c *Controller) scaleUpNewGameServerSetForCanary(
	allGSSets []*carrierv1alpha1.GameServerSet,
	newGSSet *carrierv1alpha1.GameServerSet,
	squad *carrierv1alpha1.Squad) (bool, error) {
	threshold := CanaryThreshold(*squad)
	allReplicas := GetReplicaCountForGameServerSets(allGSSets)
	if threshold == 0 && allReplicas > 0 {
		// Do nothing if the threshold is zero
		// and replicas of all GameServerSet is not zero
		return false, nil
	}
	if newGSSet.Spec.Replicas == threshold && allReplicas > 0 {
		// Scaling not required.
		return false, nil
	}
	if newGSSet.Spec.Replicas > squad.Spec.Replicas {
		// Scale down.
		scaled, _, err := c.scaleGameServerSetAndRecordEvent(newGSSet, squad.Spec.Replicas, squad)
		return scaled, err
	}
	newReplicasCount, err := NewGSSetNewReplicas(squad, allGSSets, newGSSet)
	if err != nil {
		return false, err
	}
	scaled, _, err := c.scaleGameServerSetAndRecordEvent(newGSSet, newReplicasCount, squad)
	return scaled, err
}

// scaleDownOldGameServerSetsForCanary scales down old GameServerSets when squad strategy is "CanaryUpdate".
func (c *Controller) scaleDownOldGameServerSetsForCanary(
	allGSSets []*carrierv1alpha1.GameServerSet,
	oldGSSets []*carrierv1alpha1.GameServerSet,
	newGSSet *carrierv1alpha1.GameServerSet,
	squad *carrierv1alpha1.Squad) (bool, error) {
	oldGameServersCount := GetReplicaCountForGameServerSets(oldGSSets)
	if oldGameServersCount == 0 {
		// Can't scale down further
		return false, nil
	}
	threshold := CanaryThreshold(*squad)
	if newGSSet != nil && newGSSet.Status.ReadyReplicas < threshold {
		// wait for new replicas are ready
		klog.V(4).Infof("Found %d ready GameServers in new GameServerSet %s/%s",
			newGSSet.Status.ReadyReplicas,
			newGSSet.Namespace,
			newGSSet.Name)
		return false, nil
	}

	allOldGameServersCount := GetReplicaCountForGameServerSets(oldGSSets)
	klog.V(4).Infof("%d old GameServer for squad: %v", allOldGameServersCount, squad.ObjectMeta)
	maxScaledDown := allOldGameServersCount + threshold - squad.Spec.Replicas
	if maxScaledDown <= 0 {
		//  stop scale down
		return false, nil
	}

	oldGSSets, cleanupCount, err := c.cleanupUnhealthyReplicas(oldGSSets, squad, maxScaledDown)
	if err != nil {
		return false, nil
	}
	klog.V(4).Infof("Cleaned up unhealthy replicas from old GSSets by %d", cleanupCount)
	maxScaledDown = maxScaledDown - cleanupCount
	if maxScaledDown <= 0 {
		return false, nil
	}

	totalScaledDown := int32(0)
	// scale down
	sort.Sort(GameServerSetsByCreationTimestamp(oldGSSets))

	for _, targetGSSet := range oldGSSets {
		if totalScaledDown >= maxScaledDown {
			// No further scaling required.
			break
		}
		if targetGSSet.Spec.Replicas == 0 {
			// cannot scale down this GameServerSet.
			continue
		}
		// Scale down.
		scaleDownCount := int32(integer.IntMin(int(targetGSSet.Spec.Replicas), int(maxScaledDown-totalScaledDown)))
		newReplicasCount := targetGSSet.Spec.Replicas - scaleDownCount
		if newReplicasCount > targetGSSet.Spec.Replicas {
			return false, fmt.Errorf("when scaling down old GSSet, got invalid request to scale down "+
				"%s/%s %d -> %d", targetGSSet.Namespace, targetGSSet.Name, targetGSSet.Spec.Replicas, newReplicasCount)
		}
		_, _, err := c.scaleGameServerSetAndRecordEvent(targetGSSet, newReplicasCount, squad)
		if err != nil {
			return false, err
		}
		totalScaledDown += scaleDownCount
	}

	return totalScaledDown > 0, nil
}

// clearThreshold sets .spec.strategy.canaryUpdate.threshold to zero and update the input Squad
func (c *Controller) clearThreshold(squad *carrierv1alpha1.Squad) error {
	klog.V(4).Infof("Cleans up threshold (%v) of squad %q", squad.Spec.Strategy.CanaryUpdate, squad.Name)
	squadCopy := squad.DeepCopy()
	threshold := intstr.FromInt(0)
	squadCopy.Spec.Strategy.CanaryUpdate.Threshold = &threshold
	patch, err := kube.CreateMergePatch(squad, squadCopy)
	if err != nil {
		return err
	}
	_, err = c.squadGetter.Squads(squad.Namespace).Patch(squad.Name, types.MergePatchType, patch)
	return err
}

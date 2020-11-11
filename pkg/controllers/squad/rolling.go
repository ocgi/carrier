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
	"fmt"
	"sort"

	"k8s.io/klog"
	"k8s.io/utils/integer"

	carrierv1alpha1 "github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
)

// rolloutRecreate implements the logic for rolling a GameServerSet.
func (c *Controller) rolloutRolling(squad *carrierv1alpha1.Squad, gsSetList []*carrierv1alpha1.GameServerSet) error {
	newGSSet, oldGSSets, err := c.getAllGameServerSetsAndSyncRevision(squad, gsSetList, true)
	if err != nil {
		return err
	}
	allGSSets := append(oldGSSets, newGSSet)
	// Scale up, if we can.
	scaledUp, err := c.reconcileNewGameServerSet(allGSSets, newGSSet, squad)
	if err != nil {
		return err
	}
	if scaledUp {
		// Update SquadStatus
		return c.syncRolloutStatus(allGSSets, newGSSet, squad)
	}
	// Scale down, if we can.
	scaledDown, err := c.reconcileOldGameServerSets(allGSSets, FilterActiveGameServerSets(oldGSSets), newGSSet, squad)
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
	}

	// Sync Squad status
	return c.syncRolloutStatus(allGSSets, newGSSet, squad)
}

func (c *Controller) reconcileNewGameServerSet(allGSSets []*carrierv1alpha1.GameServerSet, newGSSet *carrierv1alpha1.GameServerSet, squad *carrierv1alpha1.Squad) (bool, error) {
	if newGSSet.Spec.Replicas == squad.Spec.Replicas {
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

func (c *Controller) reconcileOldGameServerSets(allGSSets []*carrierv1alpha1.GameServerSet, oldGSSets []*carrierv1alpha1.GameServerSet, newGSSet *carrierv1alpha1.GameServerSet, squad *carrierv1alpha1.Squad) (bool, error) {
	oldGameServersCount := GetReplicaCountForGameServerSets(oldGSSets)
	if oldGameServersCount == 0 {
		// Can't scale down further
		return false, nil
	}

	allGameServersCount := GetReplicaCountForGameServerSets(allGSSets)
	klog.V(4).Infof("New GameServerSet %s/%s has %d ready GameServers.", newGSSet.Namespace, newGSSet.Name, newGSSet.Status.ReadyReplicas)
	maxUnavailable := MaxUnavailable(*squad)
	minAvailable := squad.Spec.Replicas - maxUnavailable
	newGSSetUnreadyGameServerCount := newGSSet.Spec.Replicas - newGSSet.Status.ReadyReplicas
	maxScaledDown := allGameServersCount - minAvailable - newGSSetUnreadyGameServerCount
	if maxScaledDown <= 0 {
		return false, nil
	}

	// Clean up unhealthy replicas first, otherwise unhealthy replicas will block Squad
	oldGSSets, cleanupCount, err := c.cleanupUnhealthyReplicas(oldGSSets, squad, maxScaledDown)
	if err != nil {
		return false, nil
	}
	klog.V(4).Infof("Cleaned up unhealthy replicas from old GSSets by %d", cleanupCount)

	// Scale down old GameServerSet, need check maxUnavailable to ensure we can scale down
	allGSSets = append(oldGSSets, newGSSet)
	scaledDownCount, err := c.scaleDownOldGameServerSetsForRollingUpdate(allGSSets, oldGSSets, squad)
	if err != nil {
		return false, nil
	}
	klog.V(4).Infof("Scaled down old GSSets of Squad %s by %d", squad.Name, scaledDownCount)

	totalScaledDown := cleanupCount + scaledDownCount
	return totalScaledDown > 0, nil
}

// scaleDownOldGameServerSetsForRollingUpdate scales down old GameServerSet when Squad strategy is "RollingUpdate".
// Need check maxUnavailable to ensure availability
func (c *Controller) scaleDownOldGameServerSetsForRollingUpdate(allGSSets []*carrierv1alpha1.GameServerSet, oldGSSets []*carrierv1alpha1.GameServerSet, squad *carrierv1alpha1.Squad) (int32, error) {
	maxUnavailable := MaxUnavailable(*squad)

	// Check if we can scale down.
	minAvailable := squad.Spec.Replicas - maxUnavailable
	// Find the number of ready GameServers.
	readyGameServerCount := GetReadyReplicaCountForGameServerSets(allGSSets)
	if readyGameServerCount <= minAvailable {
		// Cannot scale down.
		return 0, nil
	}
	klog.V(4).Infof("Found %d available GameServers in Squad %s, scaling down old GSSets", readyGameServerCount, squad.Name)

	sort.Sort(GameServerSetsByCreationTimestamp(oldGSSets))

	totalScaledDown := int32(0)
	totalScaleDownCount := readyGameServerCount - minAvailable
	for _, targetGSSet := range oldGSSets {
		if totalScaledDown >= totalScaleDownCount {
			// No further scaling required.
			break
		}
		if targetGSSet.Spec.Replicas == 0 {
			// cannot scale down this GameServerSet.
			continue
		}
		// Scale down.
		scaleDownCount := int32(integer.IntMin(int(targetGSSet.Spec.Replicas), int(totalScaleDownCount-totalScaledDown)))
		newReplicasCount := targetGSSet.Spec.Replicas - scaleDownCount
		if newReplicasCount > targetGSSet.Spec.Replicas {
			return 0, fmt.Errorf("when scaling down old GSSet, got invalid request to scale down %s/%s %d -> %d", targetGSSet.Namespace, targetGSSet.Name, targetGSSet.Spec.Replicas, newReplicasCount)
		}
		_, _, err := c.scaleGameServerSetAndRecordEvent(targetGSSet, newReplicasCount, squad)
		if err != nil {
			return totalScaledDown, err
		}

		totalScaledDown += scaleDownCount
	}

	return totalScaledDown, nil
}

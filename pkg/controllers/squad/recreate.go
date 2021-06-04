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
	"k8s.io/apimachinery/pkg/types"

	carrierv1alpha1 "github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
)

// rolloutRecreate implements the logic for recreating a GameServerSet.
func (c *Controller) rolloutRecreate(
	squad *carrierv1alpha1.Squad,
	gsSetList []*carrierv1alpha1.GameServerSet,
	gsMap map[types.UID][]*carrierv1alpha1.GameServer) error {
	// Don't create a new GameServerSet if not already existed, so that we avoid scaling up before scaling down.
	newGSSet, oldGSSets, err := c.getAllGameServerSetsAndSyncRevision(squad, gsSetList, false)
	if err != nil {
		return err
	}

	allGSSets := append(oldGSSets, newGSSet)
	activeOldGSSets := FilterActiveGameServerSets(oldGSSets)
	// scale down old GameServerSets.
	scaledDown, err := c.scaleDownOldGameServerSetsForRecreate(activeOldGSSets, squad)
	if err != nil {
		return err
	}
	if scaledDown {
		// Update SquadStatus.
		return c.syncRolloutStatus(allGSSets, newGSSet, squad)
	}

	// Do not process a Squad when it has old gameservers running.
	if oldGameServersRunning(newGSSet, oldGSSets, gsMap) {
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
	if _, err := c.scaleUpNewGameServerSetForRecreate(newGSSet, squad); err != nil {
		return err
	}
	if SquadComplete(squad, &squad.Status) {
		if err := c.cleanupSquad(oldGSSets, squad); err != nil {
			return err
		}
	}

	// Sync Squad status.
	return c.syncRolloutStatus(allGSSets, newGSSet, squad)
}

// scaleDownOldGameServerSetsForRecreate scales down old GameServerSets when Squad strategy is "Recreate".
func (c *Controller) scaleDownOldGameServerSetsForRecreate(
	oldGSSets []*carrierv1alpha1.GameServerSet,
	squad *carrierv1alpha1.Squad) (bool, error) {
	scaled := false
	for i := range oldGSSets {
		gsSet := oldGSSets[i]
		// Scaling not required.
		if gsSet.Spec.Replicas == 0 {
			continue
		}
		scaledGSSet, updatedGSSet, err := c.scaleGameServerSetAndRecordEvent(gsSet, 0, squad)
		if err != nil {
			return false, err
		}
		if scaledGSSet {
			oldGSSets[i] = updatedGSSet
			scaled = true
		}
	}
	return scaled, nil
}

// oldGameServersRunning returns whether there are old GameServers running or
// any of the old GameServerSets thinks that it runs GameServers.
func oldGameServersRunning(
	newGSSet *carrierv1alpha1.GameServerSet,
	oldGSSets []*carrierv1alpha1.GameServerSet,
	gsMap map[types.UID][]*carrierv1alpha1.GameServer) bool {
	if oldGameServers := GetActualReplicaCountForGameServerSets(oldGSSets); oldGameServers > 0 {
		return true
	}
	for gsSetUID, gsList := range gsMap {
		if newGSSet != nil && newGSSet.UID == gsSetUID {
			continue
		}
		for _, gs := range gsList {
			switch gs.Status.State {
			case carrierv1alpha1.GameServerFailed, carrierv1alpha1.GameServerExited:
				// Don't count GameServers in terminal state.
				continue
			case carrierv1alpha1.GameServerUnknown:
				// This happens in situation like when the node is temporarily disconnected from the cluster.
				// If we can't be sure that the pod is not running, we have to count it.
				return true
			default:
				// GameServer is not in terminal phase.
				return true
			}
		}
	}
	return false
}

// scaleUpNewGameServerSetForRecreate scales up new GameServerSet when Squad strategy is "Recreate".
func (c *Controller) scaleUpNewGameServerSetForRecreate(
	newGSSet *carrierv1alpha1.GameServerSet,
	squad *carrierv1alpha1.Squad) (bool, error) {
	scaled, _, err := c.scaleGameServerSetAndRecordEvent(newGSSet, squad.Spec.Replicas, squad)
	return scaled, err
}

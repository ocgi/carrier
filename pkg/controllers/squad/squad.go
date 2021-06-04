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
	"reflect"
	"sort"
	"strconv"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog"
	"k8s.io/utils/integer"

	carrierv1alpha1 "github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/util"
)

const (
	// limit revision history length to 100 element (~2000 chars)
	maxRevHistoryLengthInChars = 2000
)

// controllerKind contains the schema.GroupVersionKind for this controller type.
var controllerKind = carrierv1alpha1.SchemeGroupVersion.WithKind("Squad")

// sync is responsible for reconciling Squads on scaling events or when they
// are paused.
func (c *Controller) sync(squad *carrierv1alpha1.Squad, gsSetList []*carrierv1alpha1.GameServerSet) error {
	newGSSet, oldGSSets, err := c.getAllGameServerSetsAndSyncRevision(squad, gsSetList, false)
	if err != nil {
		return err
	}
	if err := c.scale(squad, newGSSet, oldGSSets); err != nil {
		// If we get an error while trying to scale, the Squads will be requeued
		// so we can abort this resync
		return err
	}

	// Clean up the Squad when it's paused and no rollback is in flight.
	if squad.Spec.Paused && getRollbackTo(squad) == nil {
		if err := c.cleanupSquad(oldGSSets, squad); err != nil {
			return err
		}
	}

	allRSs := append(oldGSSets, newGSSet)
	return c.syncSquadStatus(allRSs, newGSSet, squad)
}

// syncStatusOnly only updates Squadss Status and doesn't take any mutating actions.
func (c *Controller) syncStatusOnly(squad *carrierv1alpha1.Squad, gsSetList []*carrierv1alpha1.GameServerSet) error {
	newGSSet, oldGSSets, err := c.getAllGameServerSetsAndSyncRevision(squad, gsSetList, false)
	if err != nil {
		return err
	}

	allGSSets := append(oldGSSets, newGSSet)
	return c.syncSquadStatus(allGSSets, newGSSet, squad)
}

// syncSquadStatus checks if the status is up-to-date and sync it if necessary
func (c *Controller) syncSquadStatus(
	allGSSets []*carrierv1alpha1.GameServerSet,
	newGSSet *carrierv1alpha1.GameServerSet,
	squad *carrierv1alpha1.Squad) error {
	newStatus := calculateStatus(allGSSets, newGSSet, squad)
	klog.V(4).Infof("sync squad status: name: %v, spec: %v, status: %v",
		squad.ObjectMeta, squad.Spec, newStatus)
	if reflect.DeepEqual(squad.Status, newStatus) {
		return nil
	}

	newSquad := squad
	newSquad.Status = newStatus
	_, err := c.squadGetter.Squads(newSquad.Namespace).UpdateStatus(newSquad)
	return err
}

// calculateStatus calculates the latest status for the provided Squad by looking into the provided GameServerSet.
func calculateStatus(
	allGSSets []*carrierv1alpha1.GameServerSet,
	newGSSet *carrierv1alpha1.GameServerSet,
	squad *carrierv1alpha1.Squad) carrierv1alpha1.SquadStatus {
	status := carrierv1alpha1.SquadStatus{
		ObservedGeneration: squad.Generation,
		Replicas:           GetActualReplicaCountForGameServerSets(allGSSets),
		UpdatedReplicas:    GetUpdateReplicaCountForGameServerSets([]*carrierv1alpha1.GameServerSet{newGSSet}),
		ReadyReplicas:      GetReadyReplicaCountForGameServerSets(allGSSets),
	}
	conditions := squad.Status.Conditions
	for i := range conditions {
		status.Conditions = append(status.Conditions, conditions[i])
	}
	if squad.Spec.Selector != nil && squad.Spec.Selector.MatchLabels != nil {
		status.Selector = labels.Set(squad.Spec.Selector.MatchLabels).String()
	}
	return status
}

// ListGameServerSetsBySquadOwner lists all the GameServerSets for a given Squad
func (c *Controller) listGameServerSetsByOwner(squad *carrierv1alpha1.Squad) ([]*carrierv1alpha1.GameServerSet, error) {
	labelSelector := labels.Set{util.SquadNameLabelKey: squad.ObjectMeta.Name}
	if squad.Spec.Selector != nil && len(squad.Spec.Selector.MatchLabels) != 0 {
		labelSelector = squad.Spec.Selector.MatchLabels
	}
	list, err := c.gameServerSetLister.GameServerSets(squad.Namespace).List(labels.SelectorFromSet(labelSelector))
	if err != nil {
		return list, errors.Wrapf(err, "error listing GameServerSets for squad %s", squad.ObjectMeta.Name)
	}

	var result []*carrierv1alpha1.GameServerSet
	for _, gsSet := range list {
		if metav1.IsControlledBy(gsSet, squad) {
			result = append(result, gsSet)
		}
	}

	//
	return result, nil
}

// ListGameServersBySquadOwner lists all GameServers that belong to a squad through the
// GameServer -> GameServerSet -> Squad owner chain
func (c *Controller) listGameServersBySquadOwner(squad *carrierv1alpha1.Squad) ([]*carrierv1alpha1.GameServer, error) {
	labelSelector := labels.Set{util.SquadNameLabelKey: squad.ObjectMeta.Name}
	if squad.Spec.Selector != nil && len(squad.Spec.Selector.MatchLabels) != 0 {
		labelSelector = squad.Spec.Selector.MatchLabels
	}
	list, err := c.gameServerLister.GameServers(squad.Namespace).List(labels.SelectorFromSet(labelSelector))
	if err != nil {
		return list, errors.Wrapf(err, "error listing GameServers for squad %s", squad.ObjectMeta.Name)
	}
	return list, nil
}

// getAllGameServerSetsAndSyncRevision returns all the GameServerSet for the provided Squad (new and all old),
// with new GSSet's and Squad's revision updated.
func (c *Controller) getAllGameServerSetsAndSyncRevision(
	squad *carrierv1alpha1.Squad,
	gsSetList []*carrierv1alpha1.GameServerSet,
	createIfNotExisted bool) (*carrierv1alpha1.GameServerSet, []*carrierv1alpha1.GameServerSet, error) {
	_, allOldGSSets := FindOldGameServerSets(squad, gsSetList)
	// Get new GameServerSet with the updated revision number
	newGSSet, err := c.getNewGameServerSet(squad, gsSetList, allOldGSSets, createIfNotExisted)
	if err != nil {
		return nil, nil, err
	}
	return newGSSet, allOldGSSets, nil
}

// findOrCreateGameServerSet returns the active or latest GameServerSet, or create new one on the first time
func (c *Controller) findOrCreateGameServerSet(
	squad *carrierv1alpha1.Squad,
	gsSetList []*carrierv1alpha1.GameServerSet) (*carrierv1alpha1.GameServerSet, bool, error) {
	var (
		allOldGSSets  []*carrierv1alpha1.GameServerSet
		newGSSet      *carrierv1alpha1.GameServerSet
		err           error
		isFirstCreate = false
	)
	if len(gsSetList) == 0 {
		isFirstCreate = true
		newGSSet, err = c.getNewGameServerSet(squad, gsSetList, allOldGSSets, true)
		return newGSSet, isFirstCreate, err
	}
	// when update squad
	newGSSet = FindActiveOrLatest(nil, gsSetList)
	if newGSSet == nil {
		return nil, isFirstCreate, fmt.Errorf("cannot find the active or latest GameServerSet")
	}
	return newGSSet, isFirstCreate, nil
}

// Returns a GameServerSet that matches the intent of the given Squad.
// Returns nil if the new GameServerSet doesn't exist yet.
// 1. Get existing new GameServerSet (the GameServerSet that the given Squad targets,
//    whose GameServer template is the same as Squad's).
// 2. If there's existing new GameServerSet, update its revision number if it's smaller than (maxOldRevision + 1),
//    where maxOldRevision is the max revision number among all old GameServerSetes.
// 3. If there's no existing new GameServerSet and createIfNotExisted is true,
//    create one with appropriate revision number (maxOldRevision + 1) and replicas.
func (c *Controller) getNewGameServerSet(
	squad *carrierv1alpha1.Squad,
	gsSetList, oldGSSets []*carrierv1alpha1.GameServerSet,
	createIfNotExisted bool) (*carrierv1alpha1.GameServerSet, error) {
	existingNewGSSet := FindNewGameServerSet(squad, gsSetList)

	// Calculate the max revision number among all old GameServerSet
	maxOldRevision := MaxRevision(oldGSSets)
	// Calculate revision number for this new GameServerSet
	newRevision := strconv.FormatInt(maxOldRevision+1, 10)

	// Latest GameServerSet exists. We need to sync its annotations
	if existingNewGSSet != nil {
		gsSetCopy := existingNewGSSet.DeepCopy()

		// Set existing new GameServerSet's annotation
		annotationsUpdated := SetNewGameServerSetAnnotations(squad, gsSetCopy,
			newRevision, true, maxRevHistoryLengthInChars)
		if annotationsUpdated {
			return c.gameServerSetGetter.GameServerSets(gsSetCopy.ObjectMeta.Namespace).Update(gsSetCopy)
		}

		// Should use the revision in existingNewGSSet's annotation, since it set by before
		needsUpdate := SetSquadRevision(squad, gsSetCopy.Annotations[util.RevisionAnnotation])
		cond := GetSquadCondition(squad.Status, carrierv1alpha1.SquadProgressing)
		if cond == nil {
			msg := fmt.Sprintf("Found new GameServerSet %q", gsSetCopy.Name)
			condition := NewSquadCondition(carrierv1alpha1.SquadProgressing,
				corev1.ConditionTrue, util.FoundNewGSSetReason, msg)
			SetSquadCondition(&squad.Status, *condition)
			needsUpdate = true
		}

		if needsUpdate {
			var err error
			if _, err = c.squadGetter.Squads(squad.Namespace).UpdateStatus(squad); err != nil {
				return nil, err
			}
		}
		return gsSetCopy, nil
	}

	if !createIfNotExisted {
		return nil, nil
	}

	// new GameServerSet does not exist, create one.
	newGSSetTemplate := *squad.Spec.Template.DeepCopy()
	gsTemplateSpecHash := ComputeHash(&newGSSetTemplate)
	newGSSSetelector := metav1.CloneSelectorAndAddLabel(squad.Spec.Selector, util.SquadNameLabelKey, squad.Name)
	// Create new GameServerSet
	newGSSet := carrierv1alpha1.GameServerSet{
		ObjectMeta: metav1.ObjectMeta{
			// Make the name deterministic, to ensure idempotence
			Name:            squad.Name + "-" + gsTemplateSpecHash,
			Namespace:       squad.Namespace,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(squad, controllerKind)},
			Labels:          newGSSetTemplate.Labels,
		},
		Spec: carrierv1alpha1.GameServerSetSpec{
			Scheduling:         squad.Spec.Scheduling,
			Selector:           newGSSSetelector,
			Template:           newGSSetTemplate,
			ExcludeConstraints: squad.Spec.ExcludeConstraints,
		},
	}
	// Setting GameServerSet labels
	if newGSSet.ObjectMeta.Labels == nil {
		newGSSet.ObjectMeta.Labels = make(map[string]string)
	}
	newGSSet.ObjectMeta.Labels[util.SquadNameLabelKey] = squad.Name
	SetGameServerTemplateHashLabels(&newGSSet)

	allGSSets := append(oldGSSets, &newGSSet)
	newReplicasCount, err := NewGSSetNewReplicas(squad, allGSSets, &newGSSet)
	if err != nil {
		return nil, err
	}

	newGSSet.Spec.Replicas = newReplicasCount
	// Set new GameServerSet's annotation
	SetNewGameServerSetAnnotations(squad, &newGSSet, newRevision, false, maxRevHistoryLengthInChars)
	// Create the new GameServerSet. If it already exists, then we need to check for possible
	// hash collisions. If there is any other error, we need to report it in the status of
	// the Squad.
	alreadyExists := false
	createdGSSet, err := c.gameServerSetGetter.GameServerSets(squad.Namespace).Create(&newGSSet)
	switch {
	// We may end up hitting this due to a slow cache or a fast resync of the Squad.
	case k8serrors.IsAlreadyExists(err):
		alreadyExists = true

		// Fetch a copy of the GameServerSet.
		gsSet, gsSetErr := c.gameServerSetLister.GameServerSets(newGSSet.Namespace).Get(newGSSet.Name)
		if gsSetErr != nil {
			return nil, gsSetErr
		}

		// If the Squad owns the GameServerSet and the GameServerSet's PodTemplateSpec is semantically
		// deep equal to the PodTemplateSpec of the Squad, it's the Squad's new GameServerSet.
		// Otherwise, this is a hash collision and we need to increment the collisionCount field in
		// the status of the Squad and requeue to try the creation in the next sync.
		controllerRef := metav1.GetControllerOf(gsSet)
		if controllerRef != nil &&
			controllerRef.UID == squad.UID &&
			EqualGameServerTemplate(&squad.Spec.Template, &gsSet.Spec.Template) {
			createdGSSet = gsSet
			err = nil
			break
		}
		// Update the collisionCount for the Squad and let it requeue by returning the original
		// error.
		_, dErr := c.squadGetter.Squads(squad.Namespace).UpdateStatus(squad)
		if dErr != nil {
			return nil, dErr
		}
		return nil, err
	case k8serrors.HasStatusCause(err, corev1.NamespaceTerminatingCause):
		// if the namespace is terminating, all subsequent creates will fail and we can safely do nothing
		return nil, err
	case err != nil:
		msg := fmt.Sprintf("Failed to create new GameServerSet %q: %v", newGSSet.Name, err)
		c.recorder.Eventf(squad, corev1.EventTypeWarning, util.FailedGSSetCreateReason, msg)
		return nil, err
	}
	if !alreadyExists && newReplicasCount > 0 {
		c.recorder.Eventf(
			squad,
			corev1.EventTypeNormal,
			"ScalingGameServerSet",
			"Scaled up GameServerSet %s to %d", createdGSSet.Name, newReplicasCount)
	}

	needsUpdate := SetSquadRevision(squad, newRevision)
	if !alreadyExists {
		msg := fmt.Sprintf("Created new GameServerSet %q", createdGSSet.Name)
		condition := NewSquadCondition(
			carrierv1alpha1.SquadProgressing,
			corev1.ConditionTrue,
			util.NewGameServerSetReason,
			msg)
		SetSquadCondition(&squad.Status, *condition)
		needsUpdate = true
	}
	if needsUpdate {
		_, err = c.squadGetter.Squads(squad.Namespace).UpdateStatus(squad)
	}
	return createdGSSet, err
}

// scale scales proportionally in order to mitigate risk. Otherwise, scaling up can increase the size
// of the new GameServerSet and scaling down can decrease the sizes of the old ones, both of which would
// have the effect of hastening the rollout progress, which could produce a higher proportion of unavailable
// replicas in the event of a problem with the rolled out template. Should run only on scaling events or
// when a Squads is paused and not during the normal rollout process.
func (c *Controller) scale(
	squad *carrierv1alpha1.Squad,
	newGSSet *carrierv1alpha1.GameServerSet,
	oldGSSets []*carrierv1alpha1.GameServerSet) error {
	// If there is only one active GameServerSet then we should scale that up to the full count of the
	// Squads. If there is no active GameServerSet, then we should scale up the newest GameServerSet.
	if activeOrLatest := FindActiveOrLatest(newGSSet, oldGSSets); activeOrLatest != nil {
		if activeOrLatest.Spec.Replicas == squad.Spec.Replicas {
			return nil
		}
		_, _, err := c.scaleGameServerSetAndRecordEvent(activeOrLatest, squad.Spec.Replicas, squad)
		return err
	}
	// If the new GameServerSet is saturated, old GameServerSets should be fully scaled down.
	// This case handles GameServerSet adoption during a saturated new GameServerSet.
	if IsSaturated(squad, newGSSet) {
		for _, old := range FilterActiveGameServerSets(oldGSSets) {
			if _, _, err := c.scaleGameServerSetAndRecordEvent(old, 0, squad); err != nil {
				return err
			}
		}
		return nil
	}
	// There are old GameServerSets with GameServers and the new GameServerSet is not saturated.
	// We need to proportionally scale all GameServerSets (new and old) in case of a
	// rolling Squads.
	if IsRollingUpdate(squad) {
		allGSSets := FilterActiveGameServerSets(append(oldGSSets, newGSSet))
		allGSSetsReplicas := GetReplicaCountForGameServerSets(allGSSets)
		allowedSize := int32(0)
		if squad.Spec.Replicas > 0 {
			allowedSize = squad.Spec.Replicas + MaxSurge(*squad)
		}
		// Number of additional replicas that can be either added or removed from the total
		// replicas count. These replicas should be distributed proportionally to the active
		// GameServerSets.
		squadReplicasToAdd := allowedSize - allGSSetsReplicas
		// The additional replicas should be distributed proportionally amongst the active
		// GameServerSets from the larger to the smaller in size GameServerSet. Scaling direction
		// drives what happens in case we are trying to scale GameServerSets of the same size.
		// In such a case when scaling up, we should scale up newer GameServerSets first, and
		// when scaling down, we should scale down older GameServerSets first.
		var scalingOperation string
		switch {
		case squadReplicasToAdd > 0:
			sort.Sort(GameServerSetsBySizeNewer(allGSSets))
			scalingOperation = "up"
		case squadReplicasToAdd < 0:
			sort.Sort(GameServerSetsBySizeOlder(allGSSets))
			scalingOperation = "down"
		}
		// Iterate over all active GameServerSets and estimate proportions for each of them.
		// The absolute value of SquadsReplicasAdded should never exceed the absolute
		// value of SquadsReplicasToAdd.
		squadReplicasAdded := int32(0)
		nameToSize := make(map[string]int32)
		for i := range allGSSets {
			gsSet := allGSSets[i]
			// Estimate proportions if we have replicas to add, otherwise simply populate
			// nameToSize with the current sizes for each GameServerSet.
			if squadReplicasToAdd != 0 {
				proportion := GetProportion(gsSet, *squad, squadReplicasToAdd, squadReplicasAdded)
				nameToSize[gsSet.Name] = gsSet.Spec.Replicas + proportion
				squadReplicasAdded += proportion
			} else {
				nameToSize[gsSet.Name] = gsSet.Spec.Replicas
			}
		}
		// Update all GameServerSets
		for i := range allGSSets {
			gsSet := allGSSets[i]
			// Add/remove any leftovers to the largest GameServerSet.
			if i == 0 && squadReplicasToAdd != 0 {
				leftover := squadReplicasToAdd - squadReplicasAdded
				nameToSize[gsSet.Name] = nameToSize[gsSet.Name] + leftover
				if nameToSize[gsSet.Name] < 0 {
					nameToSize[gsSet.Name] = 0
				}
			}
			if _, _, err := c.scaleGameServerSet(gsSet, nameToSize[gsSet.Name], squad, scalingOperation); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Controller) scaleGameServerSetAndRecordEvent(
	gsSet *carrierv1alpha1.GameServerSet,
	newScale int32,
	squad *carrierv1alpha1.Squad) (bool, *carrierv1alpha1.GameServerSet, error) {
	// No need to scale
	if gsSet.Spec.Replicas == newScale {
		return false, gsSet, nil
	}
	var scalingOperation string
	if gsSet.Spec.Replicas < newScale {
		scalingOperation = "up"
	} else {
		scalingOperation = "down"
	}
	scaled, newRS, err := c.scaleGameServerSet(gsSet, newScale, squad, scalingOperation)
	return scaled, newRS, err
}

func (c *Controller) scaleGameServerSet(
	gsSet *carrierv1alpha1.GameServerSet,
	newScale int32,
	squad *carrierv1alpha1.Squad,
	scalingOperation string) (bool, *carrierv1alpha1.GameServerSet, error) {

	sizeNeedsUpdate := gsSet.Spec.Replicas != newScale
	annotationsNeedUpdate := ReplicasAnnotationsNeedUpdate(
		gsSet,
		squad.Spec.Replicas,
		squad.Spec.Replicas+MaxSurge(*squad))

	scaled := false
	var err error
	if sizeNeedsUpdate || annotationsNeedUpdate {
		gsSetCopy := gsSet.DeepCopy()
		gsSetCopy.Spec.Replicas = newScale
		// Wait for the game server to exit before scaling down
		// or gracefully update
		if IsGameServerSetScaling(gsSetCopy, squad) || IsGracefulUpdate(squad) {
			klog.Infof("Update GameServerSet: %v, annotation `%s`",
				gsSetCopy.ObjectMeta, util.ScalingReplicasAnnotation)
			SetScalingAnnotations(gsSetCopy)
		}
		SetReplicasAnnotations(gsSetCopy, squad.Spec.Replicas, squad.Spec.Replicas+MaxSurge(*squad))
		gsSet, err = c.gameServerSetGetter.GameServerSets(gsSetCopy.Namespace).Update(gsSetCopy)
		if err == nil && sizeNeedsUpdate {
			scaled = true
			c.recorder.Eventf(
				squad,
				corev1.EventTypeNormal,
				"ScalingGameServerSet",
				"Scaled %s GameServerSet %s to %d",
				scalingOperation,
				gsSet.Name,
				newScale)
		}
	}
	return scaled, gsSet, err
}

// cleanupSquad is responsible for cleaning up a Squad ie. retains all but the latest N old GameServerSet
// where N=d.Spec.RevisionHistoryLimit. Old GameServerSet are older versions of the GameServerTemplate
// of a Squad kept
// around by default 1) for historical reasons and 2) for the ability to rollback a Squad.
func (c *Controller) cleanupSquad(oldGSSets []*carrierv1alpha1.GameServerSet, squad *carrierv1alpha1.Squad) error {
	if !HasRevisionHistoryLimit(squad) {
		return nil
	}

	// Avoid deleting GameServerSet with deletion timestamp set
	cleanableGSSets := FilterGameServerSets(oldGSSets, func(gsSet *carrierv1alpha1.GameServerSet) bool {
		return gsSet != nil && gsSet.ObjectMeta.DeletionTimestamp == nil
	})

	diff := int32(len(cleanableGSSets)) - *squad.Spec.RevisionHistoryLimit
	if diff <= 0 {
		return nil
	}

	sort.Sort(GameServerSetsByCreationTimestamp(cleanableGSSets))
	klog.V(4).Infof("Looking to cleanup old GameServerSet for Squad %q", squad.Name)

	for i := int32(0); i < diff; i++ {
		gsSet := cleanableGSSets[i]
		// Avoid delete GameServerSet with non-zero replica counts
		if gsSet.Status.Replicas != 0 ||
			gsSet.Spec.Replicas != 0 ||
			gsSet.Generation > gsSet.Status.ObservedGeneration ||
			gsSet.DeletionTimestamp != nil {
			continue
		}
		klog.V(4).Infof("Trying to cleanup GameServerSet %q for squad %q", gsSet.Name, squad.Name)
		if err := c.gameServerSetGetter.GameServerSets(gsSet.Namespace).Delete(gsSet.Name,
			&metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
			// Return error instead of aggregating and continuing DELETEs on the theory
			// that we may be overloading the api server.
			return err
		}
	}

	return nil
}

// checkPausedConditions checks if the given Squad is paused or not and adds an appropriate condition.
func (c Controller) checkPausedConditions(squad *carrierv1alpha1.Squad) error {
	cond := GetSquadCondition(squad.Status, carrierv1alpha1.SquadProgressing)

	pausedCondExists := cond != nil && cond.Reason == util.PausedDeployReason

	needsUpdate := false
	if squad.Spec.Paused && !pausedCondExists {
		condition := NewSquadCondition(
			carrierv1alpha1.SquadProgressing,
			corev1.ConditionUnknown,
			util.PausedDeployReason,
			"Squad is paused")
		SetSquadCondition(&squad.Status, *condition)
		needsUpdate = true
	} else if !squad.Spec.Paused && pausedCondExists {
		condition := NewSquadCondition(
			carrierv1alpha1.SquadProgressing,
			corev1.ConditionUnknown,
			util.ResumedDeployReason,
			"Squad is resumed")
		SetSquadCondition(&squad.Status, *condition)
		needsUpdate = true
	}

	if !needsUpdate {
		return nil
	}

	var err error
	_, err = c.squadGetter.Squads(squad.Namespace).UpdateStatus(squad)
	return err
}

// isScalingEvent checks whether the provided Squad has been updated with a scaling event
// by looking at the desired-replicas annotation in the active GameServerSet of the Squad.
func (c *Controller) isScalingEvent(
	squad *carrierv1alpha1.Squad,
	gsSetList []*carrierv1alpha1.GameServerSet) (bool, error) {
	newGSSet, oldGSSets, err := c.getAllGameServerSetsAndSyncRevision(squad, gsSetList, false)
	if err != nil {
		return false, err
	}
	allGSSets := append(oldGSSets, newGSSet)
	for _, rs := range FilterActiveGameServerSets(allGSSets) {
		desired, ok := GetDesiredReplicasAnnotation(rs)
		if !ok {
			continue
		}
		if desired != squad.Spec.Replicas {
			return true, nil
		}
	}
	return false, nil
}

// cleanupUnhealthyReplicas will scale down old GameServerSet with unhealthy replicas,
// so that all unhealthy replicas will be deleted.
func (c *Controller) cleanupUnhealthyReplicas(
	oldGSSets []*carrierv1alpha1.GameServerSet,
	squad *carrierv1alpha1.Squad,
	maxCleanupCount int32) ([]*carrierv1alpha1.GameServerSet, int32, error) {
	sort.Sort(GameServerSetsByCreationTimestamp(oldGSSets))
	// Safely scale down all old GameServerSet with unhealthy replicas.
	// GameServer set will sort the GameServers in the order
	// such that not-ready < ready, unscheduled < scheduled, and pending < running.
	// This ensures that unhealthy replicas will been deleted first and won't increase unavailability.
	totalScaledDown := int32(0)
	for i, targetGSSet := range oldGSSets {
		if totalScaledDown >= maxCleanupCount {
			break
		}
		if targetGSSet.Spec.Replicas == 0 {
			// cannot scale down this GameServerSet.
			continue
		}
		klog.V(4).Infof("Found %d ready GameServers in old GSSet %s/%s",
			targetGSSet.Status.ReadyReplicas,
			targetGSSet.Namespace,
			targetGSSet.Name)
		if targetGSSet.Spec.Replicas == targetGSSet.Status.ReadyReplicas {
			// no unhealthy replicas found, no scaling required.
			continue
		}

		scaledDownCount := int32(integer.IntMin(int(maxCleanupCount-totalScaledDown),
			int(targetGSSet.Spec.Replicas-targetGSSet.Status.ReadyReplicas)))
		newReplicasCount := targetGSSet.Spec.Replicas - scaledDownCount
		if newReplicasCount > targetGSSet.Spec.Replicas {
			return nil, 0, fmt.Errorf("when cleaning up unhealthy replicas, got invalid request to scale down "+
				"%s/%s %d -> %d", targetGSSet.Namespace, targetGSSet.Name, targetGSSet.Spec.Replicas, newReplicasCount)
		}
		_, updatedOldGSSet, err := c.scaleGameServerSetAndRecordEvent(targetGSSet, newReplicasCount, squad)
		if err != nil {
			return nil, totalScaledDown, err
		}
		totalScaledDown += scaledDownCount
		oldGSSets[i] = updatedOldGSSet
	}
	return oldGSSets, totalScaledDown, nil
}

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
	"hash/fnv"
	"math"
	"sort"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	intstrutil "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/klog"
	"k8s.io/utils/integer"

	carrierv1alpha1 "github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/controllers/gameserversets"
	"github.com/ocgi/carrier/pkg/util"
	"github.com/ocgi/carrier/pkg/util/hash"
)

// FilterActiveGameServerSets returns GameServerSets that have (or at least ought to have) GameServers.
func FilterActiveGameServerSets(gsSetList []*carrierv1alpha1.GameServerSet) []*carrierv1alpha1.GameServerSet {
	activeFilter := func(gsSet *carrierv1alpha1.GameServerSet) bool {
		return gsSet != nil && gsSet.Spec.Replicas > 0
	}
	return FilterGameServerSets(gsSetList, activeFilter)
}

type filterGSSet func(gsSet *carrierv1alpha1.GameServerSet) bool

// FilterGameServerSets returns GameServerSet that are filtered by filterFn (all returned ones should match filterFn).
func FilterGameServerSets(gsSetList []*carrierv1alpha1.GameServerSet, filterFn filterGSSet) []*carrierv1alpha1.GameServerSet {
	var filtered []*carrierv1alpha1.GameServerSet
	for i := range gsSetList {
		if filterFn(gsSetList[i]) {
			filtered = append(filtered, gsSetList[i])
		}
	}
	return filtered
}

// SetFromGameServerSetTemplate sets the desired GameServerTemplateSpec from a GameServerSettemplate to the given squad.
func SetFromGameServerSetTemplate(squad *carrierv1alpha1.Squad, template carrierv1alpha1.GameServerTemplateSpec) *carrierv1alpha1.Squad {
	squad.Spec.Template.ObjectMeta = template.ObjectMeta
	squad.Spec.Template.Spec = template.Spec
	return squad
}

// SetSquadAnnotationsTo sets Squad's annotations as given GSSet's annotations.
// This action should be done if and only if the Squad is rolling back to this rs.
// Note that apply and revision annotations are not changed.
func SetSquadAnnotationsTo(squad *carrierv1alpha1.Squad, rollbackToGSSet *carrierv1alpha1.GameServerSet) {
	squad.Annotations = getSkippedAnnotations(squad.Annotations)
	for k, v := range rollbackToGSSet.Annotations {
		if !skipCopyAnnotation(k) {
			squad.Annotations[k] = v
		}
	}
}

func getSkippedAnnotations(annotations map[string]string) map[string]string {
	skippedAnnotations := make(map[string]string)
	for k, v := range annotations {
		if skipCopyAnnotation(k) {
			skippedAnnotations[k] = v
		}
	}
	return skippedAnnotations
}

// GetReplicaCountForGameServerSets returns the sum of Replicas of the given GameServerSets.
func GetReplicaCountForGameServerSets(gsSetList []*carrierv1alpha1.GameServerSet) int32 {
	totalReplicas := int32(0)
	for _, gsSet := range gsSetList {
		if gsSet != nil {
			totalReplicas += gsSet.Spec.Replicas
		}
	}
	return totalReplicas
}

// GetActualReplicaCountForGameServerSets returns the sum of actual replicas of the given GameServerSet.
func GetActualReplicaCountForGameServerSets(gsSetList []*carrierv1alpha1.GameServerSet) int32 {
	totalActualReplicas := int32(0)
	for _, gsSet := range gsSetList {
		if gsSet != nil {
			totalActualReplicas += gsSet.Status.Replicas
		}
	}
	return totalActualReplicas
}

// GetUpdateReplicaCountForGameServerSets returns the sum of update replicas of the given GameServerSet.
func GetUpdateReplicaCountForGameServerSets(gsSetList []*carrierv1alpha1.GameServerSet) int32 {
	totalActualReplicas := int32(0)
	for _, gsSet := range gsSetList {
		if gsSet != nil {
			if _, ok := gsSet.Annotations[util.GameServerInPlaceUpdateAnnotation]; ok {
				totalActualReplicas += gameserversets.GetGameServerSetInplaceUpdateStatus(gsSet)
			} else {
				totalActualReplicas += gsSet.Status.Replicas
			}
		}
	}
	return totalActualReplicas
}

// GetReadyReplicaCountForGameServerSets returns the number of ready GameServers corresponding to the given GameServerSets.
func GetReadyReplicaCountForGameServerSets(gsSetList []*carrierv1alpha1.GameServerSet) int32 {
	totalReadyReplicas := int32(0)
	for _, gsSet := range gsSetList {
		if gsSet != nil {
			totalReadyReplicas += gsSet.Status.ReadyReplicas
		}
	}
	return totalReadyReplicas
}

// FindOldGameServerSets returns the old GameServerSets targeted by the given Squad, with the given slice of GameServerSets.
// Note that the first set of old GameServerSets doesn't include the ones with no GameServers, and the second set of old GameServerSets include all old GameServerSets.
func FindOldGameServerSets(squad *carrierv1alpha1.Squad, gsSetList []*carrierv1alpha1.GameServerSet) ([]*carrierv1alpha1.GameServerSet, []*carrierv1alpha1.GameServerSet) {
	var requiredGSSets []*carrierv1alpha1.GameServerSet
	var allGSSets []*carrierv1alpha1.GameServerSet
	newGSSet := FindNewGameServerSet(squad, gsSetList)
	for _, gsSet := range gsSetList {
		// Filter out new GameServerSet
		if newGSSet != nil && gsSet.UID == newGSSet.UID {
			continue
		}
		allGSSets = append(allGSSets, gsSet)
		if gsSet.Spec.Replicas != 0 {
			requiredGSSets = append(requiredGSSets, gsSet)
		}
	}
	return requiredGSSets, allGSSets
}

// FindNewGameServerSet returns the new GameServerSet this given Squad targets (the one with the same GameServer template).
func FindNewGameServerSet(squad *carrierv1alpha1.Squad, gsSetList []*carrierv1alpha1.GameServerSet) *carrierv1alpha1.GameServerSet {
	sort.Sort(GameServerSetsByCreationTimestamp(gsSetList))
	for i := range gsSetList {
		if EqualGameServerTemplate(&gsSetList[i].Spec.Template, &squad.Spec.Template) {
			return gsSetList[i]
		}
	}
	// new GameServerSet does not exist.
	return nil
}

// FindActiveOrLatest returns the only active or the latest GameServerSet in case there is at most one active GameServerSet.
// If there are more active GameServerSet, then we should proportionally scale them.
func FindActiveOrLatest(newGSSet *carrierv1alpha1.GameServerSet, oldGSSets []*carrierv1alpha1.GameServerSet) *carrierv1alpha1.GameServerSet {
	if newGSSet == nil && len(oldGSSets) == 0 {
		return nil
	}

	sort.Sort(sort.Reverse(GameServerSetsByCreationTimestamp(oldGSSets)))
	allGSSets := FilterActiveGameServerSets(append(oldGSSets, newGSSet))

	switch len(allGSSets) {
	case 0:
		// If there is no active GameServerSet then we should return the newest.
		if newGSSet != nil {
			return newGSSet
		}
		return oldGSSets[0]
	case 1:
		return allGSSets[0]
	default:
		return nil
	}
}

func EqualGameServerTemplate(template1, template2 *carrierv1alpha1.GameServerTemplateSpec) bool {
	t1Copy := template1.DeepCopy()
	t2Copy := template2.DeepCopy()
	// Remove specific labels from template.Labels before comparing
	delete(t1Copy.Labels, util.SquadNameLabelKey)
	delete(t2Copy.Labels, util.SquadNameLabelKey)
	delete(t1Copy.Labels, util.GameServerHash)
	delete(t2Copy.Labels, util.GameServerHash)
	return apiequality.Semantic.DeepEqual(t1Copy, t2Copy)
}

// GetDesiredReplicasAnnotation returns the number of desired replicas
func GetDesiredReplicasAnnotation(gsSet *carrierv1alpha1.GameServerSet) (int32, bool) {
	return getIntFromAnnotation(gsSet, util.DesiredReplicasAnnotation)
}

func getMaxReplicasAnnotation(gsSet *carrierv1alpha1.GameServerSet) (int32, bool) {
	return getIntFromAnnotation(gsSet, util.MaxReplicasAnnotation)
}

func getIntFromAnnotation(gsSet *carrierv1alpha1.GameServerSet, annotationKey string) (int32, bool) {
	annotationValue, ok := gsSet.Annotations[annotationKey]
	if !ok {
		return int32(0), false
	}
	intValue, err := strconv.Atoi(annotationValue)
	if err != nil {
		klog.V(2).Infof("Cannot convert the value %q with annotation key %q for the GameServerSet %q", annotationValue, annotationKey, gsSet.Name)
		return int32(0), false
	}
	return int32(intValue), true
}

// SetReplicasAnnotations sets the desiredReplicas and maxReplicas into the annotations
func SetReplicasAnnotations(gsSet *carrierv1alpha1.GameServerSet, desiredReplicas, maxReplicas int32) bool {
	updated := false
	if gsSet.Annotations == nil {
		gsSet.Annotations = make(map[string]string)
	}
	desiredString := fmt.Sprintf("%d", desiredReplicas)
	if hasString := gsSet.Annotations[util.DesiredReplicasAnnotation]; hasString != desiredString {
		gsSet.Annotations[util.DesiredReplicasAnnotation] = desiredString
		updated = true
	}
	maxString := fmt.Sprintf("%d", maxReplicas)
	if hasString := gsSet.Annotations[util.MaxReplicasAnnotation]; hasString != maxString {
		gsSet.Annotations[util.MaxReplicasAnnotation] = maxString
		updated = true
	}
	return updated
}

func SetScalingAnnotations(gsSet *carrierv1alpha1.GameServerSet) bool {
	updated := false
	if gsSet.Annotations == nil {
		gsSet.Annotations = make(map[string]string)
	}
	if hasString := gsSet.Annotations[util.ScalingReplicasAnnotation]; hasString == "" {
		gsSet.Annotations[util.ScalingReplicasAnnotation] = "true"
		updated = true
	}
	return updated
}

// ReplicasAnnotationsNeedUpdate return true if ReplicasAnnotations need to be updated
func ReplicasAnnotationsNeedUpdate(gsSet *carrierv1alpha1.GameServerSet, desiredReplicas, maxReplicas int32) bool {
	if gsSet.Annotations == nil {
		return true
	}
	desiredString := fmt.Sprintf("%d", desiredReplicas)
	if hasString := gsSet.Annotations[util.DesiredReplicasAnnotation]; hasString != desiredString {
		return true
	}
	maxString := fmt.Sprintf("%d", maxReplicas)
	if hasString := gsSet.Annotations[util.MaxReplicasAnnotation]; hasString != maxString {
		return true
	}
	return false
}

// IsGameServerSetScaling return true if only scaling GameServerSet
func IsGameServerSetScaling(gsSet *carrierv1alpha1.GameServerSet, squad *carrierv1alpha1.Squad) bool {
	isScaling := false
	desiredString := fmt.Sprintf("%d", squad.Spec.Replicas)
	hasString := gsSet.Annotations[util.DesiredReplicasAnnotation]
	if desiredString != hasString && EqualGameServerTemplate(&squad.Spec.Template, &gsSet.Spec.Template) {
		isScaling = true
	}
	return isScaling
}

// IsGracefulUpdate return true if only 'GracefulUpdateAnnotation' is true
func IsGracefulUpdate(squad *carrierv1alpha1.Squad) bool {
	val, ok := squad.Annotations[util.GracefulUpdateAnnotation]
	if ok && val == "true" {
		return true
	}
	return false
}

// IsSaturated checks if the new GameServerSet is saturated by comparing its size with its Squad size.
func IsSaturated(squad *carrierv1alpha1.Squad, gsSet *carrierv1alpha1.GameServerSet) bool {
	if gsSet == nil {
		return false
	}

	return gsSet.Spec.Replicas == squad.Spec.Replicas &&
		gsSet.Status.ReadyReplicas == squad.Spec.Replicas
}

// IsRollingUpdate returns true if the strategy type is a rolling update.
func IsRollingUpdate(squad *carrierv1alpha1.Squad) bool {
	return squad.Spec.Strategy.Type == carrierv1alpha1.RollingUpdateSquadStrategyType
}

// IsCanaryUpdate returns true if the strategy type is a canary update.
func IsCanaryUpdate(squad *carrierv1alpha1.Squad) bool {
	return squad.Spec.Strategy.Type == carrierv1alpha1.CanaryUpdateSquadStrategyType
}

// IsInplaceUpdate returns true if the strategy type is a inplace update
func IsInplaceUpdate(squad *carrierv1alpha1.Squad) bool {
	return squad.Spec.Strategy.Type == carrierv1alpha1.InplaceUpdateSquadStrategyType
}

// MaxSurge returns the maximum surge GameServers a rolling squad can take.
func MaxSurge(squad carrierv1alpha1.Squad) int32 {
	if !IsRollingUpdate(&squad) {
		return int32(0)
	}
	// Error caught by validation
	maxSurge, _, _ := ResolveFenceposts(squad.Spec.Strategy.RollingUpdate.MaxSurge, squad.Spec.Strategy.RollingUpdate.MaxUnavailable, squad.Spec.Replicas)
	return maxSurge
}

// MaxUnavailable returns the maximum unavailable GameServers a rolling squad can take.
func MaxUnavailable(squad carrierv1alpha1.Squad) int32 {
	if !IsRollingUpdate(&squad) || squad.Spec.Replicas == 0 {
		return int32(0)
	}
	// Error caught by validation
	_, maxUnavailable, _ := ResolveFenceposts(squad.Spec.Strategy.RollingUpdate.MaxSurge, squad.Spec.Strategy.RollingUpdate.MaxUnavailable, squad.Spec.Replicas)
	if maxUnavailable > squad.Spec.Replicas {
		return squad.Spec.Replicas
	}
	return maxUnavailable
}

func CanaryThreshold(squad carrierv1alpha1.Squad) int32 {
	if !IsCanaryUpdate(&squad) ||
		squad.Spec.Replicas == 0 ||
		squad.Spec.Strategy.CanaryUpdate == nil {
		return int32(0)
	}
	return getThreshold(squad.Spec.Replicas, squad.Spec.Strategy.CanaryUpdate.Threshold)
}

func InplaceThreshold(squad carrierv1alpha1.Squad) int32 {
	if !IsInplaceUpdate(&squad) ||
		squad.Spec.Replicas == 0 ||
		squad.Spec.Strategy.InplaceUpdate == nil {
		return int32(0)
	}
	return getThreshold(squad.Spec.Replicas, squad.Spec.Strategy.InplaceUpdate.Threshold)
}

func getThreshold(replicas int32, threshold *intstrutil.IntOrString) int32 {
	newThreshold, err := intstrutil.GetValueFromIntOrPercent(intstrutil.ValueOrDefault(threshold, intstrutil.FromInt(0)), int(replicas), true)
	if err != nil {
		return int32(0)
	}
	if int32(newThreshold) > replicas {
		return replicas
	}
	return int32(newThreshold)
}

// ResolveFenceposts resolves both maxSurge and maxUnavailable. This needs to happen in one
// step. For example:
//
// 2 desired, max unavailable 1%, surge 0% - should scale old(-1), then new(+1), then old(-1), then new(+1)
// 1 desired, max unavailable 1%, surge 0% - should scale old(-1), then new(+1)
// 2 desired, max unavailable 25%, surge 1% - should scale new(+1), then old(-1), then new(+1), then old(-1)
// 1 desired, max unavailable 25%, surge 1% - should scale new(+1), then old(-1)
// 2 desired, max unavailable 0%, surge 1% - should scale new(+1), then old(-1), then new(+1), then old(-1)
// 1 desired, max unavailable 0%, surge 1% - should scale new(+1), then old(-1)
func ResolveFenceposts(maxSurge, maxUnavailable *intstrutil.IntOrString, desired int32) (int32, int32, error) {
	surge, err := intstrutil.GetValueFromIntOrPercent(intstrutil.ValueOrDefault(maxSurge, intstrutil.FromInt(0)), int(desired), true)
	if err != nil {
		return 0, 0, err
	}
	unavailable, err := intstrutil.GetValueFromIntOrPercent(intstrutil.ValueOrDefault(maxUnavailable, intstrutil.FromInt(0)), int(desired), false)
	if err != nil {
		return 0, 0, err
	}

	if surge == 0 && unavailable == 0 {
		unavailable = 1
	}

	return int32(surge), int32(unavailable), nil
}

// GetProportion will estimate the proportion for the provided GameServerSet using
// 1. the current size of the parent Squad
// 2. the replica count that needs be added on the GameServerSet of the Squad
// 3. the total replicas added in the GameServerSet of the Squad so far.
func GetProportion(gsSet *carrierv1alpha1.GameServerSet, squad carrierv1alpha1.Squad, squadReplicasToAdd, squadReplicasAdded int32) int32 {
	if gsSet == nil || gsSet.Spec.Replicas == 0 || squadReplicasToAdd == 0 || squadReplicasToAdd == squadReplicasAdded {
		return int32(0)
	}

	gsFraction := getGameServerSetFraction(*gsSet, squad)
	allowed := squadReplicasToAdd - squadReplicasAdded

	if squadReplicasToAdd > 0 {
		// Use the minimum between the GameServerSet fraction and the maximum allowed replicas
		// when scaling up. This way we ensure we will not scale up more than the allowed
		// replicas we can add.
		return integer.Int32Min(gsFraction, allowed)
	}
	// Use the maximum between the GameServerSet fraction and the maximum allowed replicas
	// when scaling down. This way we ensure we will not scale down more than the allowed
	// replicas we can remove.
	return integer.Int32Max(gsFraction, allowed)
}

// getGameServerSetFraction estimates the fraction of replicas a GameServerSet can have in
// 1. a scaling event during a rollout or
// 2. when scaling a paused Squad.
func getGameServerSetFraction(gsSet carrierv1alpha1.GameServerSet, squad carrierv1alpha1.Squad) int32 {
	// If we are scaling down to zero then the fraction of this GameServerSet is its whole size (negative)
	if squad.Spec.Replicas == int32(0) {
		return -gsSet.Spec.Replicas
	}

	squadReplicas := squad.Spec.Replicas + MaxSurge(squad)
	annotatedReplicas, ok := getMaxReplicasAnnotation(&gsSet)
	if !ok {
		annotatedReplicas = squad.Status.Replicas
	}
	newRSsize := (float64(gsSet.Spec.Replicas * squadReplicas)) / float64(annotatedReplicas)
	return integer.RoundToInt32(newRSsize) - gsSet.Spec.Replicas
}

// MaxRevision finds the highest revision in the GameServerSet
func MaxRevision(allGSSets []*carrierv1alpha1.GameServerSet) int64 {
	max := int64(0)
	for _, gsSet := range allGSSets {
		if v, err := Revision(gsSet); err != nil {
			// Skip the GameServerSet when it failed to parse their revision information
			klog.V(4).Infof("Error: %v. Couldn't parse revision for GameServerSet %#v, Squad controller will skip it when reconciling revisions.", err, gsSet)
		} else if v > max {
			max = v
		}
	}
	return max
}

// LastRevision finds the second max revision number in all GameServerSet (the last revision)
func LastRevision(allGSSets []*carrierv1alpha1.GameServerSet) int64 {
	max, secMax := int64(0), int64(0)
	for _, gsSet := range allGSSets {
		if v, err := Revision(gsSet); err != nil {
			// Skip the GameServerSet when it failed to parse their revision information
			klog.V(4).Infof("Error: %v. Couldn't parse revision for GameServerSet %#v, squad controller will skip it when reconciling revisions.", err, gsSet)
		} else if v >= max {
			secMax = max
			max = v
		} else if v > secMax {
			secMax = v
		}
	}
	return secMax
}

// Revision returns the revision number of the input object.
func Revision(obj runtime.Object) (int64, error) {
	acc, err := meta.Accessor(obj)
	if err != nil {
		return 0, err
	}
	v, ok := acc.GetAnnotations()[util.RevisionAnnotation]
	if !ok {
		return 0, nil
	}
	return strconv.ParseInt(v, 10, 64)
}

// SetNewGameServerSetAnnotations sets new GameServerSet's annotations appropriately by updating its revision and
// copying required Squad annotations to it; it returns true if GameServerSet's annotation is changed.
func SetNewGameServerSetAnnotations(squad *carrierv1alpha1.Squad, newGSSet *carrierv1alpha1.GameServerSet, newRevision string, exists bool, revHistoryLimitInChars int) bool {
	// First, copy Squad's annotations (except for apply and revision annotations)
	annotationChanged := copySquadAnnotationsToGameServerSet(squad, newGSSet)
	// Then, update GameServerSet's revision annotation
	if newGSSet.Annotations == nil {
		newGSSet.Annotations = make(map[string]string)
	}
	oldRevision, _ := newGSSet.Annotations[util.RevisionAnnotation]
	// The newGSSet's revision should be the greatest among all GSSets. Usually, its revision number is newRevision (the max revision number
	// of all old GSSets + 1). However, it's possible that some of the old GSSets are deleted after the newGSSet revision being updated, and
	// newRevision becomes smaller than newGSSet's revision. We should only update newGSSet revision when it's smaller than newRevision.
	oldRevisionInt, err := strconv.ParseInt(oldRevision, 10, 64)
	if err != nil {
		if oldRevision != "" {
			klog.Warningf("Updating GameServerSet revision OldRevision not int %s", err)
			return false
		}
		//If the GSSet annotation is empty then initialise it to 0
		oldRevisionInt = 0
	}
	newRevisionInt, err := strconv.ParseInt(newRevision, 10, 64)
	if err != nil {
		klog.Warningf("Updating GameServerSet revision NewRevision not int %s", err)
		return false
	}
	if oldRevisionInt < newRevisionInt {
		newGSSet.Annotations[util.RevisionAnnotation] = newRevision
		annotationChanged = true
		klog.V(4).Infof("Updating GameServerSet %q revision to %s", newGSSet.Name, newRevision)
	}
	if !exists && SetReplicasAnnotations(newGSSet, squad.Spec.Replicas, squad.Spec.Replicas+MaxSurge(*squad)) {
		annotationChanged = true
	}
	return annotationChanged
}

var annotationsToSkip = map[string]bool{
	util.RevisionAnnotation:        true,
	util.RevisionHistoryAnnotation: true,
	util.DesiredReplicasAnnotation: true,
	util.MaxReplicasAnnotation:     true,
	util.ScalingReplicasAnnotation: true,
}

// skipCopyAnnotation returns true if we should skip copying the annotation with the given annotation key
func skipCopyAnnotation(key string) bool {
	return annotationsToSkip[key]
}

// copySquadAnnotationsToGameServerSet copies Squad's annotations to GameServerSet's annotations,
// and returns true if GameServerSet's annotation is changed.
// Note that apply and revision annotations are not copied.
func copySquadAnnotationsToGameServerSet(squad *carrierv1alpha1.Squad, gsSet *carrierv1alpha1.GameServerSet) bool {
	gsSetAnnotationsChanged := false
	if gsSet.Annotations == nil {
		gsSet.Annotations = make(map[string]string)
	}
	for k, v := range squad.Annotations {
		// newGSSet revision is updated automatically in getNewGameServerSet, and the Squad's revision number is then updated
		// by copying its newGSSet revision number. We should not copy Squad's revision to its newGSSet, since the update of
		// Squad revision number may fail (revision becomes stale) and the revision number in newGSSet is more reliable.
		if skipCopyAnnotation(k) || gsSet.Annotations[k] == v {
			continue
		}
		gsSet.Annotations[k] = v
		gsSetAnnotationsChanged = true
	}
	return gsSetAnnotationsChanged
}

// NewSquadCondition creates a new squad condition.
func NewSquadCondition(condType carrierv1alpha1.SquadConditionType, status corev1.ConditionStatus, reason, message string) *carrierv1alpha1.SquadCondition {
	return &carrierv1alpha1.SquadCondition{
		Type:               condType,
		Status:             status,
		LastUpdateTime:     metav1.Now(),
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
}

// GetSquadCondition returns the condition with the provided type.
func GetSquadCondition(status carrierv1alpha1.SquadStatus, condType carrierv1alpha1.SquadConditionType) *carrierv1alpha1.SquadCondition {
	for i := range status.Conditions {
		c := status.Conditions[i]
		if c.Type == condType {
			return &c
		}
	}
	return nil
}

// SetSquadCondition updates the squad to include the provided condition. If the condition that
// we are about to add already exists and has the same status and reason then we are not going to update.
func SetSquadCondition(status *carrierv1alpha1.SquadStatus, condition carrierv1alpha1.SquadCondition) {
	currentCond := GetSquadCondition(*status, condition.Type)
	if currentCond != nil && currentCond.Status == condition.Status && currentCond.Reason == condition.Reason {
		return
	}
	// Do not update lastTransitionTime if the status of the condition doesn't change.
	if currentCond != nil && currentCond.Status == condition.Status {
		condition.LastTransitionTime = currentCond.LastTransitionTime
	}
	newConditions := filterOutCondition(status.Conditions, condition.Type)
	status.Conditions = append(newConditions, condition)
}

// filterOutCondition returns a new slice of squad conditions without conditions with the provided type.
func filterOutCondition(conditions []carrierv1alpha1.SquadCondition, condType carrierv1alpha1.SquadConditionType) []carrierv1alpha1.SquadCondition {
	var newConditions []carrierv1alpha1.SquadCondition
	for _, c := range conditions {
		if c.Type == condType {
			continue
		}
		newConditions = append(newConditions, c)
	}
	return newConditions
}

// SetSquadRevision updates the revision for a squad.
func SetSquadRevision(squad *carrierv1alpha1.Squad, revision string) bool {
	updated := false

	if squad.Annotations == nil {
		squad.Annotations = make(map[string]string)
	}
	if squad.Annotations[util.RevisionAnnotation] != revision {
		squad.Annotations[util.RevisionAnnotation] = revision
		updated = true
	}

	return updated
}

// NewGSSetNewReplicas calculates the number of replicas a squad's new GSSet should have.
// When one of the followings is true, we're rolling out the squad; otherwise, we're scaling it.
// 1) The new GSSet is saturated: newGSSet's replicas == squad's replicas
// 2) Max number of GameServers allowed is reached: squad's replicas + maxSurge == all GSSets' replicas
func NewGSSetNewReplicas(squad *carrierv1alpha1.Squad, allGSSets []*carrierv1alpha1.GameServerSet, newGSSet *carrierv1alpha1.GameServerSet) (int32, error) {
	switch squad.Spec.Strategy.Type {
	case carrierv1alpha1.RollingUpdateSquadStrategyType:
		// Check if we can scale up.
		maxSurge, err := intstrutil.GetValueFromIntOrPercent(squad.Spec.Strategy.RollingUpdate.MaxSurge, int(squad.Spec.Replicas), true)
		if err != nil {
			return 0, err
		}
		// Find the total number of GameServers
		currentGameServerCount := GetReplicaCountForGameServerSets(allGSSets)
		maxTotalGameServers := squad.Spec.Replicas + int32(maxSurge)
		if currentGameServerCount >= maxTotalGameServers {
			// Cannot scale up.
			return newGSSet.Spec.Replicas, nil
		}
		// Scale up.
		scaleUpCount := maxTotalGameServers - currentGameServerCount
		// Do not exceed the number of desired replicas.
		scaleUpCount = int32(integer.IntMin(int(scaleUpCount), int(squad.Spec.Replicas-newGSSet.Spec.Replicas)))
		return newGSSet.Spec.Replicas + scaleUpCount, nil
	case carrierv1alpha1.RecreateSquadStrategyType:
		return squad.Spec.Replicas, nil
	case carrierv1alpha1.CanaryUpdateSquadStrategyType:
		allReplicas := GetReplicaCountForGameServerSets(allGSSets)
		if allReplicas == 0 {
			// When one of the followings is true, return squad's replicas
			// 1) The first time create squad
			// 2) The current number of replicas of all GameServerSet is 0 and needs to be scale up.
			return squad.Spec.Replicas, nil
		}
		return CanaryThreshold(*squad), nil
	case carrierv1alpha1.InplaceUpdateSquadStrategyType:
		return squad.Spec.Replicas, nil
	default:
		return 0, fmt.Errorf("squad type %v isn't supported", squad.Spec.Strategy.Type)
	}
}

// SquadComplete considers a Squad to be complete once all of its desired replicas
// are updated , and no old GameServers are running.
func SquadComplete(squad *carrierv1alpha1.Squad, newStatus *carrierv1alpha1.SquadStatus) bool {
	return newStatus.UpdatedReplicas == squad.Spec.Replicas &&
		newStatus.Replicas == squad.Spec.Replicas &&
		newStatus.ReadyReplicas == squad.Spec.Replicas &&
		newStatus.ObservedGeneration >= squad.Generation
}

// SquadProgressing reports progress for a Squad. Progress is estimated by comparing the
// current with the new status of the Squad that the controller is observing. More specifically,
// when new GameServers are scaled up or become ready or available, or old GameServers are scaled down, then we
// consider the Squad is progressing.
func SquadProgressing(squad *carrierv1alpha1.Squad, newStatus *carrierv1alpha1.SquadStatus) bool {
	oldStatus := squad.Status

	// Old replicas that need to be scaled down
	oldStatusOldReplicas := oldStatus.Replicas - oldStatus.UpdatedReplicas
	newStatusOldReplicas := newStatus.Replicas - newStatus.UpdatedReplicas

	return (newStatus.UpdatedReplicas > oldStatus.UpdatedReplicas) ||
		(newStatusOldReplicas < oldStatusOldReplicas) ||
		newStatus.ReadyReplicas > squad.Status.ReadyReplicas
}

// RemoveSquadCondition removes the Squad condition with the provided type.
func RemoveSquadCondition(status *carrierv1alpha1.SquadStatus, condType carrierv1alpha1.SquadConditionType) {
	status.Conditions = filterOutCondition(status.Conditions, condType)
}

// GameServerSetToSquadCondition converts a GameServerSet condition into a Squad condition.
// Useful for promoting GameServerSet failure conditions into Squad.
func GameServerSetToSquadCondition(cond carrierv1alpha1.GameServerSetCondition) carrierv1alpha1.SquadCondition {
	return carrierv1alpha1.SquadCondition{
		Type:               carrierv1alpha1.SquadConditionType(cond.Type),
		Status:             cond.Status,
		LastTransitionTime: cond.LastTransitionTime,
		LastUpdateTime:     cond.LastTransitionTime,
		Reason:             cond.Reason,
		Message:            cond.Message,
	}
}

// HasRevisionHistoryLimit checks if the Squad d is expected to keep a specified number of old GameServerSets
func HasRevisionHistoryLimit(squad *carrierv1alpha1.Squad) bool {
	return squad.Spec.RevisionHistoryLimit != nil && *squad.Spec.RevisionHistoryLimit != math.MaxInt32
}

func ComputeHash(template *carrierv1alpha1.GameServerTemplateSpec) string {
	gsTemplateSpecHasher := fnv.New32a()
	hash.DeepHashObject(gsTemplateSpecHasher, *template)

	return rand.SafeEncodeString(fmt.Sprint(gsTemplateSpecHasher.Sum32()))
}

// SetGameServerTemplateHashLabels setting pod spec hash to GameServerSet labels
func SetGameServerTemplateHashLabels(gsSet *carrierv1alpha1.GameServerSet) {
	podSpecHash := ComputePodSpecHash(&gsSet.Spec.Template.Spec.Template.Spec)
	if gsSet.Labels == nil {
		gsSet.Labels = make(map[string]string)
	}
	gsSet.Labels[util.GameServerHash] = podSpecHash
	if gsSet.Spec.Template.Labels == nil {
		gsSet.Spec.Template.Labels = make(map[string]string)
	}
	gsSet.Spec.Template.Labels[util.GameServerHash] = podSpecHash
}

// SetGameServerSetInplaceUpdateAnnotations setting GameServerSet annotations when inplace update
func SetGameServerSetInplaceUpdateAnnotations(gsSet *carrierv1alpha1.GameServerSet, squad *carrierv1alpha1.Squad) {
	if gsSet.Annotations == nil {
		gsSet.Annotations = make(map[string]string)
	}
	gsSet.Annotations[util.GameServerInPlaceUpdateAnnotation] = strconv.Itoa(int(InplaceThreshold(*squad)))
}

// ComputePodSpecHash return the hash value of the podspec
func ComputePodSpecHash(spec *corev1.PodSpec) string {
	gsTemplateHasher := fnv.New32a()
	hash.DeepHashObject(gsTemplateHasher, *spec)

	return rand.SafeEncodeString(fmt.Sprint(gsTemplateHasher.Sum32()))
}

// GameServerSetsByCreationTimestamp sorts a list of GameServerSet by creation timestamp, using their names as a tie breaker.
type GameServerSetsByCreationTimestamp []*carrierv1alpha1.GameServerSet

func (o GameServerSetsByCreationTimestamp) Len() int      { return len(o) }
func (o GameServerSetsByCreationTimestamp) Swap(i, j int) { o[i], o[j] = o[j], o[i] }
func (o GameServerSetsByCreationTimestamp) Less(i, j int) bool {
	if o[i].CreationTimestamp.Equal(&o[j].CreationTimestamp) {
		return o[i].Name < o[j].Name
	}
	return o[i].CreationTimestamp.Before(&o[j].CreationTimestamp)
}

// GameServerSetsBySizeNewer sorts a list of GameServerSet by size in descending order, using their creation timestamp or name as a tie breaker.
// By using the creation timestamp, this sorts from new to old GameServerSet.
type GameServerSetsBySizeNewer []*carrierv1alpha1.GameServerSet

func (o GameServerSetsBySizeNewer) Len() int      { return len(o) }
func (o GameServerSetsBySizeNewer) Swap(i, j int) { o[i], o[j] = o[j], o[i] }
func (o GameServerSetsBySizeNewer) Less(i, j int) bool {
	if o[i].Spec.Replicas == o[j].Spec.Replicas {
		return GameServerSetsByCreationTimestamp(o).Less(j, i)
	}
	return o[i].Spec.Replicas > o[j].Spec.Replicas
}

// GameServerSetsBySizeOlder sorts a list of GameServerSet by size in descending order, using their creation timestamp or name as a tie breaker.
// By using the creation timestamp, this sorts from old to new GameServerSet.
type GameServerSetsBySizeOlder []*carrierv1alpha1.GameServerSet

func (o GameServerSetsBySizeOlder) Len() int      { return len(o) }
func (o GameServerSetsBySizeOlder) Swap(i, j int) { o[i], o[j] = o[j], o[i] }
func (o GameServerSetsBySizeOlder) Less(i, j int) bool {
	if o[i].Spec.Replicas == o[j].Spec.Replicas {
		return GameServerSetsByCreationTimestamp(o).Less(i, j)
	}
	return o[i].Spec.Replicas > o[j].Spec.Replicas
}

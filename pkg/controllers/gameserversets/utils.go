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

package gameserversets

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	carrierv1alpha1 "github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
	listerv1 "github.com/ocgi/carrier/pkg/client/listers/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/util"
)

// GameServer returns a single GameServer derived
// from the GameServer template
func GameServer(gsSet *carrierv1alpha1.GameServerSet) *carrierv1alpha1.GameServer {
	gs := &carrierv1alpha1.GameServer{
		ObjectMeta: *gsSet.Spec.Template.ObjectMeta.DeepCopy(),
		Spec:       *gsSet.Spec.Template.Spec.DeepCopy(),
	}

	gs.Spec.Scheduling = gsSet.Spec.Scheduling

	// Switch to GenerateName, so that we always get a Unique name for the GameServer, and there
	// can be no collisions
	gs.GenerateName = gsSet.Name + "-"
	gs.Name = ""
	gs.Namespace = gsSet.Namespace
	gs.ResourceVersion = ""
	gs.UID = ""

	ref := metav1.NewControllerRef(gsSet, carrierv1alpha1.SchemeGroupVersion.WithKind("GameServerSet"))
	gs.OwnerReferences = append(gs.OwnerReferences, *ref)

	if gs.Labels == nil {
		gs.Labels = make(map[string]string, 2)
	}

	gs.Labels[util.GameServerSetGameServerLabel] = gsSet.Name
	gs.Labels[util.SquadNameLabel] = gsSet.Labels[util.SquadNameLabel]
	if gs.Annotations == nil {
		gs.Annotations = make(map[string]string)
	}
	return gs
}

// IsGameServerSetScaling check if the GameServerSet is scaling GameServer.
func IsGameServerSetScaling(gsSet *carrierv1alpha1.GameServerSet) bool {
	for _, condition := range gsSet.Status.Conditions {
		if condition.Type == carrierv1alpha1.GameServerSetScalingInProgress && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// IsGameServerSetInPlaceUpdating check if the GameServerSet is updating GameServer in place.
func IsGameServerSetInPlaceUpdating(gsSet *carrierv1alpha1.GameServerSet) (bool, int) {
	if gsSet.Annotations == nil {
		return false, 0
	}
	valStr, ok := gsSet.Annotations[util.GameServerInPlaceUpdateAnnotation]
	if !ok {
		return false, 0
	}
	if number, err := strconv.Atoi(valStr); err == nil {
		return true, number
	}
	return false, 0
}

// ChangeScalingStatus remove the ScalingInProgress when scaling finishes
func ChangeScalingStatus(gsSet *carrierv1alpha1.GameServerSet) []carrierv1alpha1.GameServerSetCondition {
	var conditions []carrierv1alpha1.GameServerSetCondition
	for _, condition := range gsSet.Status.Conditions {
		if condition.Type == carrierv1alpha1.GameServerSetScalingInProgress && condition.Status == corev1.ConditionTrue {
			conditions = append(conditions, carrierv1alpha1.GameServerSetCondition{
				Type:               condition.Type,
				Status:             corev1.ConditionFalse,
				LastTransitionTime: metav1.Time{Time: time.Now()},
				Reason:             "Scaling Success",
				Message:            "Scaling Success, scaling label removed",
			})
			continue
		}
		conditions = append(conditions, condition)
	}
	return conditions
}

// AddScalingStatus remove the ScalingInProgress when scaling finishes
func AddScalingStatus(gsSet *carrierv1alpha1.GameServerSet) []carrierv1alpha1.GameServerSetCondition {
	var conditions []carrierv1alpha1.GameServerSetCondition
	for _, condition := range gsSet.Status.Conditions {
		if condition.Type == carrierv1alpha1.GameServerSetScalingInProgress {
			continue
		}
		conditions = append(conditions, condition)
	}
	conditions = append(conditions, carrierv1alpha1.GameServerSetCondition{
		Type:               carrierv1alpha1.GameServerSetScalingInProgress,
		Status:             corev1.ConditionTrue,
		Reason:             "Scaling required",
		Message:            "GameServer Scaling required",
		LastTransitionTime: metav1.Time{Time: time.Now()},
	})
	return conditions
}

// GetDeletionCostFromGameServerAnnotations returns the integer value of gs-deletion-cost. Returns int64 max
// if not set or the value is invalid.
func GetDeletionCostFromGameServerAnnotations(annotations map[string]string) (int64, error) {
	if value, exist := annotations[util.GameServerDeletionCost]; exist {
		// values that start with plus sign (e.g, "+10") or leading zeros (e.g., "008") are not valid.
		if !validFirstDigit(value) {
			return 0, fmt.Errorf("invalid value %q", value)
		}

		i, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			// make sure we default to int64 max on error.
			return int64(math.MaxInt64), err
		}
		return i, nil
	}
	return int64(math.MaxInt64), nil
}

// GetGameServerSetInplaceUpdateStatus get the current number of updated replicas
func GetGameServerSetInplaceUpdateStatus(gsSet *carrierv1alpha1.GameServerSet) int32 {
	if gsSet.Annotations == nil {
		return 0
	}
	val, ok := gsSet.Annotations[util.GameServerInPlaceUpdatedReplicasAnnotation]
	if !ok {
		return 0
	}
	replicas, err := strconv.Atoi(val)
	if err != nil {
		return 0
	}
	return int32(replicas)
}

func validFirstDigit(str string) bool {
	if len(str) == 0 {
		return false
	}
	return str[0] == '-' || (str[0] == '0' && str == "0") || (str[0] >= '1' && str[0] <= '9')
}

// ListGameServersByGameServerSetOwner lists the GameServers for a given GameServerSet
func ListGameServersByGameServerSetOwner(gameServerLister listerv1.GameServerLister,
	gsSet *carrierv1alpha1.GameServerSet) ([]*carrierv1alpha1.GameServer, error) {
	labelSelector := labels.Set{util.GameServerSetGameServerLabel: gsSet.Name}
	if gsSet.Spec.Selector != nil && len(gsSet.Spec.Selector.MatchLabels) != 0 {
		labelSelector = gsSet.Spec.Selector.MatchLabels
	}
	list, err := gameServerLister.List(labels.SelectorFromSet(labelSelector))
	if err != nil {
		return list, errors.Wrapf(err, "error listing GameServers for GameServerSet %s", gsSet.ObjectMeta.Name)
	}
	var result []*carrierv1alpha1.GameServer
	for _, gs := range list {
		if metav1.IsControlledBy(gs, gsSet) {
			result = append(result, gs)
		}
	}

	return result, nil
}

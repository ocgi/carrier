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
	"sort"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	carrierv1alpha1 "github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
	listerv1 "github.com/ocgi/carrier/pkg/client/listers/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/util"
)

// sortGameServersByPodNum sorts the list of GameServers by which GameServers reside on the least full nodes
func sortGameServersByPodNum(list []*carrierv1alpha1.GameServer, counter *Counter) []*carrierv1alpha1.GameServer {
	sort.Slice(list, func(i, j int) bool {
		a := list[i]
		b := list[j]
		// not scheduled yet/node deleted, put them first
		ac, ok := counter.count(a.Status.NodeName)
		if !ok {
			return true
		}

		bc, ok := counter.count(b.Status.NodeName)
		if !ok {
			return false
		}
		if ac == bc {
			return a.Name < b.Name
		}

		return ac < bc
	})

	return list
}

// sortGameServersByCost sorts the list of GameServers by which GameServers reside on the game server cost.
func sortGameServersByCost(list []*carrierv1alpha1.GameServer) []*carrierv1alpha1.GameServer {
	sort.Slice(list, func(i, j int) bool {
		costI, err := GetDeletionCostFromGameServerAnnotations(list[i].Annotations)
		if err != nil {
			return true
		}
		costJ, err := GetDeletionCostFromGameServerAnnotations(list[j].Annotations)
		if err != nil {
			return false
		}
		return costI < costJ
	})

	return list
}

// sortGameServersByCreationTime sorts by newest GameServers first, and returns them
func sortGameServersByCreationTime(list []*carrierv1alpha1.GameServer) []*carrierv1alpha1.GameServer {
	sort.Slice(list, func(i, j int) bool {
		a := list[i]
		b := list[j]
		if a.CreationTimestamp.Equal(&b.CreationTimestamp) {
			return a.Name < b.Name
		}
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	})

	return list
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

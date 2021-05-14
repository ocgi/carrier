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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog"

	carrierv1alpha1 "github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/util"
)

// rollback the Squad to the specified revision. In any case cleanup the rollback spec.
func (c *Controller) rollback(squad *carrierv1alpha1.Squad, gsSetList []*carrierv1alpha1.GameServerSet) error {
	newGSSet, allOldGSSets, err := c.getAllGameServerSetsAndSyncRevision(squad, gsSetList, true)
	if err != nil {
		return err
	}

	allGSSets := append(allOldGSSets, newGSSet)
	rollbackTo := getRollbackTo(squad)
	// If rollback revision is 0, rollback to the last revision
	if rollbackTo.Revision == 0 {
		if rollbackTo.Revision = LastRevision(allGSSets); rollbackTo.Revision == 0 {
			// If we still can't find the last revision, gives up rollback
			c.emitRollbackWarningEvent(squad, util.RollbackRevisionNotFound, "Unable to find last revision.")
			// Gives up rollback
			return c.updateSquadAndClearRollbackTo(squad)
		}
	}
	for _, gsSet := range allGSSets {
		v, err := Revision(gsSet)
		if err != nil {
			klog.V(4).Infof("Unable to extract revision from squad's gameserver set %q: %v", gsSet.Name, err)
			continue
		}
		if v == rollbackTo.Revision {
			klog.V(4).Infof("Found gameserver set %q with desired revision %d", gsSet.Name, v)
			// rollback by copying gameServerTemplate.Spec from the gameserver set
			// revision number will be incremented during the next getAllGameServerSetsAndSyncRevision call
			// no-op if the spec matches current squad's gameServerTemplate.Spec
			performedRollback, err := c.rollbackToTemplate(squad, gsSet)
			if performedRollback && err == nil {
				c.emitRollbackNormalEvent(squad, fmt.Sprintf("Rolled back squad %q to revision %d", squad.Name, rollbackTo.Revision))
			}
			return err
		}
	}
	c.emitRollbackWarningEvent(squad, util.RollbackRevisionNotFound, "Unable to find the revision to rollback to.")
	// Gives up rollback
	return c.updateSquadAndClearRollbackTo(squad)
}

// rollbackToTemplate compares the templates of the provided Squad and gameserver set and
// updates the Squad with the gameserver set template in case they are different. It also
// cleans up the rollback spec so subsequent requeues of the Squad won't end up in here.
func (c *Controller) rollbackToTemplate(squad *carrierv1alpha1.Squad, gsSet *carrierv1alpha1.GameServerSet) (bool, error) {
	performedRollback := false
	if !EqualGameServerTemplate(&squad.Spec.Template, &gsSet.Spec.Template) {
		klog.V(4).Infof("Rolling back Squad %q to template spec %+v", squad.Name, gsSet.Spec.Template.Spec)
		SetFromGameServerSetTemplate(squad, gsSet.Spec.Template)
		SetSquadAnnotationsTo(squad, gsSet)
		performedRollback = true
	} else {
		klog.V(4).Infof("Rolling back to a revision that contains the same template as current Squad %q, skipping rollback...", squad.Name)
		eventMsg := fmt.Sprintf("The rollback revision contains the same template as current Squad %q", squad.Name)
		c.emitRollbackWarningEvent(squad, util.RollbackTemplateUnchanged, eventMsg)
	}

	return performedRollback, c.updateSquadAndClearRollbackTo(squad)
}

// updateSquadAndClearRollbackTo sets .spec.rollbackTo to nil and update the input Squad
func (c *Controller) updateSquadAndClearRollbackTo(squad *carrierv1alpha1.Squad) error {
	klog.V(4).Infof("Cleans up rollbackTo of squad %q", squad.Name)
	squad.Spec.RollbackTo = nil
	_, err := c.squadGetter.Squads(squad.Namespace).Update(squad)
	return err
}

func (c *Controller) emitRollbackWarningEvent(squad *carrierv1alpha1.Squad, reason, message string) {
	c.recorder.Eventf(squad, corev1.EventTypeWarning, reason, message)
}

func (c *Controller) emitRollbackNormalEvent(squad *carrierv1alpha1.Squad, message string) {
	c.recorder.Eventf(squad, corev1.EventTypeNormal, util.RollbackDone, message)
}

func getRollbackTo(squad *carrierv1alpha1.Squad) *carrierv1alpha1.RollbackConfig {
	if squad.Spec.RollbackTo != nil {
		return squad.Spec.RollbackTo
	}
	return nil
}

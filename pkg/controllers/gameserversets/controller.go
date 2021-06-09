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
	"context"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"

	carrierv1alpha1 "github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/client/clientset/versioned"
	"github.com/ocgi/carrier/pkg/client/informers/externalversions"
	listerv1alpha1 "github.com/ocgi/carrier/pkg/client/listers/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/controllers/gameservers"
	"github.com/ocgi/carrier/pkg/util"
	"github.com/ocgi/carrier/pkg/util/kube"
)

const (
	// BurstReplicas is a rate limiter for booting pods on a lot of pods.
	BurstReplicas = 64
)

// Counter caches the node GameServer location
type Counter struct {
	nodeGameServer map[string]uint64
	sync.RWMutex
}

func (c *Counter) count(node string) (uint64, bool) {
	c.RLock()
	c.RUnlock()
	count, ok := c.nodeGameServer[node]
	return count, ok
}

func (c *Counter) inc(node string) {
	c.Lock()
	c.nodeGameServer[node] += 1
	c.Unlock()
}

func (c *Counter) dec(node string) {
	c.Lock()
	defer c.Unlock()
	count, ok := c.nodeGameServer[node]
	if !ok {
		return
	}
	count -= 1
	if count == 0 {
		delete(c.nodeGameServer, node)
	}
}

// Controller is a the GameServerSet controller
type Controller struct {
	counter             *Counter
	carrierClient       versioned.Interface
	gameServerLister    listerv1alpha1.GameServerLister
	gameServerSynced    cache.InformerSynced
	gameServerSetLister listerv1alpha1.GameServerSetLister
	gameServerSetSynced cache.InformerSynced
	workerQueue         workqueue.RateLimitingInterface
	stop                <-chan struct{}
	recorder            record.EventRecorder
}

// NewController returns a new GameServerSet crd controller
func NewController(
	kubeClient kubernetes.Interface,
	carrierClient versioned.Interface,
	carrierInformerFactory externalversions.SharedInformerFactory) *Controller {

	gameServers := carrierInformerFactory.Carrier().V1alpha1().GameServers()
	gsInformer := gameServers.Informer()
	gameServerSets := carrierInformerFactory.Carrier().V1alpha1().GameServerSets()
	gsSetInformer := gameServerSets.Informer()

	c := &Controller{
		counter:             &Counter{nodeGameServer: map[string]uint64{}},
		gameServerLister:    gameServers.Lister(),
		gameServerSynced:    gsInformer.HasSynced,
		gameServerSetLister: gameServerSets.Lister(),
		gameServerSetSynced: gsSetInformer.HasSynced,
		carrierClient:       carrierClient,
	}
	c.workerQueue = workqueue.NewRateLimitingQueue(
		workqueue.NewItemFastSlowRateLimiter(20*time.Millisecond, 500*time.Millisecond, 5))
	s := scheme.Scheme
	// Register operator types with the runtime scheme.
	s.AddKnownTypes(carrierv1alpha1.SchemeGroupVersion, &carrierv1alpha1.GameServerSet{})
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{
		Interface: kubeClient.CoreV1().Events("")})
	c.recorder = eventBroadcaster.NewRecorder(s,
		corev1.EventSource{Component: "gameserverset-controller"})
	gsSetInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.enqueueGameServerSet,
		UpdateFunc: c.updateGameServerSet,
		DeleteFunc: c.deleteGamServerSet,
	})
	gsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			gs := obj.(*carrierv1alpha1.GameServer)
			if gs.DeletionTimestamp == nil && len(gs.Status.NodeName) != 0 {
				c.counter.inc(gs.Status.NodeName)
			}
			c.gameServerEventHandler(gs)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			gsOld := oldObj.(*carrierv1alpha1.GameServer)
			gs := newObj.(*carrierv1alpha1.GameServer)
			// ignore if already being deleted
			if gs.DeletionTimestamp == nil {
				c.gameServerEventHandler(gs)
			}
			if len(gsOld.Status.NodeName) == 0 && len(gs.Status.NodeName) != 0 {
				c.counter.inc(gs.Status.NodeName)
			}
		},
		DeleteFunc: func(obj interface{}) {
			gs, ok := obj.(*carrierv1alpha1.GameServer)
			if !ok {
				return
			}
			if len(gs.Status.NodeName) != 0 {
				c.counter.dec(gs.Status.NodeName)
			}
			c.gameServerEventHandler(obj)
		},
	})
	return c
}

// Run the GameServer controller. Will block until stop is closed.
// Runs threadiness number workers to process the rate limited queue
func (c *Controller) Run(workers int, stop <-chan struct{}) error {
	klog.Info("Wait for cache sync")
	if !cache.WaitForCacheSync(stop, c.gameServerSynced, c.gameServerSetSynced) {
		return errors.New("failed to wait for caches to sync")
	}
	for i := 0; i < workers; i++ {
		go wait.Until(c.worker, time.Second, stop)
	}
	<-stop
	return nil
}

func (c *Controller) updateGameServerSet(old, cur interface{}) {
	c.enqueueGameServerSet(cur)
}

func (c *Controller) enqueueGameServerSet(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %+v: %v", obj, err))
		return
	}

	c.workerQueue.AddRateLimited(key)
}

func (c *Controller) deleteGamServerSet(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %+v: %v", obj, err))
		return
	}

	c.workerQueue.Forget(key)
}

func (c *Controller) worker() {
	for c.processNextWorkItem() {
	}
	klog.Infof("GameServerSet controller worker shutting down")
}

func (c *Controller) processNextWorkItem() bool {
	key, quit := c.workerQueue.Get()
	if quit {
		return false
	}
	defer c.workerQueue.Done(key)

	err := c.syncGameServerSet(key.(string))
	if err != nil {
		c.workerQueue.AddRateLimited(key)
		utilruntime.HandleError(err)
		return true
	}
	c.workerQueue.Forget(key)
	return true
}

// gameServerEventHandler handle GameServerSet changes
func (c *Controller) gameServerEventHandler(obj interface{}) {
	gs, ok := obj.(*carrierv1alpha1.GameServer)
	if !ok {
		return
	}
	ref := metav1.GetControllerOf(gs)
	if ref == nil {
		return
	}
	gsSet, err := c.gameServerSetLister.GameServerSets(gs.Namespace).Get(ref.Name)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			klog.Infof("Owner GameServerSet no longer available for syncing, ref: %v", ref)
		} else {
			runtime.HandleError(errors.Wrap(err, "error retrieving GameServer owner"))
		}
		return
	}
	c.enqueueGameServerSet(gsSet)
}

// syncGameServerSet sync GameServers numbers and version.
func (c *Controller) syncGameServerSet(key string) error {
	// Convert the namespace/name string into a distinct namespace and name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		// don't return an error, as we don't want this retried
		runtime.HandleError(errors.Wrapf(err, "invalid resource key"))
		return nil
	}
	klog.V(2).Infof("Sync gameServerSet %v", key)
	gsSetInCache, err := c.gameServerSetLister.GameServerSets(namespace).Get(name)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			klog.V(3).Info("GameServerSet is no longer available for syncing")
			return nil
		}
		return errors.Wrapf(err, "error retrieving GameServerSet %s from namespace %s", name, namespace)
	}
	gsSet := gsSetInCache.DeepCopy()
	list, err := ListGameServersByGameServerSetOwner(c.gameServerLister, gsSet)
	if err != nil {
		return err
	}
	err = c.manageReplicas(key, list, gsSet)
	if err != nil {
		return err
	}
	gsSet, err = c.syncGameServerSetStatus(gsSet, list)
	if err != nil {
		klog.Error(err)
		return err
	}
	return nil
}

// manageReplicas manages replicas for GameServerSet: 1. scale up/down. 2. inplace updating.
// scale up and inpalce updating can operate at the same time. scale down and inpalce updating is as follow:
// if inplace updating, then scaling down. scale down the older version(for Running GameServer),
// do not scale down the updating one.
// if scaling down, then inpalce updating. constraint is added, add inplace annotation directly, and go on.
func (c *Controller) manageReplicas(key string, list []*carrierv1alpha1.GameServer,
	gsSet *carrierv1alpha1.GameServerSet) error {
	klog.Infof("Current GameServer number of GameServerSet %v: %v", key, len(list))
	gameServersToAdd, toDeleteList, exceedBurst := c.computeReconciliationAction(gsSet, list, c.counter)
	status := computeStatus(list, gsSet)
	klog.V(5).Infof("Reconciling GameServerSet name: %v, spec: %v, status: %v", key, gsSet.Spec, status)
	if exceedBurst {
		defer c.workerQueue.Add(gsSet)
	}
	klog.V(2).Infof("GameSeverSet: %v toAdd: %v, toDelete: %v, list: %+v",
		key, gameServersToAdd, len(toDeleteList), toDeleteList)
	if gameServersToAdd > 0 {
		if err := c.createGameServers(gsSet, gameServersToAdd); err != nil {
			klog.Errorf("error adding game servers: %v", err)
		}
	}
	var toDeletes, candidates, runnings []*carrierv1alpha1.GameServer
	if len(toDeleteList) > 0 {
		toDeletes, candidates, runnings = classifyGameServers(toDeleteList, false)
		// GameServers can be deleted directly.
		c.recorder.Eventf(gsSet, corev1.EventTypeNormal, "ToDelete",
			"Created GameServer: %+v, can delete: %v", len(list), len(toDeleteList))
		klog.Infof("toDeleteList toDeletes %v, candidates %v, runnings %v",
			len(toDeletes), len(candidates), len(runnings))
		if err := c.deleteGameServers(gsSet, toDeletes); err != nil {
			klog.Errorf("error deleting game servers: %v", err)
			return err
		}
		if err := c.markGameServersOutOfService(gsSet, runnings); err != nil {
			return err
		}
	}

	var err error
	gsSet, err = c.syncGameServerSetStatus(gsSet, list)
	if err != nil {
		klog.Error(err)
		return err
	}
	if status.Replicas-int32(len(toDeleteList))+int32(gameServersToAdd) != gsSet.Spec.Replicas {
		return fmt.Errorf("GameServerSet %v actual replicas: %v, desired: %v, to delete %v, to add: %v", key,
			gsSet.Status.Replicas, gsSet.Spec.Replicas, len(toDeleteList), gameServersToAdd)
	}
	return c.doInPlaceUpdate(gsSet)
}

// doInPlaceUpdate  try to do inplace update.
// tree 3 steps:
// 1. update GameServer to `out of service`, add `in progress`
// 2. update GameServer image, remove `in progress`
// 3. update GameServerSet updated replicas. This step must
//    ensure success or failed but cache synced.
func (c *Controller) doInPlaceUpdate(gsSet *carrierv1alpha1.GameServerSet) error {
	inPlaceUpdating, desired := IsGameServerSetInPlaceUpdating(gsSet)
	if !inPlaceUpdating {
		return nil
	}
	klog.V(4).Infof("desired threshold : %v", gsSet.Annotations[util.GameServerInPlaceUpdateAnnotation])
	klog.V(4).Infof("desired gsSet hash: %v", gsSet.Labels[util.GameServerHash])
	// get servers from lister, may exist race
	oldGameServers, newGameServers, err := c.getOldAndNewReplicas(gsSet)
	if err != nil {
		klog.Error(err)
		return err
	}
	diff := desired - len(newGameServers)
	updatedCount := GetGameServerSetInplaceUpdateStatus(gsSet)
	klog.V(4).Infof("desired replicas satisfied, desired: %v, "+
		"diff: %v, new version: %v, updated according to ann: %v", desired, diff, len(newGameServers), updatedCount)
	if diff <= 0 || updatedCount >= int32(desired) {
		// scale up when inplace updating
		if len(newGameServers) > int(updatedCount) {
			gsSet.Annotations[util.GameServerInPlaceUpdatedReplicasAnnotation] = strconv.Itoa(len(newGameServers))
			_, err = c.carrierClient.CarrierV1alpha1().GameServerSets(gsSet.Namespace).Update(gsSet)
			return err
		}
		return nil
	}
	// two steps of GameServer:
	// 1. Mark NotInService; add annotation: inplaceUpdating: true
	// 2. Update image, remove annotation

	// update game servers
	canUpdates, waitings, runnings := classifyGameServers(oldGameServers, true)
	var candidates []*carrierv1alpha1.GameServer
	candidates = append(candidates, sortGameServersByCreationTime(canUpdates)...)
	candidates = append(candidates, sortGameServersByCreationTime(waitings)...)
	candidates = append(candidates, sortGameServersByCreationTime(runnings)...)
	if diff > len(candidates) {
		diff = len(candidates)
	}
	candidates = candidates[0:diff]

	if err = c.markGameServersOutOfService(gsSet, candidates, func(gs *carrierv1alpha1.GameServer) {
		gameservers.SetInPlaceUpdatingStatus(gs, "true")
	}); err != nil {
		return err
	}

	updated, inPlaceErr := c.inplaceUpdateGameServers(gsSet, candidates)
	// updated is from api(source of truth).
	// make sure update GameServerSet success or failed after retry.
	// if retry failed, make sure the cache has synced.
	err = wait.PollImmediate(50*time.Millisecond, 1*time.Second, func() (done bool, err error) {
		gsSet.Annotations[util.GameServerInPlaceUpdatedReplicasAnnotation] = strconv.Itoa(int(updated + updatedCount))
		_, err = c.carrierClient.CarrierV1alpha1().GameServerSets(gsSet.Namespace).Update(gsSet)
		if err == nil {
			return true, nil
		}
		if !k8serrors.IsNotFound(err) {
			return false, err
		}
		gsSetCopy, getErr := c.carrierClient.CarrierV1alpha1().GameServerSets(gsSet.Namespace).Get(gsSet.Name,
			metav1.GetOptions{})
		if getErr != nil {
			return false, nil
		}
		gsSet = gsSetCopy
		return false, nil
	})
	if inPlaceErr != nil {
		return inPlaceErr
	}
	return err
}

func (c *Controller) getOldAndNewReplicas(gsSet *carrierv1alpha1.GameServerSet) ([]*carrierv1alpha1.GameServer,
	[]*carrierv1alpha1.GameServer, error) {
	newGameServers, err := c.gameServerLister.List(labels.SelectorFromSet(
		labels.Set{
			util.GameServerHash:        gsSet.Labels[util.GameServerHash],
			util.GameServerSetLabelKey: gsSet.Name,
		}),
	)
	if err != nil {
		return nil, nil, err
	}
	selector, err := labels.Parse(fmt.Sprintf("%s!=%s,%s=%s", util.GameServerHash,
		gsSet.Labels[util.GameServerHash], util.GameServerSetLabelKey, gsSet.Name))
	if err != nil {
		return nil, nil, err
	}
	oldGameServers, err := c.gameServerLister.List(selector)
	if err != nil {
		return nil, nil, err
	}
	return oldGameServers, newGameServers, nil
}

// computeReconciliationAction computes the action to take to reconcile a GameServerSet set given
// the list of game servers that were found and target replica count.
func (c *Controller) computeReconciliationAction(gsSet *carrierv1alpha1.GameServerSet,
	list []*carrierv1alpha1.GameServer, counts *Counter) (int, []*carrierv1alpha1.GameServer, bool) {
	scaling := IsGameServerSetScaling(gsSet)
	excludeConstraintGS := excludeConstraints(gsSet)
	var upCount int

	var potentialDeletions, toDeleteGameServers []*carrierv1alpha1.GameServer
	for _, gs := range list {
		// GS being deleted don't count.
		if gameservers.IsBeingDeleted(gs) {
			continue
		}
		switch gs.Status.State {
		case "", carrierv1alpha1.GameServerStarting:
			upCount++
		case carrierv1alpha1.GameServerRunning:
			// GameServer has constraint but may still have player.
			// if excludeConstraintGS is true, we exclude this, otherwise, include.
			if gameservers.IsOutOfService(gs) && excludeConstraintGS && !gameservers.IsInPlaceUpdating(gs) {
				klog.V(4).Infof("GameServer %v is out of service and required excludeConstraint", gs.Name)
				continue
			}

			// GameServer is offline, should delete and add new one
			if gameservers.IsDeletableWithGates(gs) {
				toDeleteGameServers = append(toDeleteGameServers, gs)
				klog.V(4).Infof("GameServer %v is out of service and and ready to be delete", gs.Name)
				klog.V(5).Infof("GameServer annotations: %v, label: %v, condition: %+v",
					gs.Annotations, gs.Labels, gs.Status.Conditions)
			} else {
				upCount++
			}
		default:
			klog.Infof("Unknown state")
		}
		potentialDeletions = append(potentialDeletions, gs)
	}
	diff := int(gsSet.Spec.Replicas) - upCount
	var exceedBurst bool
	var toAdd int
	klog.Infof("targetReplicaCount: %v, upcount: %v", int(gsSet.Spec.Replicas), upCount)
	if diff > 0 {
		toAdd = diff
		if toAdd > BurstReplicas {
			toAdd = BurstReplicas
			exceedBurst = true
		}
	} else if diff < 0 {
		// 1. delete not ready
		// 2. delete deletable
		// 3. try delete running
		toDelete := -diff
		if scaling {
			candidates := make([]*carrierv1alpha1.GameServer, len(potentialDeletions))
			copy(candidates, potentialDeletions)
			deletables, deleteCandidates, runnings := classifyGameServers(candidates, false)
			// sort running gs
			runnings = sortGameServers(runnings, gsSet.Spec.Scheduling, counts)
			// sort Running GameServers for inpalce updating.
			inPlaceUpdating, _ := IsGameServerSetInPlaceUpdating(gsSet)
			if inPlaceUpdating {
				runnings = sortGameServersByHash(runnings, gsSet)
			}
			potentialDeletions = append(deletables, deleteCandidates...)
			potentialDeletions = append(potentialDeletions, runnings...)
			klog.Infof("deletables:%v, deleteCandidates:%v, runnings:%v",
				len(deletables), len(deleteCandidates), len(runnings))
		} else {
			potentialDeletions = sortGameServers(potentialDeletions, gsSet.Spec.Scheduling, counts)
		}

		if len(potentialDeletions) < toDelete {
			toDelete = len(potentialDeletions)
		}
		if toDelete > BurstReplicas {
			toDelete = BurstReplicas
			exceedBurst = true
		}
		toDeleteGameServers = append(toDeleteGameServers, potentialDeletions[0:toDelete]...)
	}
	return toAdd, toDeleteGameServers, exceedBurst
}

// inplaceUpdateGameServers update GameServer spec to api server
func (c *Controller) inplaceUpdateGameServers(gsSet *carrierv1alpha1.GameServerSet,
	toUpdate []*carrierv1alpha1.GameServer) (int32, error) {
	klog.Infof("Updating GameServers: %v, to update %v", gsSet.Name, len(toUpdate))
	if klog.V(5) {
		printGameServerName(toUpdate, "GameServer to in place update:")
	}
	var errs []error
	var count int32 = 0
	workqueue.ParallelizeUntil(context.Background(), BurstReplicas, len(toUpdate), func(piece int) {
		gs := toUpdate[piece]
		gsCopy := gs.DeepCopy()
		var err error
		if !gameservers.CanInPlaceUpdating(gsCopy) {
			return
		}
		// Double check GameServer status, same as `deleteGameServers`。
		if gameservers.IsBeforeRunning(gsCopy) {
			newGS, err := c.carrierClient.CarrierV1alpha1().
				GameServers(gsCopy.Namespace).Get(gs.Name, metav1.GetOptions{})
			if err != nil {
				errs = append(errs, errors.Wrapf(err, "error checking GameServer %s status", gs.Name))
				return
			}
			if gameservers.IsReady(newGS) && gameservers.IsReadinessExist(newGS) {
				klog.Infof("GameServer %v is not before ready now, will not update", gs.Name)
				return
			}
		}
		gsCopy.Status.Conditions = nil
		gsCopy, err = c.carrierClient.CarrierV1alpha1().GameServers(gs.Namespace).UpdateStatus(gsCopy)
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "error updating GameServer %v status for condition", gs.Name))
			return
		}
		updateGameServerSpec(gsSet, gsCopy)
		gs, err = c.carrierClient.CarrierV1alpha1().GameServers(gs.Namespace).Update(gsCopy)
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "error inpalce updating GameServer: %v", gsCopy.Name))
			return
		}
		atomic.AddInt32(&count, 1)
		c.recorder.Eventf(gsSet, corev1.EventTypeNormal,
			"SuccessfulUpdate", "Update GameServer in place success: %v", gs.Name)

	})
	return count, utilerrors.NewAggregate(errs)
}

// createGameServer will add more servers according to diff
func (c *Controller) createGameServers(gsSet *carrierv1alpha1.GameServerSet, count int) error {
	klog.Infof("Adding more GameServers: %v, count: %v", gsSet.Name, count)
	var errs []error
	gs := BuildGameServer(gsSet)
	gameservers.ApplyDefaults(gs)
	workqueue.ParallelizeUntil(context.Background(), BurstReplicas, count, func(piece int) {
		newGS, err := c.carrierClient.CarrierV1alpha1().GameServers(gs.Namespace).Create(gs)
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "error creating GameServer for GameServerSet %s", gsSet.Name))
			return
		}
		c.recorder.Eventf(gsSet, corev1.EventTypeNormal,
			"SuccessfulCreate", "Created GameServer : %s", newGS.Name)
	})
	return utilerrors.NewAggregate(errs)
}

// deleteGameServers delete GameServers. This will double check status before
// we delete the GameServers.
func (c *Controller) deleteGameServers(gsSet *carrierv1alpha1.GameServerSet,
	toDelete []*carrierv1alpha1.GameServer) error {
	klog.Infof("Deleting GameServers: %v, to delete %v", gsSet.Name, len(toDelete))
	if klog.V(5) {
		printGameServerName(toDelete, "GameServer to delete:")
	}
	var errs []error
	workqueue.ParallelizeUntil(context.Background(), BurstReplicas, len(toDelete), func(piece int) {
		gs := toDelete[piece]
		gsCopy := gs.DeepCopy()
		// Double check GameServer status to avoid cache not synced.
		// GameServer status relies on readinessGates of GameServer,
		// whose status is synced through `GameServer Controller`.
		// Case: cache not synced in this controller or
		// `GameServer Controller` updates rate limited, Status is not `Running`.
		// so we take Object from apiserver as source of truth.
		if gameservers.IsBeforeRunning(gsCopy) {
			newGS, err := c.carrierClient.CarrierV1alpha1().
				GameServers(gsCopy.Namespace).Get(gs.Name, metav1.GetOptions{})
			if err != nil {
				errs = append(errs, errors.Wrapf(err, "error checking GameServer %s status", gs.Name))
				return
			}
			if gameservers.IsReady(newGS) && gameservers.IsReadinessExist(newGS) {
				klog.Infof("GameServer %v is not before ready now, will not delete", gs.Name)
				return
			}
		}
		p := metav1.DeletePropagationBackground
		err := c.carrierClient.CarrierV1alpha1().GameServers(gs.Namespace).Delete(gs.Name,
			&metav1.DeleteOptions{PropagationPolicy: &p})
		if err != nil && !k8serrors.IsNotFound(err) {
			return
		}
		c.recorder.Eventf(gsSet, corev1.EventTypeNormal, "SuccessfulDelete",
			"Deleted delatable GameServer in state %s : %v", gs.Status.State, gs.Name)
	})
	return utilerrors.NewAggregate(errs)
}

type opt func(g *carrierv1alpha1.GameServer)

// markGameServersOutOfService marks GameServers not in Service.
func (c *Controller) markGameServersOutOfService(gsSet *carrierv1alpha1.GameServerSet,
	toMark []*carrierv1alpha1.GameServer, opts ...opt) error {
	klog.Infof("Marking GameServers not in service: %v, to mark out of service %v", gsSet.Name, toMark)
	var errs []error
	if klog.V(5) {
		printGameServerName(toMark, "GameServer to mark out of service:")
	}
	klog.Infof("gss %v mark %v", gsSet.Name, len(toMark))
	workqueue.ParallelizeUntil(context.Background(), BurstReplicas, len(toMark), func(piece int) {
		gs := toMark[piece]
		gsCopy := gs.DeepCopy()
		// 1. before running, we delete directly
		// 2. if in place updating in progress, that means already has constraints
		// 3. gs deleting, ignore.
		if gameservers.IsBeforeRunning(gsCopy) ||
			gameservers.IsInPlaceUpdating(gsCopy) || gameservers.IsBeingDeleted(gsCopy) {
			return
		}
		for _, opt := range opts {
			opt(gsCopy)
		}
		// if deletable exist
		if gameservers.IsDeletableExist(gsCopy) {
			gameservers.AddNotInServiceConstraint(gsCopy)
		}
		gsCopy, err := c.carrierClient.CarrierV1alpha1().GameServers(gsCopy.Namespace).Update(gsCopy)
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "error updating GameServer %s to not in service", gs.Name))
			return
		}
		c.recorder.Eventf(gsSet, corev1.EventTypeNormal,
			"Successful Mark ", "Mark GameServer not in service: %v", gs.Name)
	})
	return utilerrors.NewAggregate(errs)
}

// syncGameServerSetStatus synchronises the GameServerSet State with active GameServer counts
func (c *Controller) syncGameServerSetStatus(gsSet *carrierv1alpha1.GameServerSet,
	list []*carrierv1alpha1.GameServer) (*carrierv1alpha1.GameServerSet, error) {
	status := computeStatus(list, gsSet)
	status.Conditions = gsSet.Status.Conditions
	return c.updateStatusIfChanged(gsSet, status)
}

// updateStatusIfChanged updates GameServerSet status if it's different than provided.
func (c *Controller) updateStatusIfChanged(gsSet *carrierv1alpha1.GameServerSet,
	status carrierv1alpha1.GameServerSetStatus) (*carrierv1alpha1.GameServerSet, error) {
	status.ObservedGeneration = gsSet.Generation
	if gsSet.Spec.Selector != nil && gsSet.Spec.Selector.MatchLabels != nil {
		status.Selector = labels.Set(gsSet.Spec.Selector.MatchLabels).String()
	}
	var err error
	if !reflect.DeepEqual(gsSet.Status, status) {
		gsSet.Status = status
		gsSet, err = c.carrierClient.CarrierV1alpha1().GameServerSets(gsSet.Namespace).UpdateStatus(gsSet)
		if err != nil {
			return nil, errors.Wrap(err, "error updating status on GameServerSet")
		}
		return gsSet, nil
	}
	return gsSet, nil
}

// patchGameServerIfChanged  patch GameServerSet if it's different than provided.
func (c *Controller) patchGameServerIfChanged(gsSet *carrierv1alpha1.GameServerSet,
	gsSetCopy *carrierv1alpha1.GameServerSet) (*carrierv1alpha1.GameServerSet, error) {
	if reflect.DeepEqual(gsSet, gsSetCopy) {
		return gsSet, nil
	}
	patch, err := kube.CreateMergePatch(gsSet, gsSetCopy)
	if err != nil {
		return gsSet, err
	}
	klog.V(3).Infof("GameServerSet %v got to scaling: %+v", gsSet.Name, gsSetCopy.Status.Conditions)
	gsSetCopy, err = c.carrierClient.CarrierV1alpha1().GameServerSets(gsSet.Namespace).
		Patch(gsSet.Name, types.MergePatchType, patch, "status")
	if err != nil {
		return nil, errors.Wrapf(err, "error updating status on GameServerSet %s", gsSet.Name)
	}
	klog.V(3).Infof("GameServerSet %v got to scaling: %+v", gsSet.Name, gsSetCopy.Status.Conditions)
	return gsSetCopy, nil
}

// updateGameServerSpec update GameServer spec, include, image and resource.
func updateGameServerSpec(gsSet *carrierv1alpha1.GameServerSet, gs *carrierv1alpha1.GameServer) {
	var image string
	var resources corev1.ResourceRequirements
	gs.Labels[util.GameServerHash] = gsSet.Labels[util.GameServerHash]
	for _, container := range gsSet.Spec.Template.Spec.Template.Spec.Containers {
		if container.Name != util.GameServerContainerName {
			continue
		}
		image = container.Image
		resources = container.Resources
	}
	for i, container := range gs.Spec.Template.Spec.Containers {
		if container.Name != util.GameServerContainerName {
			continue
		}
		gs.Spec.Template.Spec.Containers[i].Image = image
		gs.Spec.Template.Spec.Containers[i].Resources = resources
	}
	gs.Spec.Constraints = nil
	gameservers.SetInPlaceUpdatingStatus(gs, "false")
}

// computeStatus computes the status of the GameServerSet.
func computeStatus(list []*carrierv1alpha1.GameServer,
	gsSet *carrierv1alpha1.GameServerSet) carrierv1alpha1.GameServerSetStatus {
	var status carrierv1alpha1.GameServerSetStatus
	for _, gs := range list {
		if gameservers.IsBeingDeleted(gs) {
			// don't count GS that are being deleted
			continue
		}
		status.Replicas++
		if gs.Status.State != carrierv1alpha1.GameServerRunning {
			continue
		}
		if gameservers.IsDeletableWithGates(gs) {
			continue
		}
		if gameservers.IsOutOfService(gs) &&
			excludeConstraints(gsSet) {
			continue
		}
		if gameservers.IsReady(gs) {
			// do not count GS will be deleted, this GS are not online
			status.ReadyReplicas++
		}
	}
	return status
}

// excludeConstraints return if exclude GameServers with constraint for the GameServerSet
func excludeConstraints(gsSet *carrierv1alpha1.GameServerSet) bool {
	if gsSet.Spec.ExcludeConstraints == nil {
		return false
	}
	return *gsSet.Spec.ExcludeConstraints
}

// classifyGameServers classify the GameServers to deletables, deleteCandidates, runnings
func classifyGameServers(toDelete []*carrierv1alpha1.GameServer, updating bool) (
	deletables, deleteCandidates, runnings []*carrierv1alpha1.GameServer) {
	var inPlaceUpdatings, notReadys []*carrierv1alpha1.GameServer
	for _, gs := range toDelete {
		if gameservers.IsBeingDeleted(gs) {
			continue
		}
		switch {
		case gameservers.IsInPlaceUpdating(gs):
			if updating {
				inPlaceUpdatings = append(inPlaceUpdatings, gs)
			}
		case gameservers.IsBeforeRunning(gs):
			notReadys = append(notReadys, gs)
		case gameservers.IsDeletable(gs):
			deletables = append(deletables, gs)
		case gameservers.IsOutOfService(gs):
			deleteCandidates = append(deleteCandidates, gs)
		default:
			runnings = append(runnings, gs)
		}
	}
	// benefit for sort
	all := append(inPlaceUpdatings, notReadys...)
	deletables = append(all, deletables...)
	return
}

// sortGameServers will sort GameServers.
// 1. Sort according to cost.
// 2. Sort by policy.
func sortGameServers(potentialDeletions []*carrierv1alpha1.GameServer,
	strategy carrierv1alpha1.SchedulingStrategy, counter *Counter) []*carrierv1alpha1.GameServer {
	if len(potentialDeletions) == 0 {
		return potentialDeletions
	}
	potentialDeletions = sortGameServersByCost(potentialDeletions)
	ann := potentialDeletions[0].Annotations
	if cost, _ := GetDeletionCostFromGameServerAnnotations(ann); cost == int64(math.MaxInt64) {
		if strategy == carrierv1alpha1.MostAllocated {
			potentialDeletions = sortGameServersByPodNum(potentialDeletions, counter)
		} else {
			potentialDeletions = sortGameServersByCreationTime(potentialDeletions)
		}
	}
	return potentialDeletions
}

func printGameServerName(list []*carrierv1alpha1.GameServer, prefix string) {
	for _, server := range list {
		klog.Infof("%v %v", prefix, server.Name)
	}
}

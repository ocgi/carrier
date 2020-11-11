// Copyright 2020 THL A29 Limited, a Tencent company.
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
	"sync"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1beta1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"

	"github.com/ocgi/carrier/pkg/apis"
	"github.com/ocgi/carrier/pkg/apis/carrier"
	carrierv1alpha1 "github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/client/clientset/versioned"
	getterv1alpha1 "github.com/ocgi/carrier/pkg/client/clientset/versioned/typed/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/client/informers/externalversions"
	listerv1alpha1 "github.com/ocgi/carrier/pkg/client/listers/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/controllers/gameservers"
	"github.com/ocgi/carrier/pkg/util"
	"github.com/ocgi/carrier/pkg/util/kube"
	"github.com/ocgi/carrier/pkg/util/workerqueue"
)

const (
	maxCreationParalellism         = 16
	maxGameServerCreationsPerBatch = 64

	maxDeletionParallelism         = 64
	maxGameServerDeletionsPerBatch = 64

	// maxPodPendingCount is the maximum number of pending pods per game server set
	maxPodPendingCount = 5000
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
	crdGetter           v1beta1.CustomResourceDefinitionInterface
	gameServerGetter    getterv1alpha1.GameServersGetter
	gameServerLister    listerv1alpha1.GameServerLister
	gameServerSynced    cache.InformerSynced
	gameServerSetGetter getterv1alpha1.GameServerSetsGetter
	gameServerSetLister listerv1alpha1.GameServerSetLister
	gameServerSetSynced cache.InformerSynced
	workerqueue         *workerqueue.WorkerQueue
	stop                <-chan struct{}
	recorder            record.EventRecorder
}

// NewController returns a new gameserverset crd controller
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
		gameServerGetter:    carrierClient.CarrierV1alpha1(),
		gameServerLister:    gameServers.Lister(),
		gameServerSynced:    gsInformer.HasSynced,
		gameServerSetGetter: carrierClient.CarrierV1alpha1(),
		gameServerSetLister: gameServerSets.Lister(),
		gameServerSetSynced: gsSetInformer.HasSynced,
	}

	c.workerqueue = workerqueue.NewWorkerQueueWithRateLimiter(c.syncGameServerSet, carrier.GroupName+".GameServerSetController", workerqueue.FastRateLimiter())
	s := scheme.Scheme
	// Register operator types with the runtime scheme.
	s.AddKnownTypes(carrierv1alpha1.SchemeGroupVersion, &carrierv1alpha1.GameServerSet{})
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})
	c.recorder = eventBroadcaster.NewRecorder(s, corev1.EventSource{Component: "gameserverset-controller"})

	gsSetInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: c.workerqueue.Enqueue,
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldGss := oldObj.(*carrierv1alpha1.GameServerSet)
			newGss := newObj.(*carrierv1alpha1.GameServerSet)
			if oldGss.Spec.Replicas != newGss.Spec.Replicas || !reflect.DeepEqual(oldGss.Annotations, newGss.Annotations) {
				c.workerqueue.Enqueue(newGss)
			}
		},
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

// Run the GameServerSet controller. Will block until stop is closed.
// Runs threadiness number workers to process the rate limited queue
func (c *Controller) Run(workers int, stop <-chan struct{}) error {
	c.stop = stop
	klog.Info("Wait for cache sync")
	if !cache.WaitForCacheSync(stop, c.gameServerSynced, c.gameServerSetSynced) {
		return errors.New("failed to wait for caches to sync")
	}

	c.workerqueue.Run(workers, stop)
	return nil
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
	c.workerqueue.EnqueueImmediately(gsSet)
}

// syncGameServer synchronises the GameServers for the Set,
// making sure there are aways as many GameServers as requested
func (c *Controller) syncGameServerSet(key string) error {
	// Convert the namespace/name string into a distinct namespace and name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		// don't return an error, as we don't want this retried
		runtime.HandleError(errors.Wrapf(err, "invalid resource key"))
		return nil
	}
	klog.V(2).Infof("Sync gameServerSet %v", key)
	gsSet, err := c.gameServerSetLister.GameServerSets(namespace).Get(name)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			klog.V(3).Info("GameServerSet is no longer available for syncing")
			return nil
		}
		return errors.Wrapf(err, "error retrieving GameServerSet %s from namespace %s", name, namespace)
	}
	status := *gsSet.Status.DeepCopy()
	if gsSet.Annotations[util.ScalingReplicasAnnotation] == "true" {
		klog.V(3).Infof("GameServerSet %v required scaling", gsSet.Name)
		status.Conditions = AddScalingStatus(gsSet)
	} else {
		klog.V(3).Infof("GameServerSet %v required not scaling", gsSet.Name)
		status.Conditions = ChangeScalingStatus(gsSet)
	}
	if gsSet, err = c.updateStatusIfChanged(gsSet, status); err != nil {
		return fmt.Errorf("update status of %v failed, error: %v", key, err)
	}
	klog.V(3).Infof("Mark GameServerSet %v scaling condition "+
		"successfully, conditions: %+v", gsSet.Name, gsSet.Status.Conditions)

	list, err := ListGameServersByGameServerSetOwner(c.gameServerLister, gsSet)
	if err != nil {
		return err
	}

	scaling := IsGameServerSetScaling(gsSet)
	klog.Infof("Current game server number of GameServerSet %v: %v", key, len(list))
	numServersToAdd, toDeleteList, isPartial := computeReconciliationAction(gsSet.Spec.Scheduling, list, c.counter,
		int(gsSet.Spec.Replicas), maxGameServerCreationsPerBatch, maxGameServerDeletionsPerBatch, maxPodPendingCount, scaling)
	status = computeStatus(list)
	klog.V(5).Infof("Reconciling GameServerSet name: %v, spec: %v, status: %v", key, gsSet.Spec, status)
	if isPartial {
		// we've determined that there's work to do, but we've decided not to do all the work in one shot
		// make sure we get a follow-up, by re-scheduling this GSS in the worker queue immediately before this
		// function returns
		defer c.workerqueue.EnqueueImmediately(gsSet)
	}
	klog.V(2).Infof("GameSeverSet: %v toAdd: %v, toDelete: %v, list: %+v", key, numServersToAdd, len(toDeleteList), toDeleteList)
	if numServersToAdd > 0 {
		if err := c.addMoreGameServers(gsSet, numServersToAdd); err != nil {
			klog.Errorf("error adding game servers: %v", err)
		}
	}

	if len(toDeleteList) > 0 {
		// GameServers can be deleted directly.
		c.recorder.Eventf(gsSet, corev1.EventTypeNormal, "ToDelete", "Created GameServer: %+v, can delete: %v", len(list), len(toDeleteList))
		if err := c.deleteGameServers(gsSet, toDeleteList); err != nil {
			klog.Errorf("error deleting game servers: %v", err)
			return err
		}
	} else {
		// GameServers must wait before be deleted.
		count := gameServerOutOfServiceCount(list)
		desire := int(status.Replicas - gsSet.Spec.Replicas)
		gap := desire - count
		if gap > 0 {
			// first sort by cost.
			// if not set or an error value, the cost is default int64 max.
			// so we go back to sort by Pod number on node.
			if err = c.markCandidateGameServers(gsSet, list, gap); err != nil {
				return err
			}
		}
	}

	// scale success or no action
	if len(toDeleteList) == int(status.Replicas-gsSet.Spec.Replicas) {
		gsSetCopy := gsSet.DeepCopy()
		gsSetCopy.Status.Conditions = ChangeScalingStatus(gsSet)
		if gsSet, err = c.patchGameServerIfChanged(gsSet, gsSetCopy); err != nil {
			return err
		}
		gsSetCopy = gsSet.DeepCopy()
		if _, ok := gsSet.Annotations[util.ScalingReplicasAnnotation]; ok {
			delete(gsSet.Annotations, util.ScalingReplicasAnnotation)
			if gsSet, err = c.gameServerSetGetter.GameServerSets(gsSet.Namespace).Update(gsSet); err != nil {
				return err
			}
		}
	}
	klog.V(3).Infof("GameServerSet %v remove annotation success", gsSet.Name)
	gsSet, err = c.syncGameServerSetStatus(gsSet, list)
	if err != nil {
		klog.Error(err)
		return err
	}
	if status.Replicas-int32(len(toDeleteList))+int32(numServersToAdd) != gsSet.Spec.Replicas {
		return fmt.Errorf("GameServerSet %v actual replicas: %v, desired: %v, to delete %v, to add: %v", key,
			gsSet.Status.Replicas, gsSet.Spec.Replicas, len(toDeleteList),
			numServersToAdd)
	}
	return err
}

// computeReconciliationAction computes the action to take to reconcile a game server set set given
// the list of game servers that were found and target replica count.
func computeReconciliationAction(strategy apis.SchedulingStrategy, list []*carrierv1alpha1.GameServer,
	counts *Counter, targetReplicaCount int, maxCreations int, maxDeletions int,
	maxPending int, scaling bool) (int, []*carrierv1alpha1.GameServer, bool) {
	var upCount int     // up == Ready or will become ready
	var deleteCount int // number of gameservers to delete

	// track the number of pods that are being created at any given moment by the GameServerSet
	// so we can limit it at a throughput that Kubernetes can handle
	var podPendingCount int // podPending == "up" but don't have a Pod running yet

	var potentialDeletions []*carrierv1alpha1.GameServer
	var toDelete []*carrierv1alpha1.GameServer

	scheduleDeletion := func(gs *carrierv1alpha1.GameServer) {
		toDelete = append(toDelete, gs)
		deleteCount--
	}

	handleGameServerUp := func(gs *carrierv1alpha1.GameServer) {
		if upCount >= targetReplicaCount {
			deleteCount++
		} else {
			upCount++
		}

		// Track gameservers that could be potentially deleted
		potentialDeletions = append(potentialDeletions, gs)
	}

	for _, gs := range list {
		// GS being deleted don't count.
		if gameservers.IsBeingDeleted(gs) {
			continue
		}

		switch gs.Status.State {
		case "", carrierv1alpha1.GameServerStarting:
			podPendingCount++
			handleGameServerUp(gs)
		case carrierv1alpha1.GameServerExited, carrierv1alpha1.GameServerFailed:
			scheduleDeletion(gs)
		case carrierv1alpha1.GameServerUnknown:
		default:
			// unrecognized state, assume it's up.
			handleGameServerUp(gs)
		}
	}

	var partialReconciliation bool
	var numServersToAdd int
	klog.Infof("targetReplicaCount: %v, upcount: %v, deleteCount: %v", targetReplicaCount, upCount, deleteCount)
	if upCount < targetReplicaCount {
		numServersToAdd = targetReplicaCount - upCount
		originalNumServersToAdd := numServersToAdd

		if numServersToAdd > maxCreations {
			numServersToAdd = maxCreations
		}

		if numServersToAdd+podPendingCount > maxPending {
			numServersToAdd = maxPending - podPendingCount
			if numServersToAdd < 0 {
				numServersToAdd = 0
			}
		}

		if originalNumServersToAdd != numServersToAdd {
			partialReconciliation = true
		}
	}

	if deleteCount > 0 {
		if scaling {
			potentialDeletions = filteredGameServers(potentialDeletions)
		}
		potentialDeletions = sortGameServers(potentialDeletions, strategy, counts)
		if len(potentialDeletions) < deleteCount {
			deleteCount = len(potentialDeletions)
		}

		toDelete = append(toDelete, potentialDeletions[0:deleteCount]...)
	}

	if len(toDelete) > maxDeletions {
		toDelete = toDelete[0:maxDeletions]
		partialReconciliation = true
	}

	return numServersToAdd, toDelete, partialReconciliation
}

// addMoreGameServers adds diff more GameServers to the set
func (c *Controller) addMoreGameServers(gsSet *carrierv1alpha1.GameServerSet, count int) error {
	klog.Infof("Adding more gameservers: %v, count: %v", gsSet.Name, count)
	var errs []error
	gs := GameServer(gsSet)
	gameservers.ApplyDefaults(gs)
	workqueue.ParallelizeUntil(context.Background(), maxCreationParalellism, count, func(piece int) {
		newGS, err := c.gameServerGetter.GameServers(gs.Namespace).Create(gs)
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "error creating gameserver for gameserverset %s", gsSet.Name))
			return
		}
		c.recorder.Eventf(gsSet, corev1.EventTypeNormal, "SuccessfulCreate", "Created gameserver: %s", newGS.Name)
	})
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

func (c *Controller) deleteGameServers(gsSet *carrierv1alpha1.GameServerSet, toDelete []*carrierv1alpha1.GameServer) error {
	klog.Infof("Deleting gameservers: %v, to delete %v", gsSet.Name, len(toDelete))
	var errs []error
	workqueue.ParallelizeUntil(context.Background(), maxDeletionParallelism, len(toDelete), func(piece int) {
		gs := toDelete[piece]
		gsCopy := gs.DeepCopy()
		gsCopy.Status.State = carrierv1alpha1.GameServerExited

		_, err := c.gameServerGetter.GameServers(gsCopy.Namespace).UpdateStatus(gsCopy)
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "error updating gameserver %s from status %s to exited status", gs.Name, gs.Status.State))
			return
		}

		c.recorder.Eventf(gsSet, corev1.EventTypeNormal, "SuccessfulDelete", "Deleted gameserver in state %s: %v", gs.Status.State, gs.Name)
	})
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

func (c *Controller) markGameServersOutOfService(gsSet *carrierv1alpha1.GameServerSet, toMark []*carrierv1alpha1.GameServer) error {
	klog.Infof("Makring gameservers not in servce: %v, to mark out of service %v", gsSet.Name, toMark)
	var errs []error
	workqueue.ParallelizeUntil(context.Background(), maxDeletionParallelism, len(toMark), func(piece int) {
		gs := toMark[piece]
		gsCopy := gs.DeepCopy()
		gameservers.AddNotInServiceConstraint(gsCopy)
		_, err := c.gameServerGetter.GameServers(gsCopy.Namespace).Update(gsCopy)
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "error updating game server %s to not in service", gs.Name))
			return
		}
		c.recorder.Eventf(gsSet, corev1.EventTypeNormal, "Successful Mark ", "Mark game server not in service: %v", gs.Name)
	})
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// syncGameServerSetStatus synchronises the GameServerSet State with active GameServer counts
func (c *Controller) syncGameServerSetStatus(gsSet *carrierv1alpha1.GameServerSet, list []*carrierv1alpha1.GameServer) (*carrierv1alpha1.GameServerSet, error) {
	status := computeStatus(list)
	status.Conditions = gsSet.Status.Conditions
	return c.updateStatusIfChanged(gsSet, status)
}

// updateStatusIfChanged updates GameServerSet status if it's different than provided.
func (c *Controller) updateStatusIfChanged(gsSet *carrierv1alpha1.GameServerSet, status carrierv1alpha1.GameServerSetStatus) (*carrierv1alpha1.GameServerSet, error) {
	status.ObservedGeneration = gsSet.Generation
	if gsSet.Spec.Selector != nil && gsSet.Spec.Selector.MatchLabels != nil {
		status.Selector = labels.Set(gsSet.Spec.Selector.MatchLabels).String()
	}
	var err error
	if !reflect.DeepEqual(gsSet.Status, status) {
		gsSet.Status = status
		gsSet, err = c.gameServerSetGetter.GameServerSets(gsSet.Namespace).UpdateStatus(gsSet)
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
	gsSetCopy, err = c.gameServerSetGetter.GameServerSets(gsSet.Namespace).Patch(gsSet.Name, types.MergePatchType, patch, "status")
	if err != nil {
		return nil, errors.Wrapf(err, "error updating status on GameServerSet %s", gsSet.Name)
	}
	klog.V(3).Infof("GameServerSet %v got to scaling: %+v", gsSet.Name, gsSetCopy.Status.Conditions)
	return gsSetCopy, nil
}

// markCandidateGameServers mark game server to be deleted
func (c *Controller) markCandidateGameServers(gsSet *carrierv1alpha1.GameServerSet,
	list []*carrierv1alpha1.GameServer, candidateNumber int) error {
	potentialList := sortGameServers(list, gsSet.Spec.Scheduling, c.counter)
	if len(potentialList) > candidateNumber {
		potentialList = potentialList[0:candidateNumber]
	}

	if err := c.markGameServersOutOfService(gsSet, potentialList); err != nil {
		klog.Errorf("error marking game servers out of service: %v", err)
		return err
	}
	return nil
}

// computeStatus computes the status of the game server set.
func computeStatus(list []*carrierv1alpha1.GameServer) carrierv1alpha1.GameServerSetStatus {
	var status carrierv1alpha1.GameServerSetStatus
	for _, gs := range list {
		if gameservers.IsBeingDeleted(gs) {
			// don't count GS that are being deleted
			continue
		}

		status.Replicas++
		switch gs.Status.State {
		case carrierv1alpha1.GameServerRunning:
			status.ReadyReplicas++
		}
	}
	return status
}

// filteredGameServers filters the GameServers should not be deleted
func filteredGameServers(toDelete []*carrierv1alpha1.GameServer) []*carrierv1alpha1.GameServer {
	var filtered []*carrierv1alpha1.GameServer
	for _, gs := range toDelete {
		klog.V(4).Infof("Go through %v/%v, condition: %+v", gs.Namespace, gs.Name, gs.Status.Conditions)
		if gameservers.IsBeforeReady(gs) {
			klog.V(4).Infof("Add %v/%v", gs.Namespace, gs.Name)
			filtered = append(filtered, gs)
			continue
		}
		if gameservers.IsDeletable(gs) {
			klog.V(4).Infof("Add %v/%v", gs.Namespace, gs.Name)
			filtered = append(filtered, gs)
		}
		continue
	}
	return filtered
}

func gameServerOutOfServiceCount(gsList []*carrierv1alpha1.GameServer) int {
	count := 0
	for _, gs := range gsList {
		for _, constraint := range gs.Spec.Constraints {
			if constraint.Type == carrierv1alpha1.NotInService &&
				constraint.Effective != nil && *constraint.Effective == true {
				count++
				break
			}
		}
	}
	return count
}

func sortGameServers(potentialDeletions []*carrierv1alpha1.GameServer, strategy apis.SchedulingStrategy, counter *Counter) []*carrierv1alpha1.GameServer {
	if len(potentialDeletions) == 0 {
		return potentialDeletions
	}
	potentialDeletions = sortGameServersByCost(potentialDeletions)
	if cost, _ := GetDeletionCostFromGameServerAnnotations(potentialDeletions[0].Annotations); cost == int64(math.MaxInt64) {
		if strategy == apis.MostAllocated {
			potentialDeletions = sortGameServersByPodNum(potentialDeletions, counter)
		} else {
			potentialDeletions = sortGameServersByCreationTime(potentialDeletions)
		}
	}
	return potentialDeletions
}

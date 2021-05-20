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

package gameservers

import (
	"fmt"
	"reflect"
	"sync"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog"

	"github.com/ocgi/carrier/pkg/apis/carrier"
	carrierv1alpha1 "github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/client/clientset/versioned"
	"github.com/ocgi/carrier/pkg/client/informers/externalversions"
	listerv1 "github.com/ocgi/carrier/pkg/client/listers/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/util"
	"github.com/ocgi/carrier/pkg/util/workerqueue"
)

const (
	sdkserverSidecarName = "carrier-gameserver-sidecar"
	grpcPort             = "CARRIER_SDK_GRPC_PORT"
	httpPort             = "CARRIER_SDK_HTTP_PORT"
)

// Sidecar creates the sidecar container for a given GameServer
type Sidecar func(*carrierv1alpha1.GameServer) corev1.Container

// Controller is a the main GameServer crd controller
type Controller struct {
	podLister          corelisterv1.PodLister
	podSynced          cache.InformerSynced
	gameServerLister   listerv1.GameServerLister
	gameServerSynced   cache.InformerSynced
	nodeLister         corelisterv1.NodeLister
	nodeSynced         cache.InformerSynced
	updateQueue        *workerqueue.WorkerQueue
	deleteQueue        *workerqueue.WorkerQueue // handles deletion only
	nodeTaintWorkQueue *workerqueue.WorkerQueue // handles node autoscaler taint only
	kubeClient         kubernetes.Interface
	carrierClient      versioned.Interface
	stop               <-chan struct{}
	recorder           record.EventRecorder
	serviceAccount     string
	sidecar            Sidecar
}

// NewController returns a new GameServer crd controller
func NewController(
	kubeClient kubernetes.Interface,
	kubeInformerFactory informers.SharedInformerFactory,
	carrierClient versioned.Interface,
	carrierInformerFactory externalversions.SharedInformerFactory, serviceAccount string, sidecar Sidecar) *Controller {

	pods := kubeInformerFactory.Core().V1().Pods()
	gameServers := carrierInformerFactory.Carrier().V1alpha1().GameServers()
	gsInformer := gameServers.Informer()
	nodeInformer := kubeInformerFactory.Core().V1().Nodes()

	c := &Controller{
		podLister:        pods.Lister(),
		podSynced:        pods.Informer().HasSynced,
		gameServerLister: gameServers.Lister(),
		gameServerSynced: gsInformer.HasSynced,
		nodeLister:       nodeInformer.Lister(),
		nodeSynced:       nodeInformer.Informer().HasSynced,
		kubeClient:       kubeClient,
		carrierClient:    carrierClient,
		serviceAccount:   serviceAccount,
		sidecar:          sidecar,
	}

	s := scheme.Scheme
	// Register operator types with the runtime scheme.
	s.AddKnownTypes(carrierv1alpha1.SchemeGroupVersion, &carrierv1alpha1.GameServer{})

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})
	c.recorder = eventBroadcaster.NewRecorder(s, corev1.EventSource{Component: "gameserver-controller"})

	c.updateQueue = workerqueue.NewWorkerQueueWithRateLimiter(c.syncGameServerUpdate, carrier.GroupName+".GameServerController", workerqueue.FastRateLimiter())
	c.deleteQueue = workerqueue.NewWorkerQueueWithRateLimiter(c.syncGameServerDelete, carrier.GroupName+".GameServerControllerDeletion",
		workerqueue.FastRateLimiter())
	c.nodeTaintWorkQueue = workerqueue.NewWorkerQueue(c.syncNodeTaint, carrier.GroupName+".GameServerNodeTaintAction")
	gsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: c.enqueueGameServerBasedOnState,
		UpdateFunc: func(oldObj, newObj interface{}) {
			newGs := newObj.(*carrierv1alpha1.GameServer)
			c.enqueueGameServerBasedOnState(newGs)
		},
	})

	pods.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldPod := oldObj.(*corev1.Pod)
			newPod := newObj.(*corev1.Pod)
			if isGameServerPod(oldPod) {
				// pod scheduled
				// container status change
				if oldPod.Spec.NodeName != newPod.Spec.NodeName || !reflect.DeepEqual(oldPod.Status.ContainerStatuses, newPod.Status.ContainerStatuses) {
					owner := metav1.GetControllerOf(newPod)
					c.updateQueue.Enqueue(cache.ExplicitKey(newPod.Namespace + "/" + owner.Name))
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			// ignore DeletedFinalStateUnknown
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				return
			}
			if isGameServerPod(pod) {
				owner := metav1.GetControllerOf(pod)
				c.updateQueue.Enqueue(cache.ExplicitKey(pod.Namespace + "/" + owner.Name))
			}
		},
	})

	nodeInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			node, ok := obj.(*corev1.Node)
			if !ok {
				return false
			}
			if !checkNodeTaintByCA(node) {
				return false
			}
			return true
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				node := obj.(*corev1.Node)
				if !checkNodeTaintByCA(node) {
					return
				}
				c.nodeTaintWorkQueue.Enqueue(node)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				oldNode, ok := oldObj.(*corev1.Node)
				if !ok {
					return
				}
				newNode := newObj.(*corev1.Node)
				// old node does not have the taint
				// new node have the taint.
				if checkNodeTaintByCA(oldNode) || !checkNodeTaintByCA(newNode) {
					return
				}
				c.nodeTaintWorkQueue.Enqueue(newNode)
			},
		},
	})

	return c
}

func (c *Controller) syncNodeTaint(nodeName string) error {
	klog.Infof("Sync node taint %v", nodeName)
	fieldSelector, err := fields.ParseSelector("spec.nodeName=" + nodeName)
	if err != nil {
		return err
	}
	pods, err := c.kubeClient.CoreV1().Pods(corev1.NamespaceAll).List(metav1.ListOptions{
		FieldSelector: fieldSelector.String(),
	})
	if err != nil {
		return err
	}
	if pods == nil {
		return nil
	}
	klog.Infof("List %v pods whose nodeName is %v", len(pods.Items), nodeName)
	for _, pod := range pods.Items {
		klog.V(5).Infof("Go through pod %v/%v", pod.Namespace, pod.Name)
		gs, err := c.gameServerLister.GameServers(pod.Namespace).Get(pod.Name)
		if k8serrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		klog.V(4).Infof("Add NotInServiceConstraint for gs %v/%v", gs.Namespace, gs.Name)
		AddNotInServiceConstraint(gs)
		gs, err = c.carrierClient.CarrierV1alpha1().GameServers(gs.Namespace).Update(gs)
		if err != nil {
			klog.Error(err)
			return errors.Wrap(err, "error updating GameServer to not in service")
		}

	}
	return nil
}

func (c *Controller) enqueueGameServerBasedOnState(item interface{}) {
	gs := item.(*carrierv1alpha1.GameServer)
	klog.Infof("Game server %+v enqueue", gs.Name)
	switch gs.Status.State {
	case carrierv1alpha1.GameServerFailed, carrierv1alpha1.GameServerExited:
		c.deleteQueue.Enqueue(gs)

	default:
		c.updateQueue.Enqueue(gs)
	}
}

// Run the GameServer controller. Will block until stop is closed.
// Runs threadiness number workers to process the rate limited queue
func (c *Controller) Run(workers int, stop <-chan struct{}) error {
	c.stop = stop
	klog.V(4).Info("Wait for cache sync")
	if !cache.WaitForCacheSync(stop, c.gameServerSynced, c.podSynced, c.nodeSynced) {
		return errors.New("failed to wait for caches to sync")
	}

	// start work queues
	var wg sync.WaitGroup

	startWorkQueue := func(wq *workerqueue.WorkerQueue) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wq.Run(workers, stop)
		}()
	}
	for _, queue := range []*workerqueue.WorkerQueue{
		c.updateQueue, c.deleteQueue, c.nodeTaintWorkQueue,
	} {
		startWorkQueue(queue)
	}
	wg.Wait()
	return nil
}

// syncGameServerUpdate reconciles GameServer status base on pod and node status.
func (c *Controller) syncGameServerUpdate(key string) error {
	klog.V(4).Infof("Sync GameServer %v", key)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}

	gs, err := c.gameServerLister.GameServers(namespace).Get(name)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			klog.V(4).Info("GameServer is no longer available for syncing")
			return nil
		}
		return errors.Wrapf(err, "error retrieving GameServer %s from namespace %s", name, namespace)
	}

	if gs.Status.State == carrierv1alpha1.GameServerExited || gs.Status.State == carrierv1alpha1.GameServerFailed {
		klog.V(3).Infof("GameServer %v is no longer available for syncing, state: %v", gs.Name, gs.Status.State)
		return nil
	}

	gsCopy := gs.DeepCopy()
	if gs, err = c.syncGameServerDeletionTimestamp(gsCopy); err != nil {
		klog.Errorf("Failed sync GameServer: %v deletion time, error: %v", key, err)
		return err
	}
	if gs, err = c.syncGameServerStartingState(gsCopy); err != nil {
		klog.Errorf("Failed sync GameServer: %v starting state, error: %v", key, err)

		return err
	}
	if gs, err = c.syncGameServerRunningState(gsCopy); err != nil {
		klog.Errorf("Failed sync GameServer: %v running state, error: %v", key, err)

		return err
	}
	return nil
}

// syncGameServerDelete reconciles GameServer status base on pod and node status.
func (c *Controller) syncGameServerDelete(key string) error {
	klog.V(4).Infof("Sync delete GameServer %v", key)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}

	gs, err := c.gameServerLister.GameServers(namespace).Get(name)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			klog.V(4).Info("GameServer is no longer available for syncing")
			return nil
		}
		return errors.Wrapf(err, "error retrieving GameServer %s from namespace %s", name, namespace)
	}
	if gs, err = c.syncGameServerDeletionTimestamp(gs); err != nil {
		return err
	}
	if err = c.syncGameServerExitedAndFailedState(gs); err != nil {
		klog.Error(err)
		return err
	}
	return nil
}

// syncGameServerDeletionTimestamp if the deletion timestamp is non-zero
// - if there are no pods or terminating, remove the finalizer
func (c *Controller) syncGameServerDeletionTimestamp(gs *carrierv1alpha1.GameServer) (*carrierv1alpha1.GameServer, error) {
	klog.V(4).Infof("Sync deletion timestamp for GameServer: %v", gs.Name)
	if gs.DeletionTimestamp == nil {
		return gs, nil
	}
	pod, err := c.getGameServerPod(gs)
	if err != nil && !k8serrors.IsNotFound(err) {
		return gs, err
	}

	if pod != nil && pod.DeletionTimestamp == nil {
		if err = c.kubeClient.CoreV1().Pods(pod.Namespace).Delete(pod.Name, &metav1.DeleteOptions{}); err != nil {
			return gs, errors.Wrapf(err, "error deleting pod for GameServer. Name: %s, Namespace: %s", gs.Name, pod.Namespace)
		}
		c.recorder.Event(gs, corev1.EventTypeNormal, string(gs.Status.State), fmt.Sprintf("Deleting Pod %s", pod.Name))
	}

	var fin []string
	for _, f := range gs.Finalizers {
		if f != carrier.GroupName {
			fin = append(fin, f)
		}
	}
	gs.Finalizers = fin
	klog.Infof("No pods of GameServer %v found, removing finalizer %s", gs.Name, carrier.GroupName)
	gs, err = c.carrierClient.CarrierV1alpha1().GameServers(gs.Namespace).Update(gs)
	return gs, errors.Wrap(err, "error removing finalizer for GameServer")
}

// syncGameServerStartingState checks if the GameServer is in the Creating state, and if so
// creates a Pod for the GameServer and moves the state to Starting
func (c *Controller) syncGameServerStartingState(gs *carrierv1alpha1.GameServer) (*carrierv1alpha1.GameServer, error) {
	klog.V(4).Infof("Start sync start state for: %v", gs.Name)
	if !gs.DeletionTimestamp.IsZero() {
		return gs, nil
	}
	// Maybe something went wrong, and the pod was created, but the state was never moved to Starting, so let's check
	pod, err := c.getGameServerPod(gs)
	if k8serrors.IsNotFound(err) {
		klog.V(4).Infof("Start creating pod for GameServer:%v", gs.Name)
		return c.createGameServerPod(gs)
	}
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if pod.Labels[util.GameServerHash] != gs.Labels[util.GameServerHash] {
		podCopy := pod.DeepCopy()
		updatePodSpec(gs, podCopy)
		pod, err = c.kubeClient.CoreV1().Pods(podCopy.Namespace).Update(podCopy)
		if err != nil {
			c.recorder.Event(gs, corev1.EventTypeWarning, string(gs.Status.State),
				fmt.Sprintf("Pod %v controlled by GameServer failed updated, reason: %v", gs.Name, err))
			return gs, err
		}
	}

	if gs.Status.State == carrierv1alpha1.GameServerRunning && pod.Status.Phase == corev1.PodRunning {
		return gs, nil
	}
	if gs.Status.State == carrierv1alpha1.GameServerStarting {
		return gs, nil
	}
	gs.Status.State = carrierv1alpha1.GameServerStarting
	gs, err = c.carrierClient.CarrierV1alpha1().GameServers(gs.Namespace).UpdateStatus(gs)
	if err != nil {
		return gs, errors.Wrap(err, "error updating GameServer to Starting state")
	}
	c.recorder.Event(gs, corev1.EventTypeNormal, string(gs.Status.State),
		fmt.Sprintf("Pod %v controlled by GameServer created", gs.Name))
	return gs, nil
}

// syncGameServerRunningState reconciles the GameServer. We will do following:
// 1. Add ready container to annotation
// 2. Add pod nodeName and port to GameServer Status(if using host port)
// 3. Check node name address changed
// 4. Check pod status
func (c *Controller) syncGameServerRunningState(gs *carrierv1alpha1.GameServer) (*carrierv1alpha1.GameServer, error) {
	klog.V(4).Infof("Start sync running state for: %v", gs.Name)
	if !gs.DeletionTimestamp.IsZero() {
		return gs, nil
	}

	pod, err := c.getGameServerPod(gs)
	if err != nil {
		return gs, err
	}
	oldHash := pod.Labels[util.GameServerHash]
	newHash := gs.Labels[util.GameServerHash]
	if oldHash != newHash || len(oldHash) == 0 && len(newHash) == 0 {
		klog.V(4).Infof("hash not equal start update %v", pod.Name)
		podCopy := pod.DeepCopy()
		updatePodSpec(gs, podCopy)
		pod, err = c.kubeClient.CoreV1().Pods(podCopy.Namespace).Update(podCopy)
		if err != nil {
			return gs, err
		}
	}

	switch gs.Status.State {
	case carrierv1alpha1.GameServerExited, carrierv1alpha1.GameServerFailed, carrierv1alpha1.GameServerUnknown:
		return gs, nil
	case carrierv1alpha1.GameServerStarting, carrierv1alpha1.GameServerRunning:
		klog.V(5).Infof("Starting reconcile state: %v", gs.Status.State)
	default:
		klog.Warningf("Found unexpected state: %v", gs.Status.State)
		return gs, nil
	}

	changed, err := getReadyContainer(gs, pod)
	if err != nil {
		return gs, err
	}
	if changed {
		gs, err = c.carrierClient.CarrierV1alpha1().GameServers(gs.Namespace).Update(gs)
		if err != nil {
			return gs, errors.Wrapf(err, "error setting ready container on GameServer %s Status", pod.Name)
		}
	}
	updated := false
	nodeName := pod.Spec.NodeName
	if len(nodeName) == 0 {
		return gs, fmt.Errorf("pod of GameServer: %v has not been schedulerd", gs.Name)
	}
	_, err = c.nodeLister.Get(nodeName)
	if err != nil {
		return gs, errors.Wrapf(err, "error retrieving node %s for Pod %s", pod.Spec.NodeName, pod.Name)
	}
	klog.Infof("Old GameServer %v state: %v, address: %v, node name: %v",
		gs.Name, gs.Status.State, gs.Status.Address, gs.Status.NodeName)
	gsStatusCopy := gs.Status.DeepCopy()
	// reconcile GameServer Address
	if gs, updated, err = c.reconcileGameServerAddress(gs, pod); err != nil {
		return gs, errors.Wrapf(err, "failed to update addrsss of %v", pod.Name)
	}
	c.reconcileGameServerState(gs, pod)
	klog.Infof("New GameServer %v state: %v, address: %v, node name: %v",
		gs.Name, gs.Status.State, gs.Status.Address, gs.Status.NodeName)
	if reflect.DeepEqual(gsStatusCopy, gs.Status) {
		return gs, nil
	}
	gs, err = c.carrierClient.CarrierV1alpha1().GameServers(gs.Namespace).UpdateStatus(gs)
	if err != nil {
		return gs, errors.Wrapf(err, "failed to update status of %v after reconcile state", pod.Name)
	}
	klog.V(4).Infof("Game server %v status: %v", gs.Name, gs.Status.State)
	if updated {
		c.recorder.Event(gs, corev1.EventTypeNormal, string(gs.Status.State), "Address and port populated")
	}
	if gs.Status.State == carrierv1alpha1.GameServerRunning {
		c.recorder.Event(gs, corev1.EventTypeNormal, string(gs.Status.State), "Waiting for receiving ready message")
	}
	return gs, nil
}

// syncGameServerExitAndFailedState deletes the GameServer and them delete pod through gc
func (c *Controller) syncGameServerExitedAndFailedState(gs *carrierv1alpha1.GameServer) error {
	if gs.Status.State != carrierv1alpha1.GameServerExited && gs.Status.State != carrierv1alpha1.GameServerFailed {
		return nil
	}
	klog.V(4).Info("Syncing Exited State for GameServer: %", gs.Name)
	// be explicit about where to delete.
	p := metav1.DeletePropagationBackground
	err := c.carrierClient.CarrierV1alpha1().GameServers(gs.Namespace).Delete(gs.Name, &metav1.DeleteOptions{PropagationPolicy: &p})
	if err != nil && !k8serrors.IsNotFound(err) {
		return errors.Wrapf(err, "error deleting GameServer %s", gs.Name)
	}
	c.recorder.Event(gs, corev1.EventTypeNormal, string(gs.Status.State),
		fmt.Sprintf("Deletion of GameServer %v started", gs.Name))
	return nil
}

// removeConstraintsFromGameServer removes constraints from GameServer migrated.
func (c *Controller) removeConstraintsFromGameServer(gs *carrierv1alpha1.GameServer) (*carrierv1alpha1.GameServer, error) {
	constraints := make([]carrierv1alpha1.Constraint, 0)
	for _, constraint := range gs.Spec.Constraints {
		if constraint.Type == carrierv1alpha1.NotInService {
			continue
		}
		constraints = append(constraints, constraint)
	}
	if len(constraints) == len(gs.Spec.Constraints) {
		return gs, nil
	}
	gs.Spec.Constraints = constraints
	return c.carrierClient.CarrierV1alpha1().GameServers(gs.Namespace).Update(gs)
}

// createGameServerPod creates the backing Pod for a given GameServer
func (c *Controller) createGameServerPod(gs *carrierv1alpha1.GameServer) (*carrierv1alpha1.GameServer, error) {
	sidecar := c.sidecar(gs)
	pod, err := buildPod(gs, c.serviceAccount, sidecar)
	if err != nil {
		// this shouldn't happen, but if it does.
		klog.Errorf("error creating pod from GameServer %v%v", gs.Namespace, gs.Name)
		gs, err = c.moveToFailedState(gs, err.Error())
		return gs, err
	}

	klog.V(4).Infof("Creating pod: %v for GameServer", pod.Name)
	pod, err = c.kubeClient.CoreV1().Pods(gs.Namespace).Create(pod)
	if err != nil {
		switch {
		case k8serrors.IsAlreadyExists(err):
			c.recorder.Event(gs, corev1.EventTypeNormal, string(gs.Status.State), "Pod already exists, reused")
			return gs, nil
		case k8serrors.IsInvalid(err):
			klog.Errorf("Pod %v created is invalid", pod)
			gs, err = c.moveToFailedState(gs, err.Error())
			return gs, fmt.Errorf("failed to move failed state error: %v", err)
		case k8serrors.IsForbidden(err):
			klog.Errorf("Pod %v created is forbidden", pod)
			gs, err = c.moveToFailedState(gs, err.Error())
			return gs, fmt.Errorf("failed to move failed state error: %v", err)
		default:
			klog.Errorf("Pod err: %v", err)
			c.recorder.Eventf(gs, corev1.EventTypeWarning, string(gs.Status.State), "error creating Pod for GameServer %s", gs.Name)
			return gs, errors.Wrapf(err, "error creating Pod for GameServer %s", gs.Name)
		}
	}
	c.recorder.Event(gs, corev1.EventTypeNormal, string(gs.Status.State),
		fmt.Sprintf("Creating pod %s", pod.Name))

	return gs, nil
}

// moveToFailedState moves the GameServer to the error state
func (c *Controller) moveToFailedState(gs *carrierv1alpha1.GameServer, msg string) (*carrierv1alpha1.GameServer, error) {
	var err error
	gs.Status.State = carrierv1alpha1.GameServerFailed
	gs, err = c.carrierClient.CarrierV1alpha1().GameServers(gs.Namespace).UpdateStatus(gs)
	if err != nil {
		return gs, err
	}

	c.recorder.Event(gs, corev1.EventTypeWarning, string(gs.Status.State), msg)
	return gs, nil
}

// getGameServerPod returns the Pod for a GameServer.
// If a pod has the same name but is not controlled by gs, return error.
func (c *Controller) getGameServerPod(gs *carrierv1alpha1.GameServer) (*corev1.Pod, error) {
	pod, err := c.podLister.Pods(gs.Namespace).Get(gs.Name)

	// if not found, propagate this error up, so we can use it in checks
	if k8serrors.IsNotFound(err) {
		return nil, err
	}

	if !metav1.IsControlledBy(pod, gs) {
		return nil, k8serrors.NewNotFound(corev1.Resource("pod"), gs.Name)
	}
	if err != nil {
		return nil, errors.Wrapf(err, "error retrieving pod for GameServer %s", gs.Name)
	}
	return pod, nil
}

func (c *Controller) reconcileGameServerAddress(gs *carrierv1alpha1.GameServer, pod *corev1.Pod) (*carrierv1alpha1.GameServer, bool, error) {
	updated := false
	if gs.Status.NodeName == "" || gs.Status.Address == "" {
		updated = true
		applyGameServerAddressAndPort(gs, pod)
		return gs, updated, nil
	}
	address := pod.Status.PodIP
	if pod.Spec.NodeName != gs.Status.NodeName || address != gs.Status.Address {
		updated = true
		var eventMsg string
		if IsBeforeReady(gs) {
			var err error
			gs, err = c.removeConstraintsFromGameServer(gs)
			if err != nil {
				return gs, updated, err
			}
			applyGameServerAddressAndPort(gs, pod)
			klog.V(4).Infof("GameServer migration occurred, new ip: %v, old ip: %v, result: %v", address, gs.Status.Address, gs.Status.Address)
			eventMsg = "Address updated due to Node migration"
			gs.Status.Conditions = nil
			gs.Status.State = ""
		} else {
			gs.Status.State = carrierv1alpha1.GameServerFailed
			eventMsg = "Node migration occurred"
		}
		c.recorder.Event(gs, corev1.EventTypeWarning, string(gs.Status.State), eventMsg)
	}
	return gs, updated, nil
}

// reconcileGameServerState reconcile pod status, including pod restart policy
func (c *Controller) reconcileGameServerState(gs *carrierv1alpha1.GameServer, pod *corev1.Pod) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != util.GameServerContainerName {
			continue
		}
		klog.V(5).Infof("Pod %v Status %+v", pod.Name, cs.State)
		switch {
		case cs.State.Running != nil:
			if !IsReady(gs) {
				klog.V(4).Infof("GS %v not ready, %+v", gs.Name, gs.Status.Conditions)
				gs.Status.State = carrierv1alpha1.GameServerStarting
				return
			}
			klog.V(4).Infof("GS %v ready, %+v", gs.Name, gs.Status.Conditions)
			gs.Status.State = carrierv1alpha1.GameServerRunning
		case cs.State.Terminated != nil:
			if pod.Spec.RestartPolicy == corev1.RestartPolicyNever {
				gs.Status.State = carrierv1alpha1.GameServerExited
				return
			}
			gs.Status.State = carrierv1alpha1.GameServerStarting
			c.recorder.Event(gs, corev1.EventTypeWarning, string(gs.Status.State), cs.State.Terminated.Message)
		default:
			gs.Status.State = carrierv1alpha1.GameServerStarting
		}
		break
	}
	return
}

func AddNotInServiceConstraint(gs *carrierv1alpha1.GameServer) {
	constraints := gs.Spec.Constraints
	notInService := NotInServiceConstraint()
	found := false
	for idx, constraint := range constraints {
		if constraint.Type != carrierv1alpha1.NotInService {
			continue
		}
		found = true
		constraints[idx] = notInService
		break
	}
	if !found {
		constraints = append(constraints, notInService)
	}
	gs.Spec.Constraints = constraints
}

// getContainerReady gets container status. If ready, add it to GameServer annotations.
func getReadyContainer(gs *carrierv1alpha1.GameServer, pod *corev1.Pod) (bool, error) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != util.GameServerContainerName {
			continue
		}
		container, ok := gs.Annotations[util.GameServerContainerAnnotation]
		if !ok {
			if cs.State.Running == nil {
				return false, errors.New("GameServer container is not currently running, try again")
			}
			if gs.Annotations == nil {
				gs.Annotations = map[string]string{}
			}
			gs.Annotations[util.GameServerContainerAnnotation] = cs.ContainerID
			return true, nil
		}
		if container != cs.ContainerID {
			gs.Annotations[util.GameServerContainerAnnotation] = cs.ContainerID
			return true, nil
		}
		break
	}
	return false, nil
}

// addGameServerHealthCheck adds the http health check to the GameServer container
func addGameServerHealthCheck(gs *carrierv1alpha1.GameServer, pod *corev1.Pod, healthCheckPath string) {
	if gs.Spec.Health.Disabled {
		return
	}
	for i, c := range pod.Spec.Containers {
		if c.Name != util.GameServerContainerName {
			continue
		}
		pod.Spec.Containers[i] = healthCheck(gs, pod.Spec.Containers[i], healthCheckPath)
		return
	}
	return
}

// healthCheck formats liveness probe base on the GameServer Spec
func healthCheck(gs *carrierv1alpha1.GameServer, c corev1.Container, healthCheckPath string) corev1.Container {
	if c.LivenessProbe == nil {
		c.LivenessProbe = &corev1.Probe{
			Handler: corev1.Handler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: healthCheckPath,
					Port: intstr.FromInt(8080),
				},
			},
			InitialDelaySeconds: gs.Spec.Health.InitialDelaySeconds,
			PeriodSeconds:       gs.Spec.Health.PeriodSeconds,
			FailureThreshold:    gs.Spec.Health.FailureThreshold,
		}
	}
	return c
}

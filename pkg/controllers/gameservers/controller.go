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
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"

	"github.com/ocgi/carrier/pkg/apis/carrier"
	carrierv1alpha1 "github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/client/clientset/versioned"
	"github.com/ocgi/carrier/pkg/client/informers/externalversions"
	listerv1 "github.com/ocgi/carrier/pkg/client/listers/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/util"
)

// Controller is a the main GameServer crd controller
type Controller struct {
	podLister          corelisterv1.PodLister
	podSynced          cache.InformerSynced
	gameServerLister   listerv1.GameServerLister
	gameServerSynced   cache.InformerSynced
	nodeLister         corelisterv1.NodeLister
	nodeSynced         cache.InformerSynced
	queue              workqueue.RateLimitingInterface
	nodeTaintWorkQueue workqueue.RateLimitingInterface // handles node autoscaler taint only
	kubeClient         kubernetes.Interface
	carrierClient      versioned.Interface
	recorder           record.EventRecorder
}

// NewController returns a new GameServer crd controller
func NewController(
	kubeClient kubernetes.Interface,
	kubeInformerFactory informers.SharedInformerFactory,
	carrierClient versioned.Interface,
	carrierInformerFactory externalversions.SharedInformerFactory) *Controller {

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
	}

	s := scheme.Scheme
	// Register operator types with the runtime scheme.
	s.AddKnownTypes(carrierv1alpha1.SchemeGroupVersion, &carrierv1alpha1.GameServer{})

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})
	c.recorder = eventBroadcaster.NewRecorder(s, corev1.EventSource{Component: "gameserver-controller"})

	c.queue = workqueue.NewRateLimitingQueue(workqueue.NewItemFastSlowRateLimiter(20*time.Millisecond, 500*time.Millisecond, 5))
	c.nodeTaintWorkQueue = workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	gsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.enqueueGamServer,
		UpdateFunc: c.updateGamServer,
		DeleteFunc: c.deleteGamServer,
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
					c.enqueueGamServer(cache.ExplicitKey(newPod.Namespace + "/" + owner.Name))
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				return
			}
			if isGameServerPod(pod) {
				owner := metav1.GetControllerOf(pod)
				c.enqueueGamServer(cache.ExplicitKey(pod.Namespace + "/" + owner.Name))
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
				c.enqueueNode(node)
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
				c.enqueueNode(newNode)
			},
			DeleteFunc: c.deleteNode,
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

// obj could be a GamServer, or a DeletionFinalStateUnknown marker item.
func (c *Controller) updateGamServer(old, cur interface{}) {
	c.enqueueGamServer(cur)
}

// obj could be a GamServer, or a DeletionFinalStateUnknown marker item.
func (c *Controller) enqueueGamServer(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %+v: %v", obj, err))
		return
	}

	// Requests are always added to queue with resyncPeriod delay.  If there's already
	// request for the Squad in the queue then c new request is always dropped. Requests spend resync
	// interval in queue so Squads are processed every resync interval.
	c.queue.AddRateLimited(key)
}

func (c *Controller) deleteGamServer(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %+v: %v", obj, err))
		return
	}

	c.queue.Forget(key)
}

// obj could be a Node, or a DeletionFinalStateUnknown marker item.
func (c *Controller) updateNode(old, cur interface{}) {
	c.enqueueGamServer(cur)
}

// obj could be a Node, or a DeletionFinalStateUnknown marker item.
func (c *Controller) enqueueNode(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %+v: %v", obj, err))
		return
	}

	// Requests are always added to queue with resyncPeriod delay.  If there's already
	// request for the Squad in the queue then c new request is always dropped. Requests spend resync
	// interval in queue so Squads are processed every resync interval.
	c.nodeTaintWorkQueue.AddRateLimited(key)
}

func (c *Controller) deleteNode(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %+v: %v", obj, err))
		return
	}

	c.nodeTaintWorkQueue.Forget(key)
}

type handle func(string) error

func (c *Controller) gsWorker() {
	for c.processNextWorkItem(c.queue, c.syncGameServer) {
	}
	klog.Infof("GameServer controller worker shutting down")
}

func (c *Controller) nodeWorker() {
	for c.processNextWorkItem(c.nodeTaintWorkQueue, c.syncNodeTaint) {
	}
	klog.Infof("Node controller worker shutting down")
}

func (c *Controller) processNextWorkItem(queue workqueue.RateLimitingInterface, f handle) bool {
	key, quit := queue.Get()
	if quit {
		return false
	}
	defer queue.Done(key)

	err := f(key.(string))
	if err != nil {
		queue.AddRateLimited(key)
		utilruntime.HandleError(err)
		return true
	}
	queue.Forget(key)
	return true
}

// Run the GameServer controller. Will block until stop is closed.
// Runs threadiness number workers to process the rate limited queue
func (c *Controller) Run(workers int, stop <-chan struct{}) error {
	klog.V(4).Info("Wait for cache sync")
	if !cache.WaitForCacheSync(stop, c.gameServerSynced, c.podSynced, c.nodeSynced) {
		return errors.New("failed to wait for caches to sync")
	}

	go wait.Until(c.gsWorker, time.Second, stop)
	go wait.Until(c.nodeWorker, time.Second, stop)
	<-stop
	return nil
}

// syncGameServer reconciles GameServer status base on pod and node status.
func (c *Controller) syncGameServer(key string) error {
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

	gsCopy := gs.DeepCopy()
	if gs, err = c.syncGameServerDeletionTimestamp(gsCopy); err != nil {
		klog.Errorf("Failed sync GameServer: %v deletion time, error: %v", key, err)
		return err
	}
	if gs, err = c.syncGameServerStartingState(gs); err != nil {
		klog.Errorf("Failed sync GameServer: %v starting state, error: %v", key, err)
		return err
	}
	if gs, err = c.syncGameServerRunningState(gs); err != nil {
		klog.Errorf("Failed sync GameServer: %v running state, error: %v", key, err)
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
	nodeName := pod.Spec.NodeName
	if len(nodeName) == 0 {
		return gs, fmt.Errorf("pod of GameServer: %v has not been scheduled", gs.Name)
	}
	_, err = c.nodeLister.Get(nodeName)
	if err != nil {
		return gs, errors.Wrapf(err, "error retrieving node %s for Pod %s", pod.Spec.NodeName, pod.Name)
	}
	klog.Infof("Old GameServer %v state: %v, address: %v, node name: %v",
		gs.Name, gs.Status.State, gs.Status.Address, gs.Status.NodeName)
	gsStatusCopy := gs.Status.DeepCopy()
	// reconcile GameServer Address
	updated := false
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
		c.recorder.Event(gs, corev1.EventTypeNormal, string(gs.Status.State), "Waiting for receiving readiness message")
	}
	return gs, nil
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
	pod, err := buildPod(gs)
	if err != nil {
		// this shouldn't happen, but if it does.
		c.recorder.Eventf(gs, corev1.EventTypeWarning, string(gs.Status.State), "build Pod for GameServer %s", gs.Name)
		return gs, errors.Wrapf(err, "error building Pod for GameServer %s", gs.Name)
	}

	klog.V(4).Infof("Creating pod: %v for GameServer", pod.Name)
	pod, err = c.kubeClient.CoreV1().Pods(gs.Namespace).Create(pod)
	if err != nil {
		switch {
		case k8serrors.IsAlreadyExists(err):
			c.recorder.Event(gs, corev1.EventTypeNormal, string(gs.Status.State), "Pod already exists")
			return gs, nil
		default:
			klog.Errorf("Pod err: %v", err)
			c.recorder.Eventf(gs, corev1.EventTypeWarning, string(gs.Status.State), "error creating Pod for GameServer %s: %v", gs.Name, err)
			return gs, errors.Wrapf(err, "error creating Pod for GameServer %s: %v", gs.Name, err)
		}
	}
	c.recorder.Event(gs, corev1.EventTypeNormal, string(gs.Status.State),
		fmt.Sprintf("Creating pod %s", pod.Name))

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
		gs.Status.State = carrierv1alpha1.GameServerFailed
		c.recorder.Event(gs, corev1.EventTypeWarning, string(gs.Status.State), "Node migration occurred")
	}
	return gs, updated, nil
}

// reconcileGameServerState reconcile pod status, including pod restart policy
func (c *Controller) reconcileGameServerState(gs *carrierv1alpha1.GameServer, pod *corev1.Pod) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != util.GameServerContainerName {
			continue
		}

		switch pod.Status.Phase {
		case corev1.PodRunning:
			if cs.State.Terminated == nil {
				if IsOutOfService(gs) && IsDeletable(gs) {
					gs.Status.State = carrierv1alpha1.GameServerExited
					return
				}
				gs.Status.State = carrierv1alpha1.GameServerRunning
				return
			}
			c.recorder.Event(gs, corev1.EventTypeWarning, string(gs.Status.State), cs.State.Terminated.Message)
			if pod.Spec.RestartPolicy == corev1.RestartPolicyNever {
				gs.Status.State = carrierv1alpha1.GameServerExited
				return
			}
			gs.Status.State = carrierv1alpha1.GameServerRunning
		case corev1.PodPending:
			gs.Status.State = carrierv1alpha1.GameServerStarting
		case corev1.PodFailed:
			gs.Status.State = carrierv1alpha1.GameServerFailed
		case corev1.PodSucceeded:
			gs.Status.State = carrierv1alpha1.GameServerExited
		default:
			gs.Status.State = carrierv1alpha1.GameServerUnknown
		}
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

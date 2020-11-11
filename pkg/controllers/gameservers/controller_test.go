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

package gameservers

import (
	"context"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	v12 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes/fake"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog"

	"github.com/ocgi/carrier/pkg/apis/carrier"
	"github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
	gsfake "github.com/ocgi/carrier/pkg/client/clientset/versioned/fake"
	"github.com/ocgi/carrier/pkg/client/informers/externalversions"
	v1alpha12 "github.com/ocgi/carrier/pkg/client/informers/externalversions/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/util"
)

func TestNewControllerNodeTaint(t *testing.T) {
	ctx := context.Background()
	_, _, _, c, client := fakeController(ctx)
	node := nodeWithTaint()
	node, err := client.CoreV1().Nodes().Update(node)
	if err != nil {
		t.Fatal(err)
	}

	err = c.syncNodeTaint("test")
	if err != nil {
		t.Fatal(err)
	}
	err = wait.Poll(100*time.Millisecond, 1*time.Second, func() (done bool, err error) {
		gs, err := c.carrierClient.CarrierV1alpha1().GameServers("default").Get("test", v1.GetOptions{})
		if err != nil {
			t.Error(err)
			return false, nil
		}
		if len(gs.Spec.Constraints) == 0 {
			return false, nil
		}
		t.Logf("gs %v constraints: %v", gs.Name, gs.Spec.Constraints)
		return true, nil
	})
	if err != nil {
		t.Errorf("Add constraint to gs failed: %v", err)
	}
}

func TestNewControllerSyncDeleteTimeStamp(t *testing.T) {
	ctx := context.Background()
	_, _, _, c, _ := fakeController(ctx)
	for _, testCase := range []struct {
		name     string
		gs       *v1alpha1.GameServer
		gsChange bool
	}{
		{
			name:     "not deleting",
			gs:       gs(),
			gsChange: false,
		},
		{
			name:     "deleting",
			gs:       gsToDelete(),
			gsChange: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			gs, err := c.syncGameServerDeletionTimestamp(testCase.gs.DeepCopy())
			if err != nil {
				t.Error(err)
			}
			if gsChanged(gs, testCase.gs) != testCase.gsChange {
				t.Errorf("desire: %v, get: %v", testCase.gsChange, !reflect.DeepEqual(gs, testCase.gs))
			}
		})
	}
}

func TestNewControllerSyncStarting(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		gs       *v1alpha1.GameServer
		podExist bool
		gsChange bool
	}{
		{
			name:     "pod exist",
			gs:       gsWithTemp(),
			podExist: true,
			gsChange: true,
		},
		{
			name:     "pod not exit",
			gs:       gsWithTemp(),
			gsChange: true,
		},
		{
			name:     "gs already running",
			gs:       gsWithTempRunning(),
			podExist: true,
			gsChange: false,
		},
		{
			name:     "gs delete",
			gs:       gsToDelete(),
			podExist: true,
			gsChange: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			podInformer, _, _, c, _ := fakeController(ctx)
			if testCase.podExist {
				if err := podInformer.Informer().GetStore().Add(pod()); err != nil {
					t.Error(err)
				}
			}

			gs, err := c.syncGameServerStartingState(testCase.gs.DeepCopy())
			if err != nil {
				t.Error(err)
			}
			if gsChanged(gs, testCase.gs) != testCase.gsChange {
				t.Errorf("desire: %v, get: %v", testCase.gsChange, !reflect.DeepEqual(gs, testCase.gs))
			}
		})
	}
}

func TestNewControllerSyncRunning(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		gs        *v1alpha1.GameServer
		nodeExist bool
		pod       *corev1.Pod
		errorRet  bool
		state     v1alpha1.GameServerState
	}{
		{
			name:      "pod running, node exist, container ready",
			gs:        gsWithTempStarting(),
			nodeExist: true,
			pod:       podRunning(),
			state:     v1alpha1.GameServerRunning,
		},
		{
			name:      "pod running, node exist, container not ready, not running",
			gs:        gsWithTempStarting(),
			pod:       podNotRunning(),
			nodeExist: true,
			errorRet:  true,
		},
		{
			name:     "pod not exist",
			gs:       gsWithTemp(),
			errorRet: true,
		},
		{
			name:     "pod exist, node not exist",
			gs:       gsWithTemp(),
			pod:      podRunning(),
			errorRet: true,
		},
		{
			name:      "gs already running, container not ready",
			gs:        gsWithTempRunningWithAnn(),
			pod:       podNotRunning(),
			nodeExist: true,
			state:     v1alpha1.GameServerStarting,
		},
		{
			name:      "gs delete",
			gs:        gsToDelete(),
			nodeExist: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			podInformer, nodeInformer, _, c, _ := fakeController(ctx)
			if testCase.nodeExist {
				if err := nodeInformer.Informer().GetStore().Add(node()); err != nil {
					t.Error(err)
				}
			}
			if testCase.pod != nil {
				if err := podInformer.Informer().GetStore().Add(testCase.pod); err != nil {
					t.Error(err)
				}
			}

			gs, err := c.syncGameServerRunningState(testCase.gs)
			if err != nil {
				if testCase.errorRet {
					t.Log(err)
					return
				}
				t.Error(err)
			}
			if gs.Status.State != testCase.state {
				t.Errorf("gs is %v, desire: %v", gs.Status.State, testCase.state)
			}
		})
	}
}

func fakeController(ctx context.Context) (v12.PodInformer, v12.NodeInformer, v1alpha12.GameServerInformer, *Controller, *fake.Clientset) {

	fakeClient := fake.NewSimpleClientset(node(), pod())
	fakeGSClient := gsfake.NewSimpleClientset(&v1alpha1.GameServer{ObjectMeta: v1.ObjectMeta{Name: "test", Namespace: "default", UID: "123"}})
	factory := informers.NewSharedInformerFactory(fakeClient, 0)
	podInformer := factory.Core().V1().Pods()
	nodeInformer := factory.Core().V1().Nodes()
	carrierFactory := externalversions.NewSharedInformerFactory(fakeGSClient, 0)
	gsInformer := carrierFactory.Carrier().V1alpha1().GameServers()

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: fakeClient.CoreV1().Events("default")})
	s := runtime.NewScheme()
	s.AddKnownTypes(v1alpha1.SchemeGroupVersion, &v1alpha1.GameServer{}, &v1alpha1.GameServerList{})

	c := &Controller{
		podLister:        podInformer.Lister(),
		podSynced:        podInformer.Informer().HasSynced,
		nodeLister:       nodeInformer.Lister(),
		nodeSynced:       nodeInformer.Informer().HasSynced,
		gameServerLister: gsInformer.Lister(),
		gameServerSynced: gsInformer.Informer().HasSynced,
		carrierClient:    fakeGSClient,
		kubeClient:       fakeClient,
		recorder:         eventBroadcaster.NewRecorder(s, corev1.EventSource{Component: "gameserver-controller"}),
	}
	factory.Start(ctx.Done())
	carrierFactory.Start(ctx.Done())
	cache.WaitForCacheSync(ctx.Done(), c.podSynced, c.gameServerSynced, c.nodeSynced)
	return podInformer, nodeInformer, gsInformer, c, fakeClient
}

func pod() *corev1.Pod {
	controller := true
	return &corev1.Pod{
		ObjectMeta: v1.ObjectMeta{Name: "test", Namespace: "default", OwnerReferences: []v1.OwnerReference{
			{
				Name:       "test",
				UID:        "123",
				Controller: &controller,
			}},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "server"}}, NodeName: "test"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}
}

func podRunning() *corev1.Pod {
	pod := pod()
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name:                 "server",
			State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: v1.Now()}},
			LastTerminationState: corev1.ContainerState{},
			Ready:                true,
			ContainerID:          "XXX",
		},
	}
	return pod
}

func podNotRunning() *corev1.Pod {
	pod := pod()
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name:                 "server",
			State:                corev1.ContainerState{},
			LastTerminationState: corev1.ContainerState{},
			Ready:                false,
			ContainerID:          "XXXXX",
		},
	}
	return pod
}

func node() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: v1.ObjectMeta{Name: "test"},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{
					Type:    corev1.NodeInternalIP,
					Address: "127.0.0.1",
				},
			},
		},
	}
}

func nodeWithTaint() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: v1.ObjectMeta{Name: "test"},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{
					Key: ToBeDeletedTaint,
				},
			},
		},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{
					Type:    corev1.NodeInternalIP,
					Address: "127.0.0.1",
				},
			},
		},
	}
}

func gs() *v1alpha1.GameServer {
	return &v1alpha1.GameServer{ObjectMeta: v1.ObjectMeta{Name: "test", Namespace: "default", UID: "123"}}
}

func gsToDelete() *v1alpha1.GameServer {
	timeNow := v1.Now()
	return &v1alpha1.GameServer{ObjectMeta: v1.ObjectMeta{Name: "test", Namespace: "default", DeletionTimestamp: &timeNow,
		Finalizers: []string{carrier.GroupName}}}
}

func gsWithTemp() *v1alpha1.GameServer {
	gs := gs()
	gs.Spec = v1alpha1.GameServerSpec{
		Template: corev1.PodTemplateSpec{Spec: pod().Spec},
	}
	return gs
}

func gsWithTempRunning() *v1alpha1.GameServer {
	gs := gsWithTemp()
	gs.Status.State = v1alpha1.GameServerRunning
	return gs
}
func gsWithTempStarting() *v1alpha1.GameServer {
	gs := gsWithTemp()
	gs.Status.State = v1alpha1.GameServerStarting
	return gs
}

func gsWithTempRunningWithAnn() *v1alpha1.GameServer {
	gs := gsWithTemp()
	gs.Status.State = v1alpha1.GameServerRunning
	gs.Annotations = map[string]string{util.GameServerContainerAnnotation: "XXX"}
	return gs
}

func gsChanged(gs, gsRet *v1alpha1.GameServer) bool {
	return !reflect.DeepEqual(gs, gsRet)
}

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
	"testing"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog"

	"github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
	gsfake "github.com/ocgi/carrier/pkg/client/clientset/versioned/fake"
	"github.com/ocgi/carrier/pkg/client/informers/externalversions"
	v1alpha12 "github.com/ocgi/carrier/pkg/client/informers/externalversions/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/util"
)

var selectMap = map[string]string{util.GameServerSetLabelKey: "test"}

func TestControllerSyncGameServerSet(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		gameServers    []*v1alpha1.GameServer
		specReplicas   int32
		desireReplicas int32
	}{
		{
			name:           "from 1 to 2",
			gameServers:    gsOwnered(),
			specReplicas:   2,
			desireReplicas: 2,
		},
		{
			name:           "from 2 to 1, running",
			gameServers:    gsOwnered2Running(),
			specReplicas:   1,
			desireReplicas: 2,
		},
		{
			name:           "from 2 to 1, state: empty",
			gameServers:    gsOwnered2(),
			specReplicas:   1,
			desireReplicas: 1,
		},
		{
			name:           "from 2 to 1, condition: unhealthy",
			gameServers:    gsOwnered2Unhealthy(),
			specReplicas:   1,
			desireReplicas: 1,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()

			_, gsClient, gsInformer, gssInformer, c := fakeController(ctx)
			gss := gss()
			if gss.Spec.Replicas != testCase.specReplicas {
				gss.Spec.Replicas = testCase.specReplicas
			}
			gssInformer.Informer().GetStore().Add(gss)
			for _, gs := range testCase.gameServers {
				gsClient.CarrierV1alpha1().GameServers(gs.Namespace).Create(gs)
				gsInformer.Informer().GetStore().Add(gs)
			}
			gssName := fmt.Sprintf("%v/%v", gss.Namespace, gss.Name)
			err := c.syncGameServerSet(gssName)
			if err != nil {
				t.Error(err)
			}
			gameServers, err := c.carrierClient.CarrierV1alpha1().GameServers(gss.Namespace).List(v1.ListOptions{LabelSelector: labels.FormatLabels(selectMap)})
			if err != nil {
				t.Error(err)
			}
			var filtered []*v1alpha1.GameServer
			for _, gs := range gameServers.Items {
				if gs.DeletionTimestamp != nil || gs.Status.State == v1alpha1.GameServerExited {
					continue
				}
				filtered = append(filtered, &gs)
			}
			if len(filtered) != int(testCase.desireReplicas) {
				t.Errorf("Current GameServers: %v, desired: %v", len(filtered), testCase.desireReplicas)
			}
		})
	}
}

func fakeController(ctx context.Context) (*fake.Clientset, *gsfake.Clientset, v1alpha12.GameServerInformer, v1alpha12.GameServerSetInformer, *Controller) {

	fakeClient := fake.NewSimpleClientset()
	fakeGSClient := gsfake.NewSimpleClientset(gss())
	carrierFactory := externalversions.NewSharedInformerFactory(fakeGSClient, 0)
	gsInformer := carrierFactory.Carrier().V1alpha1().GameServers()
	gssInformer := carrierFactory.Carrier().V1alpha1().GameServerSets()

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: fakeClient.CoreV1().Events("default")})
	s := runtime.NewScheme()
	s.AddKnownTypes(v1alpha1.SchemeGroupVersion, &v1alpha1.GameServerSet{}, &v1alpha1.GameServerSetList{})

	c := &Controller{
		carrierClient:       fakeGSClient,
		gameServerSetLister: gssInformer.Lister(),
		gameServerSetSynced: gssInformer.Informer().HasSynced,
		gameServerLister:    gsInformer.Lister(),
		gameServerSynced:    gsInformer.Informer().HasSynced,
		recorder:            eventBroadcaster.NewRecorder(s, corev1.EventSource{Component: "gameserverset-controller"}),
		counter:             &Counter{nodeGameServer: map[string]uint64{}},
	}
	carrierFactory.Start(ctx.Done())
	cache.WaitForCacheSync(ctx.Done(), c.gameServerSetSynced, c.gameServerSynced)
	return fakeClient, fakeGSClient, gsInformer, gssInformer, c
}

func gss() *v1alpha1.GameServerSet {
	return &v1alpha1.GameServerSet{
		ObjectMeta: v1.ObjectMeta{Name: "test", Namespace: "default", UID: "123"},
		Spec: v1alpha1.GameServerSetSpec{Replicas: 1, Template: v1alpha1.GameServerTemplateSpec{
			Spec: v1alpha1.GameServerSpec{},
		}},
		Status: v1alpha1.GameServerSetStatus{
			Replicas:      1,
			ReadyReplicas: 1,
		}}
}

func gsOwnered() []*v1alpha1.GameServer {
	controlled := true
	return []*v1alpha1.GameServer{
		{
			ObjectMeta: v1.ObjectMeta{Name: "test-xxx", Namespace: "default", OwnerReferences: []v1.OwnerReference{
				{
					Controller: &controlled,
					Name:       "test",
					UID:        "123",
				},
			}, Labels: map[string]string{util.GameServerSetLabelKey: "test"}},
			Status: v1alpha1.GameServerStatus{
				State: v1alpha1.GameServerRunning,
			},
		},
	}
}

func gsOwnered2() []*v1alpha1.GameServer {
	controlled := true
	return []*v1alpha1.GameServer{
		{
			ObjectMeta: v1.ObjectMeta{Name: "test-xxx", Namespace: "default", OwnerReferences: []v1.OwnerReference{
				{
					Controller: &controlled,
					Name:       "test",
					UID:        "123",
				},
			}, Labels: map[string]string{util.GameServerSetLabelKey: "test"}},
			Spec:   v1alpha1.GameServerSpec{DeletableGates: []string{"carrier.ocgi.dev/has-no-play"}},
			Status: v1alpha1.GameServerStatus{},
		},
		{
			ObjectMeta: v1.ObjectMeta{Name: "test-yyy", Namespace: "default", OwnerReferences: []v1.OwnerReference{
				{
					Controller: &controlled,
					Name:       "test",
					UID:        "123",
				},
			}, Labels: map[string]string{util.GameServerSetLabelKey: "test"}},
			Spec:   v1alpha1.GameServerSpec{DeletableGates: []string{"carrier.ocgi.dev/has-no-play"}},
			Status: v1alpha1.GameServerStatus{},
		},
	}
}

func gsOwnered2Running() []*v1alpha1.GameServer {
	gamesvrs := gsOwnered2()
	gamesvrs[0].Status.State = v1alpha1.GameServerRunning
	gamesvrs[1].Status.State = v1alpha1.GameServerRunning
	gamesvrs[0].Spec.DeletableGates = []string{"test"}
	gamesvrs[1].Spec.DeletableGates = []string{"test"}
	return gamesvrs
}

func gsOwnered2Unhealthy() []*v1alpha1.GameServer {
	gamesvrs := gsOwnered2()
	gamesvrs[0].Status.Conditions = []v1alpha1.GameServerCondition{{
		Type:   "carrier.ocgi.dev/has-no-play",
		Status: v1alpha1.ConditionTrue,
	}}
	gamesvrs[0].Status.State = v1alpha1.GameServerRunning
	gamesvrs[1].Status.State = v1alpha1.GameServerRunning
	return gamesvrs
}

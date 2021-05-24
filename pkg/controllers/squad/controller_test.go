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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/uuid"
	core "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	"github.com/ocgi/carrier/pkg/apis/carrier"
	carrierv1alpha1 "github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
	carrierfake "github.com/ocgi/carrier/pkg/client/clientset/versioned/fake"
	"github.com/ocgi/carrier/pkg/client/informers/externalversions"
	"github.com/ocgi/carrier/pkg/util"
)

var (
	alwaysReady        = func() bool { return true }
	noResyncPeriodFunc = func() time.Duration { return 0 }
)

type fixture struct {
	t *testing.T

	client *carrierfake.Clientset
	// Objects to put in the store.
	squadLister []*carrierv1alpha1.Squad
	gsSetLister []*carrierv1alpha1.GameServerSet
	gsLister    []*carrierv1alpha1.GameServer
	// Actions expected to happen on the client.
	actions []core.Action
	// Objects from here preloaded into NewSimpleFake.
	objects []runtime.Object
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.objects = []runtime.Object{}
	return f
}

func (f *fixture) expectCreateGameServerSetAction(gsSet *carrierv1alpha1.GameServerSet) {
	f.actions = append(f.actions, core.NewCreateAction(schema.GroupVersionResource{Resource: "gameserversets"}, gsSet.Namespace, gsSet))
}

func (f *fixture) expectUpdateSquadAction(squad *carrierv1alpha1.Squad) {
	f.actions = append(f.actions, core.NewUpdateAction(schema.GroupVersionResource{Resource: "squads"}, squad.Namespace, squad))
}

func (f *fixture) expectUpdateSquadStatusAction(squad *carrierv1alpha1.Squad) {
	action := core.NewUpdateAction(schema.GroupVersionResource{Resource: "squads"}, squad.Namespace, squad)
	action.Subresource = "status"
	f.actions = append(f.actions, action)
}

func (f *fixture) run(squadName string) {
	f.runController(squadName, true, false)
}

func (f *fixture) runExpectError(squadName string) {
	f.runController(squadName, true, true)
}

func (f *fixture) runController(squadName string, startInformers bool, expectError bool) {
	c, informer := f.newController()
	if startInformers {
		stopCh := make(chan struct{})
		defer close(stopCh)
		informer.Start(stopCh)
	}

	err := c.syncSquad(squadName)
	if !expectError && err != nil {
		f.t.Errorf("error syncing squad: %v", err)
	} else if expectError && err == nil {
		f.t.Error("expected error syncing squad, got nil")
	}

	actions := filterInformerActions(f.client.Actions())
	for i, action := range actions {
		if len(f.actions) < i+1 {
			f.t.Errorf("%d unexpected actions: %+v", len(actions)-len(f.actions), actions[i:])
			break
		}

		expectedAction := f.actions[i]
		if !(expectedAction.Matches(action.GetVerb(), action.GetResource().Resource) && action.GetSubresource() == expectedAction.GetSubresource()) {
			f.t.Errorf("Expected\n\t%#v\ngot\n\t%#v", expectedAction, action)
			continue
		}
	}

	if len(f.actions) > len(actions) {
		f.t.Errorf("%d additional expected actions:%+v", len(f.actions)-len(actions), f.actions[len(actions):])
	}
}

// filterInformerActions filters list and watch actions for testing resources.
// Since list and watch don't change resource state we can filter it to lower
// nose level in our tests.
func filterInformerActions(actions []core.Action) []core.Action {
	ret := []core.Action{}
	for _, action := range actions {
		if len(action.GetNamespace()) == 0 &&
			(action.Matches("list", "squads") ||
				action.Matches("list", "gameserversets") ||
				action.Matches("list", "gameservers") ||
				action.Matches("watch", "squads") ||
				action.Matches("watch", "gameserversets") ||
				action.Matches("watch", "gameservers")) {
			continue
		}
		ret = append(ret, action)
	}
	return ret
}

func (f *fixture) newController() (*Controller, externalversions.SharedInformerFactory) {
	f.client = carrierfake.NewSimpleClientset(f.objects...)
	carrierFactory := externalversions.NewSharedInformerFactory(f.client, noResyncPeriodFunc())

	gameServers := carrierFactory.Carrier().V1alpha1().GameServers()
	gameServerSets := carrierFactory.Carrier().V1alpha1().GameServerSets()
	gsSetInformer := gameServerSets.Informer()
	squads := carrierFactory.Carrier().V1alpha1().Squads()
	squadsInformer := squads.Informer()

	s := runtime.NewScheme()
	s.AddKnownTypes(carrierv1alpha1.SchemeGroupVersion, &carrierv1alpha1.Squad{}, &carrierv1alpha1.SquadList{})

	c := &Controller{
		gameServerLister:    gameServers.Lister(),
		gameServerSetGetter: f.client.CarrierV1alpha1(),
		gameServerSetLister: gameServerSets.Lister(),
		gameServerSetSynced: alwaysReady,
		squadGetter:         f.client.CarrierV1alpha1(),
		squadLister:         squads.Lister(),
		squadSynced:         alwaysReady,
		recorder:            &record.FakeRecorder{},
	}
	for _, squad := range f.squadLister {
		squadsInformer.GetIndexer().Add(squad)
	}
	for _, gsSet := range f.gsSetLister {
		gsSetInformer.GetIndexer().Add(gsSet)
	}
	for _, gs := range f.gsLister {
		gameServers.Informer().GetIndexer().Add(gs)
	}

	return c, carrierFactory
}

func newGameServerSet(squad *carrierv1alpha1.Squad, name string, replicas int) *carrierv1alpha1.GameServerSet {
	gsSet := &carrierv1alpha1.GameServerSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			UID:       uuid.NewUUID(),
			Namespace: metav1.NamespaceDefault,
			Labels:    squad.Spec.Selector.MatchLabels,
			Annotations: map[string]string{
				util.RevisionAnnotation:        "1",
				util.DesiredReplicasAnnotation: fmt.Sprintf("%d", squad.Spec.Replicas),
				util.MaxReplicasAnnotation:     fmt.Sprintf("%d", squad.Spec.Replicas+MaxSurge(*squad)),
			},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(squad, controllerKind)},
		},
		Spec: carrierv1alpha1.GameServerSetSpec{
			Selector: squad.Spec.Selector,
			Replicas: int32(replicas),
			Template: squad.Spec.Template,
		},
	}
	return gsSet
}

func newSquad(name string, replicas int, revisionHistoryLimit *int32, maxSurge, maxUnavailable *intstr.IntOrString, selector map[string]string) *carrierv1alpha1.Squad {
	selector[util.SquadNameLabel] = name
	squad := carrierv1alpha1.Squad{
		TypeMeta: metav1.TypeMeta{APIVersion: carrier.GroupName, Kind: "Squad"},
		ObjectMeta: metav1.ObjectMeta{
			UID:         uuid.NewUUID(),
			Name:        name,
			Namespace:   metav1.NamespaceDefault,
			Annotations: make(map[string]string),
		},
		Spec: carrierv1alpha1.SquadSpec{
			Strategy: carrierv1alpha1.SquadStrategy{
				Type: carrierv1alpha1.RollingUpdateSquadStrategyType,
				RollingUpdate: &carrierv1alpha1.RollingUpdateSquad{
					MaxUnavailable: func() *intstr.IntOrString { i := intstr.FromInt(0); return &i }(),
					MaxSurge:       func() *intstr.IntOrString { i := intstr.FromInt(0); return &i }(),
				},
			},
			Replicas: int32(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			Template: carrierv1alpha1.GameServerTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: selector,
				},
				Spec: carrierv1alpha1.GameServerSpec{
					Ports: []carrierv1alpha1.GameServerPort{
						{
							Name:          "default",
							ContainerPort: func() *int32 { i := int32(7654); return &i }(),
						},
					},
					SdkServer: carrierv1alpha1.SdkServer{
						LogLevel: carrierv1alpha1.SdkServerLogLevelInfo,
						GRPCPort: int32(9020),
						HTTPPort: int32(9021),
					},
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Image: "foo/bar",
								},
							},
						},
					},
				},
			},
			RevisionHistoryLimit: revisionHistoryLimit,
		},
		Status: carrierv1alpha1.SquadStatus{
			Selector: metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: selector}),
		},
	}
	if maxSurge != nil {
		squad.Spec.Strategy.RollingUpdate.MaxSurge = maxSurge
	}
	if maxUnavailable != nil {
		squad.Spec.Strategy.RollingUpdate.MaxUnavailable = maxUnavailable
	}
	return &squad
}

// GetKey is a helper function used by controllers unit tests to get the
// key for a given kubernetes resource.
func getKey(squad *carrierv1alpha1.Squad, t *testing.T) string {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(squad)
	if err != nil {
		t.Errorf("Unexpected error getting key for foo %v: %v", squad.Name, err)
		return ""
	}
	return key
}

func TestSyncSquadCreatesGameServerSet(t *testing.T) {
	f := newFixture(t)

	squad := newSquad("squad", 1, nil, nil, nil, map[string]string{"foo": "bar"})
	f.squadLister = append(f.squadLister, squad)
	f.objects = append(f.objects, squad)

	gsSet := newGameServerSet(squad, "gsSet", 1)

	f.expectCreateGameServerSetAction(gsSet)
	f.expectUpdateSquadStatusAction(squad)

	f.run(getKey(squad, t))
}

func TestGetGameServerMapForSquad(t *testing.T) {
	f := newFixture(t)

	squad := newSquad("squad", 1, nil, nil, nil, map[string]string{"foo": "bar"})
	gsSet1 := newGameServerSet(squad, "gsSet1", 1)
	gsSet2 := newGameServerSet(squad, "gsSet2", 1)

	// Add a GameServer for each GameServerSet.
	gs1 := generateGameServerFromGSSet(gsSet1)
	gs2 := generateGameServerFromGSSet(gsSet2)
	// Add a GameServer that has matching labels, but no ControllerRef.
	gs3 := generateGameServerFromGSSet(gsSet1)
	gs3.Name = "gs3"
	gs3.OwnerReferences = nil
	// Add a GameServer that has matching labels and ControllerRef, but is inactive.
	gs4 := generateGameServerFromGSSet(gsSet1)
	gs4.Name = "gs4"
	gs4.Status.State = carrierv1alpha1.GameServerFailed

	f.squadLister = append(f.squadLister, squad)
	f.gsSetLister = append(f.gsSetLister, gsSet1, gsSet2)
	f.gsLister = append(f.gsLister, gs1, gs2, gs3, gs4)
	f.objects = append(f.objects, squad, gsSet1, gsSet2, gs1, gs2, gs3, gs4)

	c, informers := f.newController()
	stopCh := make(chan struct{})
	defer close(stopCh)
	informers.Start(stopCh)

	gsMap, err := c.getGameServerMapForSquad(squad, f.gsSetLister)
	if err != nil {
		t.Fatalf("getGameServerMapForSquad() error: %v", err)
	}
	gsCount := 0
	for _, gsList := range gsMap {
		gsCount += len(gsList)
	}
	if got, want := gsCount, 3; got != want {
		t.Errorf("gsCount = %v, want %v", got, want)
	}
	// two GameServerSet
	if got, want := len(gsMap), 2; got != want {
		t.Errorf("len(gsMap) = %v, want %v", got, want)
	}
	if got, want := len(gsMap[gsSet1.UID]), 2; got != want {
		t.Errorf("len(gsMap[gsSet1]) = %v, want %v", got, want)
	}
	expect := map[string]struct{}{"gsSet1-gameserver": {}, "gs4": {}}
	for _, gs := range gsMap[gsSet1.UID] {
		if _, ok := expect[gs.Name]; !ok {
			t.Errorf("unexpected gameserver name for rs1: %s", gs.Name)
		}
	}
	if got, want := len(gsMap[gsSet2.UID]), 1; got != want {
		t.Errorf("len(gsMap[gsSet2]) = %v, want %v", got, want)
	}
	if got, want := gsMap[gsSet2.UID][0].Name, "gsSet2-gameserver"; got != want {
		t.Errorf("gsMap[gsSet2] = [%v], want [%v]", got, want)
	}
}

// generateGameServerFromGSSet creates a GameServer, with the input GameServer Set's selector and its template
func generateGameServerFromGSSet(gsSet *carrierv1alpha1.GameServerSet) *carrierv1alpha1.GameServer {
	trueVar := true
	return &carrierv1alpha1.GameServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gsSet.Name + "-gameserver",
			Namespace: gsSet.Namespace,
			Labels:    gsSet.Spec.Selector.MatchLabels,
			OwnerReferences: []metav1.OwnerReference{
				{UID: gsSet.UID, APIVersion: "v1beta1", Kind: "GameServerSet", Name: gsSet.Name, Controller: &trueVar},
			},
		},
		Spec: gsSet.Spec.Template.Spec,
	}
}

package gameserversets

import (
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	carrierv1alpha1 "github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/util"
)

func TestByCount(t *testing.T) {
	list := []*carrierv1alpha1.GameServer{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
			Status: carrierv1alpha1.GameServerStatus{
				NodeName: "node1",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test1",
			},
			Status: carrierv1alpha1.GameServerStatus{
				NodeName: "node2",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test2",
			},
			Status: carrierv1alpha1.GameServerStatus{
				NodeName: "node1",
			},
		},
	}
	counter := Counter{
		nodeGameServer: map[string]uint64{"node1": 2, "node2": 1},
	}
	desiredNames := []string{"test1", "test", "test2"}
	var actual []string
	list = sortGameServersByPodNum(list, &counter)
	for _, server := range list {
		actual = append(actual, server.Name)
	}
	if !reflect.DeepEqual(desiredNames, actual) {
		t.Errorf("desired: %v, actual: %v", desiredNames, actual)
	}
}

func TestByCreationTime(t *testing.T) {
	now := time.Now()
	list := []*carrierv1alpha1.GameServer{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "test",
				CreationTimestamp: metav1.NewTime(now.Add(1 * time.Second)),
			},
			Status: carrierv1alpha1.GameServerStatus{
				NodeName: "node1",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "test1",
				CreationTimestamp: metav1.NewTime(now.Add(2 * time.Second)),
			},
			Status: carrierv1alpha1.GameServerStatus{
				NodeName: "node2",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "test2",
				CreationTimestamp: metav1.NewTime(now.Add(1 * time.Second)),
			},
			Status: carrierv1alpha1.GameServerStatus{
				NodeName: "node1",
			},
		},
	}
	desiredNames := []string{"test", "test2", "test1"}
	var actual []string
	list = sortGameServersByCreationTime(list)
	for _, server := range list {
		actual = append(actual, server.Name)
	}
	if !reflect.DeepEqual(desiredNames, actual) {
		t.Errorf("desired: %v, actual: %v", desiredNames, actual)
	}
}

func TestByCost(t *testing.T) {
	list := []*carrierv1alpha1.GameServer{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "test",
				Annotations: map[string]string{util.GameServerDeletionCost: "1"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "test1",
				Annotations: map[string]string{util.GameServerDeletionCost: "2"},
			},
		},
	}
	desiredNames := []string{"test", "test1"}
	var actual []string
	list = sortGameServersByCost(list)
	for _, server := range list {
		actual = append(actual, server.Name)
	}
	if !reflect.DeepEqual(desiredNames, actual) {
		t.Errorf("desired: %v, actual: %v", desiredNames, actual)
	}
}

func TestByHash(t *testing.T) {
	list := []*carrierv1alpha1.GameServer{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "test",
				Labels: map[string]string{util.GameServerHash: "1"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "test1",
				Labels: map[string]string{util.GameServerHash: "2"},
			},
		},
	}
	gss := &carrierv1alpha1.GameServerSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "testa",
			Labels: map[string]string{util.GameServerHash: "1"},
		},
	}
	desiredNames := []string{"test1", "test"}
	var actual []string
	list = sortGameServersByHash(list, gss)
	for _, server := range list {
		actual = append(actual, server.Name)
	}
	if !reflect.DeepEqual(desiredNames, actual) {
		t.Errorf("desired: %v, actual: %v", desiredNames, actual)
	}
}

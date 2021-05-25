package gameserversets

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	carrierv1alpha1 "github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/util"
)

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

package gameserversets

import (
	"github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func TestGameServer(t *testing.T) {
	gss := v1alpha1.GameServerSet{
		ObjectMeta: v1.ObjectMeta{
			Annotations: map[string]string{"a": "v"},
		},
	}
	gs := GameServer(&gss)

	t.Logf("Annotations: %v", gs.Annotations)
}

package gameservers

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"

	carrierv1alpha1 "github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
)

type SDKServerConfig struct {
	image      string
	alwaysPull bool
	cpu        resource.Quantity
	memory     resource.Quantity
}

func NewSDKServerConfig(image string,
	alwaysPull bool,
	cpu resource.Quantity,
	memory resource.Quantity) *SDKServerConfig {
	return &SDKServerConfig{
		image:      image,
		alwaysPull: alwaysPull,
		cpu:        cpu,
		memory:     memory,
	}
}

// BuildSidecar creates the sidecar container for a given GameServer
func (sdk *SDKServerConfig) BuildSidecar(gs *carrierv1alpha1.GameServer) corev1.Container {
	sidecar := corev1.Container{
		Name:  sdkserverSidecarName,
		Image: sdk.image,
		Env: []corev1.EnvVar{
			{
				Name:  "GAMESERVER_NAME",
				Value: gs.Name,
			},
			{
				Name: "POD_NAMESPACE",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.namespace",
					},
				},
			},
		},
		Resources: corev1.ResourceRequirements{},
		LivenessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.FromInt(8080),
				},
			},
			InitialDelaySeconds: 3,
			PeriodSeconds:       3,
		},
	}

	if gs.Spec.SdkServer.GRPCPort != 0 {
		sidecar.Args = append(sidecar.Args, fmt.Sprintf("--grpc-port=%d", gs.Spec.SdkServer.GRPCPort))
	}

	if gs.Spec.SdkServer.HTTPPort != 0 {
		sidecar.Args = append(sidecar.Args, fmt.Sprintf("--http-port=%d", gs.Spec.SdkServer.HTTPPort))
	}

	requests := corev1.ResourceList{}
	limits := corev1.ResourceList{}
	if !sdk.cpu.IsZero() {
		requests[corev1.ResourceCPU] = sdk.cpu
		limits[corev1.ResourceCPU] = sdk.cpu
	}
	if !sdk.memory.IsZero() {
		requests[corev1.ResourceMemory] = sdk.memory
		limits[corev1.ResourceMemory] = sdk.memory
	}
	sidecar.Resources.Requests = requests
	sidecar.Resources.Limits = limits

	if sdk.alwaysPull {
		sidecar.ImagePullPolicy = corev1.PullAlways
	}
	return healthCheck(gs, sidecar, "health")
}

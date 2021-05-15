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
	"strconv"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/ocgi/carrier/pkg/apis/carrier"
	carrierv1alpha1 "github.com/ocgi/carrier/pkg/apis/carrier/v1alpha1"
	"github.com/ocgi/carrier/pkg/util"
)

const (
	// ToBeDeletedTaint is a taint used to make the node unschedulable.
	ToBeDeletedTaint = "ToBeDeletedByClusterAutoscaler"
)

// ApplyDefaults applies default values to the GameServer if they are not already populated
func ApplyDefaults(gs *carrierv1alpha1.GameServer) {
	if gs.Annotations == nil {
		gs.Annotations = map[string]string{}
	}
	gs.Annotations[carrier.GroupName] = carrierv1alpha1.SchemeGroupVersion.String()
	gs.Finalizers = append(gs.Finalizers, carrier.GroupName)

	applySpecDefaults(gs)
	applyStatusDefaults(gs)
}

// ApplyDefaults applies default values to the GameServerSpec if they are not already populated
func applySpecDefaults(gs *carrierv1alpha1.GameServer) {
	gss := &gs.Spec
	if isHostPortNetwork(gs.Annotations) {
		applyPortDefaults(gss)
	}
	applyHealthDefaults(gss)
	applySchedulingDefaults(gss)
	applySdkServerDefaults(gss)
}

// applySdkServerDefaults applies the default log level ("Info") for the sidecar
func applySdkServerDefaults(gss *carrierv1alpha1.GameServerSpec) {
	if gss.SdkServer.LogLevel == "" {
		gss.SdkServer.LogLevel = carrierv1alpha1.SdkServerLogLevelInfo
	}
	if gss.SdkServer.GRPCPort == 0 {
		gss.SdkServer.GRPCPort = 9020
	}
	if gss.SdkServer.HTTPPort == 0 {
		gss.SdkServer.HTTPPort = 9021
	}
}

// applyHealthDefaults applies health checking defaults
func applyHealthDefaults(gss *carrierv1alpha1.GameServerSpec) {
	if !gss.Health.Disabled {
		if gss.Health.PeriodSeconds <= 0 {
			gss.Health.PeriodSeconds = 5
		}
		if gss.Health.FailureThreshold <= 0 {
			gss.Health.FailureThreshold = 3
		}
		if gss.Health.InitialDelaySeconds <= 0 {
			gss.Health.InitialDelaySeconds = 5
		}
	}
}

// applyStatusDefaults applies Status defaults
func applyStatusDefaults(gs *carrierv1alpha1.GameServer) {
	if gs.Status.State == "" {
		gs.Status.State = carrierv1alpha1.GameServerStarting
	}
}

// applyPortDefaults applies default values for all ports
func applyPortDefaults(gss *carrierv1alpha1.GameServerSpec) {
	for i, p := range gss.Ports {
		// basic spec
		if p.PortPolicy == "" {
			gss.Ports[i].PortPolicy = carrierv1alpha1.Dynamic
		}

		if p.Protocol == "" {
			gss.Ports[i].Protocol = "UDP"
		}
	}
}

func applySchedulingDefaults(gss *carrierv1alpha1.GameServerSpec) {
	if gss.Scheduling == "" {
		gss.Scheduling = carrierv1alpha1.MostAllocated
	}
}

// IsDeletable returns false if the server is currently not deletable
func IsDeletable(gs *carrierv1alpha1.GameServer) bool {
	if IsBeforeReady(gs) {
		return true
	}
	condMap := make(map[string]carrierv1alpha1.ConditionStatus, len(gs.Status.Conditions))
	for _, condition := range gs.Status.Conditions {
		condMap[string(condition.Type)] = condition.Status
	}
	for _, gate := range gs.Spec.DeletableGates {
		if v, ok := condMap[gate]; !ok || v != carrierv1alpha1.ConditionTrue {
			return false
		}
	}
	return true
}

// IsBeingDeleted returns true if the server is in the process of being deleted.
func IsBeingDeleted(gs *carrierv1alpha1.GameServer) bool {
	return !gs.DeletionTimestamp.IsZero() || gs.Status.State == carrierv1alpha1.GameServerFailed || gs.Status.State == carrierv1alpha1.GameServerExited
}

// IsBeforeReady returns true if the GameServer Status has yet to move to or past the Ready
// state in its lifecycle, such as Allocated or Reserved, or any of the Error/Unhealthy states
func IsBeforeReady(gs *carrierv1alpha1.GameServer) bool {
	if gs.Status.State == "" {
		return true
	}
	if gs.Status.State == carrierv1alpha1.GameServerStarting {
		return true
	}
	condMap := make(map[string]carrierv1alpha1.ConditionStatus, len(gs.Status.Conditions))
	for _, condition := range gs.Status.Conditions {
		condMap[string(condition.Type)] = condition.Status
	}
	for _, gate := range gs.Spec.ReadinessGates {
		if v, ok := condMap[gate]; !ok || v != carrierv1alpha1.ConditionTrue {
			return true
		}
	}
	return false
}

// IsReady returns true if the GameServer Status Condition are all OK
func IsReady(gs *carrierv1alpha1.GameServer) bool {
	condMap := make(map[string]carrierv1alpha1.ConditionStatus, len(gs.Status.Conditions))
	for _, condition := range gs.Status.Conditions {
		condMap[string(condition.Type)] = condition.Status
	}
	for _, gate := range gs.Spec.ReadinessGates {
		if v, ok := condMap[gate]; !ok || v != carrierv1alpha1.ConditionTrue {
			return false
		}
	}
	return true
}

// IsOutOfService checks if a gameserver is marked out of service, and a delete candidate
func IsOutOfService(gs *carrierv1alpha1.GameServer) bool {
	for _, constraint := range gs.Spec.Constraints {
		if constraint.Type != carrierv1alpha1.NotInService {
			continue
		}
		if *constraint.Effective == true {
			return true
		}
	}
	return false
}

// ApplyToPodContainer applies func(v1.Container) to the specified container in the pod.
// Returns an error if the container is not found.
func ApplyToPodContainer(pod *corev1.Pod, containerName string, f func(corev1.Container) corev1.Container) error {
	for i, c := range pod.Spec.Containers {
		if c.Name == containerName {
			pod.Spec.Containers[i] = f(c)
			return nil
		}
	}
	return errors.Errorf("failed to find container named %s in pod spec", containerName)
}

// buildPod creates a new Pod from the PodTemplateSpec
// attached to the GameServer resource
func buildPod(gs *carrierv1alpha1.GameServer, sa string, sidecars ...corev1.Container) (*corev1.Pod, error) {
	pod := &corev1.Pod{
		ObjectMeta: *gs.Spec.Template.ObjectMeta.DeepCopy(),
		Spec:       *gs.Spec.Template.Spec.DeepCopy(),
	}

	podObjectMeta(gs, pod)
	if isHostPortNetwork(gs.Annotations) {
		i, gsContainer, err := FindGameServerContainer(gs)
		// this shouldn't happen, but if it does.
		if err != nil {
			return pod, err
		}
		for _, p := range gs.Spec.Ports {
			if p.ContainerPort != nil {
				cp := corev1.ContainerPort{
					ContainerPort: *p.ContainerPort,
					Protocol:      p.Protocol,
				}
				cp.HostPort = *p.HostPort
				gsContainer.Ports = append(gsContainer.Ports, cp)
			}
			if p.ContainerPortRange != nil && p.HostPortRange != nil {
				for idx := p.ContainerPortRange.MinPort; idx <= p.ContainerPortRange.MaxPort; idx++ {
					cp := corev1.ContainerPort{
						ContainerPort: idx,
						Protocol:      p.Protocol,
					}
					cp.HostPort = p.HostPortRange.MinPort + (p.HostPortRange.MinPort - idx)
					gsContainer.Ports = append(gsContainer.Ports, cp)
				}
			}
			pod.Spec.Containers[i] = gsContainer
		}
	}
	pod.Spec.Containers = append(pod.Spec.Containers, sidecars...)
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	injectPodScheduling(gs, pod)
	// if the service account is not set, then you are in the "opinionated"
	// mode. If the user sets the service account, we assume they know what they are
	// doing, and don't disable the gameserver container.
	if pod.Spec.ServiceAccountName == "" {
		pod.Spec.ServiceAccountName = sa
		err := DisableServiceAccount(pod)
		if err != nil {
			return pod, err
		}
	}
	addGameServerHealthCheck(gs, pod, "/gshealthz")
	addSDKServerEnv(gs, pod)
	return pod, nil
}

// podObjectMeta configures the pod ObjectMeta details
func podObjectMeta(gs *carrierv1alpha1.GameServer, pod *corev1.Pod) {
	pod.GenerateName = ""
	pod.ResourceVersion = ""
	pod.UID = ""
	pod.Name = gs.Name
	pod.Namespace = gs.Namespace
	// pod annotations and labels will be overridden by gs if they have some keys
	pod.Labels = merge(pod.Labels, gs.Labels)
	pod.Annotations = merge(pod.Annotations, gs.Annotations)
	pod.Labels[util.RoleLabel] = util.GameServerLabelRole
	pod.Labels[util.GameServerPodLabel] = gs.Name
	ref := metav1.NewControllerRef(gs, carrierv1alpha1.SchemeGroupVersion.WithKind("GameServer"))
	pod.OwnerReferences = append(pod.OwnerReferences, *ref)

	// Add Carrier version into Pod Annotations
	pod.Annotations[carrier.GroupName] = carrierv1alpha1.SchemeGroupVersion.Version
}

// injectPodScheduling helps inject podAffinity to podSpec if the policy is `MostAllocated`
func injectPodScheduling(gs *carrierv1alpha1.GameServer, pod *corev1.Pod) {
	if gs.Spec.Scheduling == carrierv1alpha1.MostAllocated {
		if pod.Spec.Affinity == nil {
			pod.Spec.Affinity = &corev1.Affinity{}
		}
		if pod.Spec.Affinity.PodAffinity == nil {
			pod.Spec.Affinity.PodAffinity = &corev1.PodAffinity{}
		}

		term := corev1.WeightedPodAffinityTerm{
			Weight: 100,
			PodAffinityTerm: corev1.PodAffinityTerm{
				TopologyKey:   "kubernetes.io/hostname",
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{util.RoleLabel: util.GameServerLabelRole}},
			},
		}

		pod.Spec.Affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution = append(pod.Spec.Affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution, term)
	}
}

// DisableServiceAccount disables the service account for the gameserver container
func DisableServiceAccount(pod *corev1.Pod) error {
	// gameservers don't get access to the k8s api.
	emptyVol := corev1.Volume{Name: "empty", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}
	pod.Spec.Volumes = append(pod.Spec.Volumes, emptyVol)
	mount := corev1.VolumeMount{MountPath: "/var/run/secrets/kubernetes.io/serviceaccount", Name: emptyVol.Name, ReadOnly: true}

	return ApplyToPodContainer(pod, util.GameServerContainerName, func(c corev1.Container) corev1.Container {
		c.VolumeMounts = append(c.VolumeMounts, mount)

		return c
	})
}

// CountPorts returns the number of
// ports that match condition function
func CountPorts(gs *carrierv1alpha1.GameServer, f func(policy carrierv1alpha1.PortPolicy) bool) (int, bool) {
	count := 0
	continuous := false
	for _, p := range gs.Spec.Ports {
		if f(p.PortPolicy) {
			if p.ContainerPort != nil {
				count++
			} else {
				if p.ContainerPortRange == nil {
					continue
				}
				continuous = true
				cpr := p.ContainerPortRange
				count += int(cpr.MaxPort-cpr.MinPort) + 1
			}
		}
	}
	return count, continuous
}

// isGameServerPod returns if this Pod is a Pod that comes from a GameServer
func isGameServerPod(pod *corev1.Pod) bool {
	if util.GameServerRolePodSelector.Matches(labels.Set(pod.Labels)) {
		owner := metav1.GetControllerOf(pod)
		return owner != nil && owner.Kind == "GameServer"
	}

	return false
}

// applyGameServerAddressAndPort gathers the address and port details from the node and pod
// and applies them to the GameServer that is passed in, and returns it.
func applyGameServerAddressAndPort(gs *carrierv1alpha1.GameServer, pod *corev1.Pod) {
	gs.Status.Address = pod.Status.PodIP
	gs.Status.NodeName = pod.Spec.NodeName
	if isHostPortNetwork(gs.Annotations) {
		var ingress []carrierv1alpha1.LoadBalancerIngress
		for _, p := range gs.Spec.Ports {
			ingress = append(ingress, carrierv1alpha1.LoadBalancerIngress{
				IP: pod.Spec.NodeName,
				Ports: []carrierv1alpha1.LoadBalancerPort{
					{
						ContainerPort:      p.ContainerPort,
						ExternalPort:       p.HostPort,
						ContainerPortRange: p.ContainerPortRange,
						ExternalPortRange:  p.HostPortRange,
						Protocol:           p.Protocol,
					},
				},
			})
		}
		gs.Status.LoadBalancerStatus = &carrierv1alpha1.LoadBalancerStatus{
			Ingress: ingress,
		}
	}
}

// isBeforePodCreated checks to see if the GameServer is in a state in which the pod could not have been
// created yet. This includes "Starting" in which a pod MAY exist, but may not yet be available, depending on when the
// informer cache updates
func isBeforePodCreated(gs *carrierv1alpha1.GameServer) bool {
	state := gs.Status.State
	return state == "" || state == carrierv1alpha1.GameServerStarting || state == carrierv1alpha1.GameServerUnknown
}

// isHostPortNetwork checks if pod runs as hostHost
func isHostPortNetwork(ann map[string]string) bool {
	if ann == nil {
		return false
	}
	return ann[util.GameServerNetworkType] == util.GameServerHostPort
}

// FindContainer returns the container specified by the name parameter. Returns the index and the value.
// Returns an error if not found.
func FindContainer(gss *carrierv1alpha1.GameServerSpec, name string) (int, corev1.Container, error) {
	for i, c := range gss.Template.Spec.Containers {
		if c.Name == name {
			return i, c, nil
		}
	}

	return -1, corev1.Container{}, errors.Errorf("Could not find a container named %s", name)
}

// FindGameServerContainer returns the container that is specified in
// gameServer.Spec.Container. Returns the index and the value.
// Returns an error if not found
func FindGameServerContainer(gs *carrierv1alpha1.GameServer) (int, corev1.Container, error) {
	return FindContainer(&gs.Spec, util.GameServerContainerName)
}

// merge helps merge labels or annotations
func merge(one, two map[string]string) map[string]string {
	three := make(map[string]string)
	for k, v := range one {
		three[k] = v
	}
	for k, v := range two {
		three[k] = v
	}
	return three
}

// checkNodeTaintByCA checks if node is marked as deletable.
// if true we should add constraints to gs spec.
func checkNodeTaintByCA(node *corev1.Node) bool {
	if len(node.Spec.Taints) == 0 {
		return false
	}
	for _, taint := range node.Spec.Taints {
		if taint.Key == ToBeDeletedTaint {
			return true
		}
	}
	return false
}

// NotInServiceConstraint describe a constraint that gs should not be
// in service again.
func NotInServiceConstraint() carrierv1alpha1.Constraint {
	effective := true
	now := metav1.NewTime(time.Now())
	return carrierv1alpha1.Constraint{
		Type:      carrierv1alpha1.NotInService,
		Effective: &effective,
		Message:   "Carrier controller mark this game server as not in service",
		TimeAdded: &now,
	}
}

func addSDKServerEnv(gs *carrierv1alpha1.GameServer, pod *corev1.Pod) {
	var env []corev1.EnvVar
	if gs.Spec.SdkServer.GRPCPort != 0 {
		env = append(env, corev1.EnvVar{
			Name:  grpcPort,
			Value: strconv.Itoa(int(gs.Spec.SdkServer.GRPCPort)),
		})
	}
	if gs.Spec.SdkServer.HTTPPort != 0 {
		env = append(env, corev1.EnvVar{
			Name:  httpPort,
			Value: strconv.Itoa(int(gs.Spec.SdkServer.HTTPPort)),
		})
	}
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		if c.Name == sdkserverSidecarName {
			continue
		}
		c.Env = append(c.Env, env...)
		pod.Spec.Containers[i] = *c
	}
}

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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/spf13/pflag"
	apiv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	ext "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	componentbaseconfig "k8s.io/component-base/config"
	"k8s.io/klog"

	"github.com/ocgi/carrier/cmd/controller/app"
	carrierclient "github.com/ocgi/carrier/pkg/client/clientset/versioned"
	carrierinformer "github.com/ocgi/carrier/pkg/client/informers/externalversions"
	"github.com/ocgi/carrier/pkg/controllers"
	"github.com/ocgi/carrier/pkg/controllers/gameservers"
	"github.com/ocgi/carrier/pkg/controllers/gameserversets"
	"github.com/ocgi/carrier/pkg/controllers/squad"
	"github.com/ocgi/carrier/pkg/version"
)

const (
	defaultLeaseDuration = 15 * time.Second
	defaultRenewDeadline = 10 * time.Second
	defaultRetryPeriod   = 2 * time.Second
)

func main() {
	runConfig := app.NewServerRunOptions()
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()
	defer klog.Flush()
	klog.V(4).Infof("config: %v", runConfig)
	if runConfig.ShowVersion {
		fmt.Println(version.Version)
		return
	}
	version.Print()
	leaderElection := defaultLeaderElectionConfiguration()
	if len(runConfig.ElectionResourceLock) != 0 {
		leaderElection.ResourceLock = runConfig.ElectionResourceLock
	}
	kubeconfig, err := runConfig.NewConfig()
	if err != nil {
		klog.Fatal("Failed to build config")
	}

	stop := server.SetupSignalHandler()

	client := kubernetes.NewForConfigOrDie(kubeconfig)
	carrierClient := carrierclient.NewForConfigOrDie(kubeconfig)
	exClient := ext.NewForConfigOrDie(kubeconfig)

	coreFactory := informers.NewSharedInformerFactory(client, runConfig.Resync)
	carrierFactory := carrierinformer.NewSharedInformerFactory(carrierClient, runConfig.Resync)

	if !isCRDReady(exClient.ApiextensionsV1beta1().CustomResourceDefinitions()) {
		klog.Fatalf("wait for crd ready timeout")
	}

	gscontroller := gameservers.NewController(client, coreFactory, carrierClient, carrierFactory,
		runConfig.MinPort, runConfig.MaxPort)
	gsscontroller := gameserversets.NewController(client, carrierClient, carrierFactory)
	sqdcontroller := squad.NewController(client, carrierClient, carrierFactory)
	coreFactory.Start(stop)
	carrierFactory.Start(stop)
	run := func(ctx context.Context) {
		for _, c := range []controllers.Controller{gscontroller, gsscontroller, sqdcontroller} {
			go func(c controllers.Controller) {
				err := c.Run(10, ctx.Done())
				if err != nil {
					klog.Fatal("Start controller failed")
				}
			}(c)
		}
	}

	id, err := os.Hostname()
	if err != nil {
		klog.Fatalf("Unable to get hostname: %v", err)
	}

	lock, err := resourcelock.New(
		leaderElection.ResourceLock,
		runConfig.ElectionNamespace,
		runConfig.ElectionName,
		client.CoreV1(),
		client.CoordinationV1(),
		resourcelock.ResourceLockConfig{
			Identity: id,
		},
	)
	if err != nil {
		klog.Fatalf("Unable to create leader election lock: %v", err)
	}

	ctx, cancel := context.WithCancel(context.TODO())
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:          lock,
		LeaseDuration: leaderElection.LeaseDuration.Duration,
		RenewDeadline: leaderElection.RenewDeadline.Duration,
		RetryPeriod:   leaderElection.RetryPeriod.Duration,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				// Since we are committing a suicide after losing
				// mastership, we can safely ignore the argument.
				run(ctx)
			},
			OnStoppedLeading: func() {
				klog.Fatalf("lost master")
			},
		},
	})
}

func defaultLeaderElectionConfiguration() componentbaseconfig.LeaderElectionConfiguration {
	return componentbaseconfig.LeaderElectionConfiguration{
		LeaderElect:   false,
		LeaseDuration: metav1.Duration{Duration: defaultLeaseDuration},
		RenewDeadline: metav1.Duration{Duration: defaultRenewDeadline},
		RetryPeriod:   metav1.Duration{Duration: defaultRetryPeriod},
		ResourceLock:  resourcelock.LeasesResourceLock,
	}
}

func isCRDReady(client v1beta1.CustomResourceDefinitionInterface) bool {
	var wg sync.WaitGroup
	var errs []error
	for _, crdName := range []string{"gameservers", "gameserversets", "squads"} {
		wg.Add(1)
		go func(crdName string) {
			defer wg.Done()
			crd, err := client.Get(crdName, metav1.GetOptions{})
			if err != nil {
				errs = append(errs, err)
				return
			}
			found := false
			for _, cond := range crd.Status.Conditions {
				if cond.Type == apiv1beta1.Established &&
					cond.Status == apiv1beta1.ConditionTrue {
					found = true
					return
				}
			}
			if !found {
				errs = append(errs, fmt.Errorf("crd %v is not ready now", crdName))
			}
		}(crdName)
	}
	wg.Wait()
	if len(errs) > 0 {
		return false
	}
	return true
}

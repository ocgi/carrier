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

package app

import (
	"time"

	"github.com/spf13/pflag"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type RunOptions struct {
	KubeconfigPath    string
	MasterUrl         string
	QPS               int
	Burst             int
	Resync            time.Duration
	ElectionName      string
	ElectionNamespace string

	SDKServiceAccount    string
	ElectionResourceLock string

	ShowVersion bool
}

func NewServerRunOptions() *RunOptions {
	options := &RunOptions{}
	options.addKubeFlags()
	options.addElectionFlags()
	options.addControllerFlags()
	return options
}

func (s *RunOptions) addKubeFlags() {
	pflag.DurationVar(&s.Resync, "resync", 10*time.Minute, "Time to resync from apiserver.")
	pflag.StringVar(&s.KubeconfigPath, "kubeconfig-path", "", "Absolute path to the kubeconfig file.")
	pflag.StringVar(&s.MasterUrl, "master", "", "Master url.")
	pflag.IntVar(&s.QPS, "qps", 100, "qps of auto scaler.")
	pflag.IntVar(&s.Burst, "burst", 200, "burst of auto scaler.")
}

func (s *RunOptions) addElectionFlags() {
	pflag.StringVar(&s.ElectionName, "election-name", "carrier-controller", "election name.")
	pflag.StringVar(&s.ElectionNamespace, "election-namespace", "kube-system", "election namespace.")
	pflag.StringVar(&s.ElectionResourceLock, "election-resource-lock", "leases", "election resource type, support endpoints, leases, configmaps and so on.")

}

func (s *RunOptions) addControllerFlags() {
	pflag.BoolVar(&s.ShowVersion, "version", s.ShowVersion, "version of carrier.")
}

func (s *RunOptions) NewConfig() (*rest.Config, error) {
	var (
		config *rest.Config
		err    error
	)
	config, err = rest.InClusterConfig()
	if err != nil {
		config, err = clientcmd.BuildConfigFromFlags(s.MasterUrl, s.KubeconfigPath)
		if err != nil {
			return nil, err
		}
	}
	config.Burst = s.Burst
	config.QPS = float32(s.QPS)
	return config, nil
}

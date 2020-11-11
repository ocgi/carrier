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

package apis

const (
	// MostAllocated strategy will allocate GameServers
	// on Nodes with the most allocated by inject PodInterAffinity.
	// For dynamic clusters, this Strategy will may cause less GameServers migration when clusters auto scale.
	MostAllocated SchedulingStrategy = "MostAllocated"

	// LeastAllocated strategy will prioritise allocating GameServers
	// on Nodes with the least allocated(scheduler default).
	// For dynamic clusters, this Strategy will may cause more GameServers migration when clusters auto scale.
	LeastAllocated SchedulingStrategy = "LeastAllocated"
)

// SchedulingStrategy is the strategy that a Squad & GameServers will use
// when scheduling GameServers' Pods across a cluster.
type SchedulingStrategy string

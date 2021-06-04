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

// TODO: replace unit test with table driven tests

package gameservers

import (
	"testing"
)

func TestAllocate(t *testing.T) {
	// unallocatable
	allo := NewMinMaxAllocator(100, 200)
	ports, err := allo.Allocate("test", "test1", 102, false)
	if err == nil {
		t.Errorf("desired can not allocate: %v", ports)
	}

	// allocate continuous
	allo = NewMinMaxAllocator(100, 200)
	ports, err = allo.Allocate("test", "test1", 19, true)
	if err != nil {
		t.Errorf("desired can allocate: %v", err)
	}
	if ports[0] != 100 || ports[18] != 118 {
		t.Errorf("desired：%v, %v,  get: %v, %v", 100, 118, ports[0], ports[18])
	}

	// allocate continuous, has some ref
	allo = NewMinMaxAllocator(100, 200)
	ports1, err := allo.Allocate("test", "test1", 19, true)
	ports2, err := allo.Allocate("test", "test2", 19, true)
	if err != nil {
		t.Errorf("desired can allocate: %v", err)
	}
	if ports1[0] != ports2[0] || ports1[2] != ports2[2] {
		t.Errorf("port allocated not same")
	}

	// allocate some already allocated, continuous, false
	allo = NewMinMaxAllocator(100, 200)
	port1 := getPorts(100, 165)
	port2 := getPorts(170, 190)
	allo.SetUsed("xx", "xxx", port1)
	allo.SetUsed("xx1", "xxx2", port2)
	ports, err = allo.Allocate("test", "test1", 5, false)
	if err != nil {
		t.Errorf("desired can not allocate: %v", err)
	}
	if ports[0] != 166 || ports[4] != 191 {
		t.Errorf("desired：%v, %v,  get: %v, %v", 166, 191, ports[0], ports[4])
	}

	// allocate some already allocated, continuous, false
	allo = NewMinMaxAllocator(100, 200)
	port1 = getPorts(100, 165)
	port2 = getPorts(170, 190)
	allo.SetUsed("xx", "xxx", port1)
	allo.SetUsed("xx1", "xxx2", port2)
	ports, err = allo.Allocate("test", "test1", 12, true)
	if err == nil {
		t.Errorf("desired can not allocate: %v, desired: %v", ports, ErrRangeFull)
	}
}

func TestRelease(t *testing.T) {
	// allocate some already allocated, continuous, false
	allo2 := NewMinMaxAllocator(100, 200)
	port1 := getPorts(100, 165)
	allo2.SetUsed("xx", "xxx", port1)
	allo2.SetUsed("xx", "xxx1", port1)

	// release one
	allo2.Release("xx", "xxx", port1)
	_, ok := allo2.usedPair["xx"]
	if !ok {
		t.Error("desired pair, still exist, but not")
	}

	// release again
	allo2.Release("xx", "xxx1", port1)
	_, ok = allo2.usedPair["xx"]
	if ok {
		t.Error("desired pair, not exist, but exist")
	}
	if allo2.used[101] {
		t.Error("desire 100 not used, but used")
	}

	// test release and allocate again
	ports, err := allo2.Allocate("test", "test1", 19, true)
	if err != nil {
		t.Errorf("desired can not allocate: %v", err)
	}
	if ports[0] != 100 || ports[18] != 118 {
		t.Errorf("desired：%v, %v,  get: %v, %v", 100, 118, ports[0], ports[18])
	}
}

func getPorts(min, max int) []int {
	ps := []int{}
	for i := min; i <= max; i++ {
		ps = append(ps, i)
	}
	return ps
}

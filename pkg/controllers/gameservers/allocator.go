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
	"errors"
	"sync"
)

var (
	//ErrRangeFull returned when no more free values in the pool.
	ErrRangeFull = errors.New("range full")
)

//MinMaxAllocator defines allocator struct.
type MinMaxAllocator struct {
	lock sync.Mutex
	min  int
	max  int
	free int
	used map[int]bool
	// usedPair is ownerId: port array
	usedPair map[string]allocateInfo
}

type allocateInfo struct {
	ports       []int
	allocatedID map[string]string
}

var _ Allocator = &MinMaxAllocator{}

// Allocator is an Interface that can adjust its min/max range.
// Allocator should be threadsafe
type Allocator interface {
	Allocate(string, string, int, bool) ([]int, error)
	Release(string, string, []int)
	SetUsed(string, string, []int)
}

// NewMinMaxAllocator return a new allocator or error based on provided min/max value.
func NewMinMaxAllocator(min, max int) *MinMaxAllocator {
	return &MinMaxAllocator{
		min:      min,
		max:      max,
		free:     1 + max - min,
		used:     map[int]bool{},
		usedPair: map[string]allocateInfo{},
	}
}

//Allocate allocates multi continuous ports from the allocator.
func (a *MinMaxAllocator) Allocate(refId, id string, count int, continuous bool) ([]int, error) {
	a.lock.Lock()
	defer a.lock.Unlock()

	_, ok := a.usedPair[refId]
	if ok {
		a.usedPair[refId].allocatedID[id] = id
		return a.usedPair[refId].ports, nil
	}

	// Fast check if we're out of items
	if a.free < count {
		return nil, ErrRangeFull
	}
	min, max := a.min, a.max
	for {
		var portSlice []int
		// Scan from the minimum until we find a free range
		for i := 0; i < count; i++ {
			for i := min; i <= max; i++ {
				if !a.has(i) {
					a.used[i] = true
					a.free--
					portSlice = append(portSlice, i)
					break
				}
			}
			if len(portSlice) >= count {
				break
			}
		}

		if len(portSlice) == 0 {
			return nil, ErrRangeFull
		}

		if len(portSlice) == count && !continuous {
			a.usedPair[refId] = allocateInfo{
				ports:       portSlice,
				allocatedID: map[string]string{id: id},
			}
			return portSlice, nil
		}
		// allocate finished
		if portSlice[len(portSlice)-1]-portSlice[0] == count-1 {
			a.usedPair[refId] = allocateInfo{
				ports:       portSlice,
				allocatedID: map[string]string{id: id},
			}
			return portSlice, nil
		}
		// a.usedPair still empty
		a.release(refId, id, portSlice)
		min = portSlice[len(portSlice)-1]
		if min == max {
			break
		}
	}
	return nil, ErrRangeFull
}

//Release free/delete provided value from the allocator.
func (a *MinMaxAllocator) Release(refId, id string, ports []int) {
	a.lock.Lock()
	defer a.lock.Unlock()
	a.release(refId, id, ports)
}

//Release free/delete provided value from the allocator.
func (a *MinMaxAllocator) release(refId, id string, ports []int) {
	deleted := false
	aInfo, ok := a.usedPair[refId]
	if !ok {
		deleted = true
	}

	delete(aInfo.allocatedID, id)
	if len(aInfo.allocatedID) == 0 {
		deleted = true
	}
	if !deleted {
		return
	}

	delete(a.usedPair, refId)
	for _, port := range ports {
		if a.inRange(port) {
			a.free++
			delete(a.used, port)
		}
	}
}

func (a *MinMaxAllocator) has(i int) bool {
	_, ok := a.used[i]
	return ok
}

//SetUsed sets a list of ports as used.
func (a *MinMaxAllocator) SetUsed(refId string, id string, ports []int) {
	a.lock.Lock()
	defer a.lock.Unlock()
	if _, ok := a.usedPair[refId]; ok {
		a.usedPair[refId].allocatedID[id] = id
		return
	}

	for _, port := range ports {
		a.used[port] = true
		a.free--
	}
	a.usedPair[refId] = allocateInfo{
		ports:       ports,
		allocatedID: map[string]string{id: id},
	}
}

func (a *MinMaxAllocator) inRange(i int) bool {
	return a.min <= i && i <= a.max
}

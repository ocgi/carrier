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

package kube

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

type test struct {
	old           interface{}
	new           interface{}
	expectedError bool
}

func TestCreateMergePatch(t *testing.T) {
	tests := []struct {
		old      interface{}
		new      interface{}
		expected string
	}{
		{
			old: &corev1.Pod{
				Spec: corev1.PodSpec{
					Hostname: "test",
				},
			},
			new: &corev1.Pod{
				Spec: corev1.PodSpec{
					Hostname: "test-new",
				},
				Status: corev1.PodStatus{
					Reason: "test",
				},
			},
			expected: `{"spec":{"hostname":"test-new"},"status":{"reason":"test"}}`,
		},

		{
			old: &corev1.Pod{
				Spec: corev1.PodSpec{
					Hostname: "test",
				},
				Status: corev1.PodStatus{
					Reason: "test1",
				},
			},
			new: &corev1.Pod{
				Status: corev1.PodStatus{
					Reason: "test",
				},
			},
			expected: `{"spec":{"hostname":null},"status":{"reason":"test"}}`,
		},
	}

	for _, tcase := range tests {
		patch, err := CreateMergePatch(tcase.old, tcase.new)
		if err != nil {
			t.Error(err)
		}
		if string(patch) != tcase.expected {
			t.Errorf("expected %v get %v", tcase.expected, string(patch))
		}
	}
}

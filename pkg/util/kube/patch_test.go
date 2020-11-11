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

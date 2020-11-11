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

package v1alpha1

import (
	v1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// WebhookConfiguration is the data structure for a WebhookConfiguration resource.
type WebhookConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Webhooks []Configurations `json:"webhooks"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// WebhookConfigurationList is a list of WebhookConfiguration resources
type WebhookConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []WebhookConfiguration `json:"items"`
}

type Configurations struct {
	ClientConfig   v1.WebhookClientConfig `json:"clientConfig"`
	Name           *string                `json:"name,omitempty"`
	Type           *string                `json:"type,omitempty"`
	TimeoutSeconds *int32                 `json:"timeoutSeconds,omitempty"`
	PeriodSeconds  *int32                 `json:"periodSeconds,omitempty"`
}

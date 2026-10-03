package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SyncthingNode describes the Syncthing identity associated with one Kubernetes node.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
type SyncthingNode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SyncthingNodeSpec   `json:"spec,omitempty"`
	Status            SyncthingNodeStatus `json:"status,omitempty"`
}

type SyncthingNodeSpec struct {
	NodeName  string   `json:"nodeName"`
	DeviceID  string   `json:"deviceID"`
	Addresses []string `json:"addresses,omitempty"`
}

type SyncthingNodeStatus struct {
	Ready            bool               `json:"ready,omitempty"`
	SyncthingVersion string             `json:"syncthingVersion,omitempty"`
	ObservedDeviceID string             `json:"observedDeviceID,omitempty"`
	LastSeen         *metav1.Time       `json:"lastSeen,omitempty"`
	Conditions       []metav1.Condition `json:"conditions,omitempty"`
}

// SyncthingNodeList contains SyncthingNode objects.
// +kubebuilder:object:root=true
type SyncthingNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SyncthingNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SyncthingNode{}, &SyncthingNodeList{})
}

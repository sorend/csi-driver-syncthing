package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const VolumeProtectionFinalizer = "syncthing-storage.sorend.github.com/volume-protection"

// SyncthingVolume describes the desired replicas for one CSI volume/folder.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
type SyncthingVolume struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SyncthingVolumeSpec   `json:"spec,omitempty"`
	Status            SyncthingVolumeStatus `json:"status,omitempty"`
}

type SyncthingVolumeSpec struct {
	VolumeHandle    string            `json:"volumeHandle"`
	FolderID        string            `json:"folderID"`
	DesiredReplicas []DesiredReplica  `json:"desiredReplicas,omitempty"`
	InitialSync     InitialSyncPolicy `json:"initialSync,omitempty"`
	Retention       *ReplicaRetention `json:"retention,omitempty"`
}

type DesiredReplica struct {
	NodeName string `json:"nodeName"`
	Mode     string `json:"mode,omitempty"`
}

type InitialSyncPolicy struct {
	Policy string `json:"policy,omitempty"`
}

type ReplicaRetention struct {
	AfterDetach string `json:"afterDetach,omitempty"`
}

type SyncthingVolumeStatus struct {
	Phase              string             `json:"phase,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Replicas           []ReplicaStatus    `json:"replicas,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

type ReplicaStatus struct {
	NodeName           string       `json:"nodeName"`
	State              string       `json:"state"`
	Completion         int32        `json:"completion,omitempty"`
	NeedBytes          int64        `json:"needBytes,omitempty"`
	NeedItems          int64        `json:"needItems,omitempty"`
	Connected          bool         `json:"connected,omitempty"`
	ConfiguredRevision string       `json:"configuredRevision,omitempty"`
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`
}

// SyncthingVolumeList contains SyncthingVolume objects.
// +kubebuilder:object:root=true
type SyncthingVolumeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SyncthingVolume `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SyncthingVolume{}, &SyncthingVolumeList{})
}

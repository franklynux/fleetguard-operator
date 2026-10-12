/*
Copyright 2026.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// TargetReference identifies the kind of resource a TTLCleanup should watch
// for expiry, optionally narrowed to a namespace and/or a label selector.
type TargetReference struct {
	// APIVersion of the target resource, e.g. "batch/v1" or "v1" for core resources.
	// +kubebuilder:validation:Required
	APIVersion string `json:"apiVersion"`

	// Kind of the target resource, e.g. "Job", "Pod", or a custom resource Kind.
	// +kubebuilder:validation:Required
	Kind string `json:"kind"`

	// Namespace to restrict the search to. Empty means cluster-wide
	// (only meaningful for namespace-scoped resources; a cluster-scoped
	// Kind ignores this field).
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// LabelSelector further narrows which objects of the given Kind are
	// considered. Nil/empty selects all objects of that Kind (within Namespace,
	// if set).
	// +optional
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
}

// TTLCleanupSpec defines the desired state of TTLCleanup.
type TTLCleanupSpec struct {
	// TargetRef identifies which resources this TTLCleanup manages.
	// +kubebuilder:validation:Required
	TargetRef TargetReference `json:"targetRef"`

	// TTL is how long a matched object may live, measured from its
	// creationTimestamp, before it is considered expired.
	// +kubebuilder:validation:Required
	TTL metav1.Duration `json:"ttl"`

	// DryRun, when true, makes the controller record and emit Events for
	// expired matches without deleting them. Useful for validating a
	// TargetRef/selector before trusting it to delete things.
	// +optional
	// +kubebuilder:default=false
	DryRun bool `json:"dryRun,omitempty"`
}

// TTLCleanupStatus defines the observed state of TTLCleanup.
type TTLCleanupStatus struct {
	// LastCleanupTime is when the controller last completed a reconcile pass.
	// +optional
	LastCleanupTime *metav1.Time `json:"lastCleanupTime,omitempty"`

	// ObjectsMatched is the count of objects matching TargetRef as of the
	// last reconcile (expired or not).
	// +optional
	ObjectsMatched int32 `json:"objectsMatched,omitempty"`

	// ObjectsDeleted is the count of expired objects deleted in the last
	// reconcile pass. Stays 0 while DryRun is true.
	// +optional
	ObjectsDeleted int32 `json:"objectsDeleted,omitempty"`

	// Conditions represent the latest available observations of the
	// TTLCleanup's state.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.targetRef.kind`
// +kubebuilder:printcolumn:name="TTL",type=string,JSONPath=`.spec.ttl`
// +kubebuilder:printcolumn:name="DryRun",type=boolean,JSONPath=`.spec.dryRun`
// +kubebuilder:printcolumn:name="Matched",type=integer,JSONPath=`.status.objectsMatched`
// +kubebuilder:printcolumn:name="Deleted",type=integer,JSONPath=`.status.objectsDeleted`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TTLCleanup is the Schema for the ttlcleanups API.
type TTLCleanup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TTLCleanupSpec   `json:"spec,omitempty"`
	Status TTLCleanupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TTLCleanupList contains a list of TTLCleanup.
type TTLCleanupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TTLCleanup `json:"items"`
}

func init() {
	SchemeBuilder.Register(addKnownTypes)
}

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion, &TTLCleanup{}, &TTLCleanupList{})
	return nil
}

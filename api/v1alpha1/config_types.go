package v1alpha1

import (
	"github.com/openmcp-project/openmcp-operator/api/common"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ProviderConfigSpec defines the desired state of ProviderConfig for CAPI clusters.
type ProviderConfigSpec struct {
	// ProviderRef is a reference to the ClusterProvider resource this configuration belongs to.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="providerRef is immutable"
	ProviderRef common.LocalObjectReference `json:"providerRef"`

	// ClusterClassNamespace is the namespace in which the ClusterClass and its referenced
	// templates reside. Defaults to the same namespace as the provisioned Cluster.
	// +optional
	ClusterClassNamespace string `json:"clusterClassNamespace,omitempty"`

	// ClusterClassName is the name of the ClusterClass to use for provisioned clusters.
	// +kubebuilder:validation:MinLength=1
	ClusterClassName string `json:"clusterClassName"`

	// TopologyVariables is a list of ClusterClass topology variables to set on every
	// provisioned Cluster. Use this to supply required variables such as "project" or "region".
	// +optional
	TopologyVariables []TopologyVariable `json:"topologyVariables,omitempty"`
}

// TopologyVariable is a name/value pair passed as a ClusterClass topology variable.
type TopologyVariable struct {
	// Name is the variable name as defined in the ClusterClass.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Value is the raw JSON value for the variable.
	// +kubebuilder:pruning:PreserveUnknownFields
	Value apiextensionsv1.JSON `json:"value"`
}

// ProviderConfigStatus defines the observed state of ProviderConfig.
type ProviderConfigStatus struct {
	common.Status `json:",inline"`
}

// ProviderConfig is the Schema for the CAPI cluster provider configuration.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=capicfg
// +kubebuilder:printcolumn:JSONPath=".status.phase",name="Phase",type=string
// +kubebuilder:metadata:labels="openmcp.cloud/cluster=platform"
type ProviderConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ProviderConfigSpec   `json:"spec,omitempty"`
	Status ProviderConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ProviderConfigList contains a list of ProviderConfig.
type ProviderConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProviderConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &ProviderConfig{}, &ProviderConfigList{})
		return nil
	})
}

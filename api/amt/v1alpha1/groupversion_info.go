// Package v1alpha1 contains the API types for Intel AMT BMC management.
//
// +kubebuilder:object:generate=true
// +groupName=amt.tinkerbell.org
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion is the group and version for the AMT API.
var GroupVersion = schema.GroupVersion{Group: "amt.tinkerbell.org", Version: "v1alpha1"}

var (
	// SchemeBuilder registers the AMT types with a scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the AMT types to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&AMTDevice{}, &AMTDeviceList{},
		&AMTProfile{}, &AMTProfileList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}

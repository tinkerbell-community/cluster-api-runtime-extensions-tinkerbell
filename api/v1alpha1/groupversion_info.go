// +kubebuilder:object:generate=true
// +groupName=variables.runtime.tinkerbell.org
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion identifies the never-installed variable CRD group. No scheme
// registration exists on purpose: these types are schema sources for the
// DiscoverVariables handler, not API objects.
var GroupVersion = schema.GroupVersion{Group: "variables.runtime.tinkerbell.org", Version: "v1alpha1"}

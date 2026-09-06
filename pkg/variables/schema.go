// Package variables converts the embedded, never-installed variable CRDs into
// ClusterClass variable schema (spec §4.3, the CAREN pattern): authoring stays
// kubebuilder markers on Go types, and defaults, validation, and CEL
// XValidations are enforced by core CAPI's variable machinery — zero variable
// webhook code in this repo.
package variables

import (
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/yaml"
)

// SchemaFromCRDYAML extracts the served version's `spec` schema from a
// generated CRD manifest and converts it to ClusterClass variable schema. The
// CRD envelope (apiVersion/kind/metadata) never leaks into the variable.
func SchemaFromCRDYAML(crdYAML []byte) (clusterv1.VariableSchema, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := yaml.UnmarshalStrict(crdYAML, crd); err != nil {
		return clusterv1.VariableSchema{}, fmt.Errorf("parsing embedded CRD: %w", err)
	}
	if len(crd.Spec.Versions) != 1 || crd.Spec.Versions[0].Schema == nil || crd.Spec.Versions[0].Schema.OpenAPIV3Schema == nil {
		return clusterv1.VariableSchema{}, fmt.Errorf("embedded CRD %q must carry exactly one version with a schema", crd.Name)
	}
	root := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
	spec, ok := root.Properties["spec"]
	if !ok {
		return clusterv1.VariableSchema{}, fmt.Errorf("embedded CRD %q has no spec schema", crd.Name)
	}
	return clusterv1.VariableSchema{OpenAPIV3Schema: convert(&spec)}, nil
}

// MustSchemaFromCRDYAML is SchemaFromCRDYAML for init-time embedding, where a
// failure is a build defect, not a runtime condition.
func MustSchemaFromCRDYAML(crdYAML []byte) clusterv1.VariableSchema {
	schema, err := SchemaFromCRDYAML(crdYAML)
	if err != nil {
		panic(err)
	}
	return schema
}

// convertScalarConstraints copies the pointer-typed flag and bound fields.
func convertScalarConstraints(in *apiextensionsv1.JSONSchemaProps, out *clusterv1.JSONSchemaProps) {
	if in.UniqueItems {
		out.UniqueItems = ptr.To(true)
	}
	if in.ExclusiveMaximum {
		out.ExclusiveMaximum = ptr.To(true)
	}
	if in.ExclusiveMinimum {
		out.ExclusiveMinimum = ptr.To(true)
	}
	if in.XPreserveUnknownFields != nil && *in.XPreserveUnknownFields {
		out.XPreserveUnknownFields = ptr.To(true)
	}
	if in.XIntOrString {
		out.XIntOrString = ptr.To(true)
	}
	if in.Maximum != nil {
		bound := int64(*in.Maximum)
		out.Maximum = &bound
	}
	if in.Minimum != nil {
		bound := int64(*in.Minimum)
		out.Minimum = &bound
	}
}

// convert maps the apiextensions schema subset kubebuilder emits onto the
// ClusterClass JSONSchemaProps. Anything ClusterClass cannot express —
// anyOf/oneOf/allOf/not, most prominently from intstr/Quantity fields — is a
// hard error here rather than a runtime variable-discovery failure with a
// non-obvious message (the `yq type: string` hazard of spec §4.3).
func convert(in *apiextensionsv1.JSONSchemaProps) clusterv1.JSONSchemaProps {
	if len(in.AnyOf) > 0 || len(in.OneOf) > 0 || len(in.AllOf) > 0 || in.Not != nil {
		panic(fmt.Sprintf("schema uses anyOf/oneOf/allOf/not, which ClusterClass variables reject; "+
			"rewrite the field (usually an IntOrString/Quantity) as a plain type: %+v", in))
	}

	out := clusterv1.JSONSchemaProps{
		Description:   in.Description,
		Type:          in.Type,
		Format:        in.Format,
		Required:      in.Required,
		MaxItems:      in.MaxItems,
		MinItems:      in.MinItems,
		MaxLength:     in.MaxLength,
		MinLength:     in.MinLength,
		Pattern:       in.Pattern,
		MaxProperties: in.MaxProperties,
		MinProperties: in.MinProperties,
		Default:       in.Default,
		Example:       in.Example,
	}
	convertScalarConstraints(in, &out)
	if len(in.Enum) > 0 {
		out.Enum = make([]apiextensionsv1.JSON, len(in.Enum))
		copy(out.Enum, in.Enum)
	}
	if len(in.Properties) > 0 {
		out.Properties = make(map[string]clusterv1.JSONSchemaProps, len(in.Properties))
		for name := range in.Properties {
			p := in.Properties[name]
			out.Properties[name] = convert(&p)
		}
	}
	if in.AdditionalProperties != nil && in.AdditionalProperties.Schema != nil {
		converted := convert(in.AdditionalProperties.Schema)
		out.AdditionalProperties = &converted
	}
	if in.Items != nil && in.Items.Schema != nil {
		converted := convert(in.Items.Schema)
		out.Items = &converted
	}
	for _, v := range in.XValidations {
		out.XValidations = append(out.XValidations, clusterv1.ValidationRule{
			Rule:              v.Rule,
			Message:           v.Message,
			MessageExpression: v.MessageExpression,
			FieldPath:         v.FieldPath,
		})
	}
	return out
}

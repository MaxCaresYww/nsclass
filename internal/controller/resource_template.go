/*
Copyright 2026 Akuity.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"encoding/json"
	"fmt"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

type namespaceClassValidationError struct {
	reason  string
	message string
}

func (e *namespaceClassValidationError) Error() string {
	return e.message
}

func newValidationError(reason, format string, args ...any) *namespaceClassValidationError {
	return &namespaceClassValidationError{
		reason:  reason,
		message: fmt.Sprintf(format, args...),
	}
}

func validationReason(err error) string {
	if validationErr, ok := err.(*namespaceClassValidationError); ok {
		return validationErr.reason
	}
	return "ValidationFailed"
}

func resourceTemplateDescription(index int, className string) string {
	if className == "" {
		return fmt.Sprintf("resource template %d", index)
	}
	return fmt.Sprintf("resource template %d in NamespaceClass %q", index, className)
}

func decodeResourceTemplate(resource runtime.RawExtension) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	switch {
	case len(resource.Raw) > 0:
		if err := json.Unmarshal(resource.Raw, &obj.Object); err != nil {
			return nil, err
		}
	case resource.Object != nil:
		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(resource.Object)
		if err != nil {
			return nil, err
		}
		obj.Object = content
	default:
		return nil, fmt.Errorf("template is empty")
	}
	return obj, nil
}

func decodeAndValidateResourceTemplate(mapper apimeta.RESTMapper, index int, resource runtime.RawExtension, className string) (*unstructured.Unstructured, error) {
	desc := resourceTemplateDescription(index, className)

	obj, err := decodeResourceTemplate(resource)
	if err != nil {
		return nil, newValidationError("TemplateInvalid", "%s is invalid: %v", desc, err)
	}

	gvk := obj.GroupVersionKind()
	if gvk.GroupVersion().Empty() {
		return nil, newValidationError("TemplateInvalid", "%s must include apiVersion", desc)
	}
	if gvk.Kind == "" {
		return nil, newValidationError("TemplateInvalid", "%s must include kind", desc)
	}
	if obj.GetName() == "" {
		return nil, newValidationError("TemplateInvalid", "%s must include metadata.name", desc)
	}

	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, newValidationError("UnsupportedResource", "%s uses unsupported kind %s: %v", desc, gvk.String(), err)
	}
	if mapping.Scope.Name() == apimeta.RESTScopeNameRoot {
		return nil, newValidationError("UnsupportedClusterScopedResource", "%s uses cluster-scoped kind %s, which is not supported", desc, gvk.String())
	}

	return obj, nil
}

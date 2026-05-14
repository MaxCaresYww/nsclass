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
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	akuityiov1alpha1 "github.com/maxcaresyww/nsclass/api/v1alpha1"
)

const (
	namespaceClassBindingFinalizer = "namespaceclassbinding.akuity.io/finalizer"

	managedLabel = "namespaceclass.akuity.io/managed"
	classLabel   = "namespaceclass.akuity.io/class"
	trueLabel    = "true"

	fieldManager = "namespaceclass-controller"

	managedResourceSyncPeriod = 5 * time.Minute
)

// NamespaceClassBindingReconciler reconciles a NamespaceClassBinding object
type NamespaceClassBindingReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	RESTMapper apimeta.RESTMapper
}

type desiredResource struct {
	object    *unstructured.Unstructured
	identity  resourceIdentity
	className string
}

type namespaceClassBindingMapping struct {
	namespace  string
	classNames []string
}

type namespaceClassBindingMappingResult struct {
	ready        bool
	reason       string
	message      string
	validClasses map[string]struct{}
}

type desiredResourcesForClassesResult struct {
	resources       []desiredResource
	validClasses    map[string]struct{}
	notReadyReason  string
	notReadyMessage string
}

func (result desiredResourcesForClassesResult) ready() bool {
	return result.notReadyReason == ""
}

type resourceIdentity struct {
	apiVersion string
	kind       string
	namespace  string
	name       string
}

type appliedResource struct {
	identity  resourceIdentity
	className string
	uid       types.UID
}

// +kubebuilder:rbac:groups=akuity.io,resources=namespaceclassbindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=akuity.io,resources=namespaceclassbindings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=akuity.io,resources=namespaceclassbindings/finalizers,verbs=update
// +kubebuilder:rbac:groups=akuity.io,resources=namespaceclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=*,resources=*,verbs=get;list;watch;create;update;patch;delete

func (r *NamespaceClassBindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	binding := &akuityiov1alpha1.NamespaceClassBinding{}
	if err := r.Get(ctx, client.ObjectKey{Name: req.Name}, binding); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !binding.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, binding)
	}

	if !controllerutil.ContainsFinalizer(binding, namespaceClassBindingFinalizer) {
		original := binding.DeepCopy()
		controllerutil.AddFinalizer(binding, namespaceClassBindingFinalizer)
		if err := r.Patch(ctx, binding, client.MergeFrom(original)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	mappings := namespaceClassBindingMappings(binding.Spec.Mappings)

	previousInventory := appliedResourcesFromStatus(binding)
	if len(mappings) == 0 {
		if err := r.deleteAppliedResources(ctx, previousInventory); err != nil {
			return r.failBinding(ctx, binding, "ManagedResourceDeleteFailed", err.Error(), err)
		}
		return ctrl.Result{}, r.setBindingStatus(ctx, binding, metav1.ConditionTrue, "NoMappings", "No namespace mappings are configured", nil)
	}

	allMappingsReady := true
	notReadyReason := ""
	notReadyMessage := ""
	desiredInventory := make(map[resourceIdentity]appliedResource)
	mappingsByNamespace := make(map[string]namespaceClassBindingMapping, len(mappings))
	validClassesByNamespace := make(map[string]map[string]struct{}, len(mappings))
	missingNamespaces := make(map[string]struct{})
	for _, mapping := range mappings {
		mappingsByNamespace[mapping.namespace] = mapping

		mappingResult, err := r.reconcileMapping(ctx, binding, mapping, desiredInventory)
		if err != nil {
			return r.failBinding(ctx, binding, validationReason(err), err.Error(), err)
		}
		if mappingResult.reason == "MissingNamespace" {
			missingNamespaces[mapping.namespace] = struct{}{}
		}
		validClassesByNamespace[mapping.namespace] = mappingResult.validClasses
		if !mappingResult.ready && allMappingsReady {
			allMappingsReady = false
			notReadyReason = mappingResult.reason
			notReadyMessage = mappingResult.message
		}
	}

	nextInventory := nextAppliedResourceInventory(previousInventory, desiredInventory, mappingsByNamespace, validClassesByNamespace, missingNamespaces)
	staleInventory := staleAppliedResourceInventory(previousInventory, nextInventory)
	if err := r.deleteAppliedResources(ctx, staleInventory); err != nil {
		return r.failBinding(ctx, binding, "ManagedResourceDeleteFailed", err.Error(), err)
	}

	if !allMappingsReady {
		if err := r.setBindingStatus(ctx, binding, metav1.ConditionFalse, notReadyReason, notReadyMessage, nextInventory); err != nil {
			return ctrl.Result{}, err
		}
		if len(nextInventory) == 0 {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: managedResourceSyncPeriod}, nil
	}
	if err := r.setBindingStatus(ctx, binding, metav1.ConditionTrue, "ResourcesApplied", "All mapped NamespaceClass resources are reconciled", nextInventory); err != nil {
		return ctrl.Result{}, err
	}
	if len(nextInventory) == 0 {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: managedResourceSyncPeriod}, nil
}

func (r *NamespaceClassBindingReconciler) reconcileDelete(ctx context.Context, binding *akuityiov1alpha1.NamespaceClassBinding) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(binding, namespaceClassBindingFinalizer) {
		return ctrl.Result{}, nil
	}

	if err := r.deleteAppliedResources(ctx, appliedResourcesFromStatus(binding)); err != nil {
		return ctrl.Result{}, err
	}

	controllerutil.RemoveFinalizer(binding, namespaceClassBindingFinalizer)
	if err := r.Update(ctx, binding); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func namespaceClassBindingMappings(values []akuityiov1alpha1.NamespaceClassBindingMapping) []namespaceClassBindingMapping {
	mappings := make([]namespaceClassBindingMapping, 0, len(values))
	for _, value := range values {
		mappings = append(mappings, namespaceClassBindingMapping{
			namespace:  value.Namespace,
			classNames: slices.Clone(value.ClassNames),
		})
	}
	return mappings
}

func (r *NamespaceClassBindingReconciler) reconcileMapping(
	ctx context.Context,
	binding *akuityiov1alpha1.NamespaceClassBinding,
	mapping namespaceClassBindingMapping,
	desiredInventory map[resourceIdentity]appliedResource,
) (namespaceClassBindingMappingResult, error) {
	namespace := &corev1.Namespace{}
	if err := r.Get(ctx, client.ObjectKey{Name: mapping.namespace}, namespace); err != nil {
		if apierrors.IsNotFound(err) {
			message := fmt.Sprintf("Namespace %q does not exist", mapping.namespace)
			return namespaceClassBindingMappingResult{
				ready:   false,
				reason:  "MissingNamespace",
				message: message,
			}, nil
		}
		return namespaceClassBindingMappingResult{}, err
	}

	classResources, err := r.desiredResourcesForClasses(ctx, mapping.namespace, mapping.classNames)
	if err != nil {
		return namespaceClassBindingMappingResult{}, err
	}

	desiredByIdentity := make(map[resourceIdentity]desiredResource, len(classResources.resources))
	for _, desired := range classResources.resources {
		if conflicting, ok := desiredByIdentity[desired.identity]; ok {
			message := fmt.Sprintf("Namespace %q: NamespaceClasses %q and %q both define %s", mapping.namespace, conflicting.className, desired.className, desired.identity)
			return namespaceClassBindingMappingResult{
				ready:   false,
				reason:  "DuplicateDesiredObject",
				message: message,
			}, nil
		}
		desiredByIdentity[desired.identity] = desired
	}

	for _, desired := range classResources.resources {
		uid, err := r.applyObject(ctx, binding, desired)
		if err != nil {
			return namespaceClassBindingMappingResult{}, err
		}
		desiredInventory[desired.identity] = appliedResourceForDesiredResource(desired, uid)
	}

	if !classResources.ready() {
		return namespaceClassBindingMappingResult{
			ready:        false,
			reason:       classResources.notReadyReason,
			message:      fmt.Sprintf("Namespace %q: %s", mapping.namespace, classResources.notReadyMessage),
			validClasses: classResources.validClasses,
		}, nil
	}
	return namespaceClassBindingMappingResult{
		ready:        true,
		validClasses: classResources.validClasses,
	}, nil
}

func (r *NamespaceClassBindingReconciler) desiredResourcesForClasses(ctx context.Context, namespaceName string, classNames []string) (desiredResourcesForClassesResult, error) {
	result := desiredResourcesForClassesResult{
		validClasses: make(map[string]struct{}),
	}

	for _, className := range classNames {
		namespaceClass := &akuityiov1alpha1.NamespaceClass{}
		if err := r.Get(ctx, client.ObjectKey{Name: className}, namespaceClass); err != nil {
			if apierrors.IsNotFound(err) {
				message := fmt.Sprintf("Namespace %q references missing NamespaceClass %q", namespaceName, className)
				if result.ready() {
					result.notReadyReason = "MissingNamespaceClass"
					result.notReadyMessage = message
				}
				continue
			}
			return desiredResourcesForClassesResult{}, err
		}

		if !namespaceClassReady(namespaceClass) {
			message := fmt.Sprintf("Namespace %q references NamespaceClass %q, which is not Ready for generation %d", namespaceName, className, namespaceClass.Generation)
			if result.ready() {
				result.notReadyReason = "NamespaceClassNotReady"
				result.notReadyMessage = message
			}
			continue
		}

		classResources, err := r.desiredResourcesForClass(namespaceName, namespaceClass)
		if err != nil {
			return desiredResourcesForClassesResult{}, err
		}
		result.validClasses[className] = struct{}{}
		result.resources = append(result.resources, classResources...)
	}

	return result, nil
}

func namespaceClassReady(namespaceClass *akuityiov1alpha1.NamespaceClass) bool {
	if namespaceClass.Status.ObservedGeneration != namespaceClass.Generation {
		return false
	}
	ready := apimeta.FindStatusCondition(namespaceClass.Status.Conditions, akuityiov1alpha1.NamespaceClassReadyCondition)
	return ready != nil && ready.Status == metav1.ConditionTrue
}

func (r *NamespaceClassBindingReconciler) desiredResourcesForClass(namespaceName string, namespaceClass *akuityiov1alpha1.NamespaceClass) ([]desiredResource, error) {
	resources := make([]desiredResource, 0, len(namespaceClass.Spec.Resources))
	for i, resource := range namespaceClass.Spec.Resources {
		obj, err := r.prepareDesiredObject(namespaceName, namespaceClass.Name, i, resource)
		if err != nil {
			return nil, err
		}
		resources = append(resources, desiredResource{
			object:    obj,
			identity:  identityForObject(obj),
			className: namespaceClass.Name,
		})
	}
	return resources, nil
}

func (r *NamespaceClassBindingReconciler) prepareDesiredObject(namespaceName, className string, index int, resource runtime.RawExtension) (*unstructured.Unstructured, error) {
	obj, err := decodeResourceTemplate(resource)
	if err != nil {
		return nil, newValidationError("TemplateInvalid", "resource template %d in NamespaceClass %q is invalid: %v", index, className, err)
	}

	gvk := obj.GroupVersionKind()
	if gvk.GroupVersion().Empty() {
		return nil, newValidationError("TemplateInvalid", "resource template %d in NamespaceClass %q must include apiVersion", index, className)
	}
	if gvk.Kind == "" {
		return nil, newValidationError("TemplateInvalid", "resource template %d in NamespaceClass %q must include kind", index, className)
	}
	if obj.GetName() == "" {
		return nil, newValidationError("TemplateInvalid", "resource template %d in NamespaceClass %q must include metadata.name", index, className)
	}

	mapping, err := r.RESTMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, newValidationError("UnsupportedResource", "resource template %d in NamespaceClass %q uses unsupported kind %s: %v", index, className, gvk.String(), err)
	}
	if mapping.Scope.Name() == apimeta.RESTScopeNameRoot {
		return nil, newValidationError("UnsupportedClusterScopedResource", "resource template %d in NamespaceClass %q uses cluster-scoped kind %s, which is not supported", index, className, gvk.String())
	}

	obj.SetNamespace(namespaceName)
	obj.SetResourceVersion("")
	obj.SetUID("")
	obj.SetManagedFields(nil)
	obj.SetCreationTimestamp(metav1.Time{})

	labels := obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[managedLabel] = trueLabel
	labels[classLabel] = className
	obj.SetLabels(labels)

	return obj, nil
}

func (r *NamespaceClassBindingReconciler) applyObject(ctx context.Context, binding *akuityiov1alpha1.NamespaceClassBinding, desired desiredResource) (types.UID, error) {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.object.GroupVersionKind())
	var uid types.UID
	if err := r.Get(ctx, client.ObjectKeyFromObject(desired.object), existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return "", newValidationError("ManagedResourceApplyFailed", "Could not get %s: %v", desired.identity, err)
		}
	} else if err := ensureManagedByBinding(existing, binding, desired.className); err != nil {
		return "", newValidationError("ManagedResourceConflict", "%s", err.Error())
	} else {
		uid = existing.GetUID()
	}

	applyConfig := client.ApplyConfigurationFromUnstructured(desired.object)
	if err := r.Apply(ctx, applyConfig, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
		return "", newValidationError("ManagedResourceApplyFailed", "Could not apply %s: %v", desired.identity, err)
	}
	if uid != "" {
		return uid, nil
	}

	applied := &unstructured.Unstructured{}
	applied.SetGroupVersionKind(desired.object.GroupVersionKind())
	if err := r.Get(ctx, client.ObjectKeyFromObject(desired.object), applied); err != nil {
		return "", newValidationError("ManagedResourceApplyFailed", "Could not get applied %s: %v", desired.identity, err)
	}
	return applied.GetUID(), nil
}

func ensureManagedByBinding(object *unstructured.Unstructured, binding *akuityiov1alpha1.NamespaceClassBinding, className string) error {
	labels := object.GetLabels()
	if labels[managedLabel] != trueLabel {
		return fmt.Errorf("resource %s is not managed by NamespaceClassBinding %s", identityForObject(object), binding.Name)
	}
	if labels[classLabel] != className {
		return fmt.Errorf("resource %s is already managed for NamespaceClass %q", identityForObject(object), labels[classLabel])
	}
	return nil
}

func appliedResourceForDesiredResource(desired desiredResource, uid types.UID) appliedResource {
	return appliedResource{
		identity:  desired.identity,
		className: desired.className,
		uid:       uid,
	}
}

func (r *NamespaceClassBindingReconciler) deleteAppliedResources(ctx context.Context, resources map[resourceIdentity]appliedResource) error {
	for _, resource := range sortedAppliedResources(resources) {
		if err := r.deleteAppliedResource(ctx, resource); err != nil {
			return err
		}
	}
	return nil
}

func (r *NamespaceClassBindingReconciler) deleteAppliedResource(ctx context.Context, resource appliedResource) error {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(resource.identity.apiVersion)
	obj.SetKind(resource.identity.kind)
	obj.SetNamespace(resource.identity.namespace)
	obj.SetName(resource.identity.name)
	if err := r.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return nil
		}
		return newValidationError("ManagedResourceDeleteFailed", "Could not get %s: %v", resource.identity, err)
	}
	if err := ensureAppliedResourceManaged(obj, resource); err != nil {
		return newValidationError("ManagedResourceConflict", "%s", err.Error())
	}

	deleteOptions := []client.DeleteOption{}
	if resource.uid != "" {
		uid := resource.uid
		deleteOptions = append(deleteOptions, client.Preconditions{UID: &uid})
	}
	if err := r.Delete(ctx, obj, deleteOptions...); err != nil && !apierrors.IsNotFound(err) {
		return newValidationError("ManagedResourceDeleteFailed", "Could not delete %s: %v", resource.identity, err)
	}
	return nil
}

func ensureAppliedResourceManaged(object *unstructured.Unstructured, resource appliedResource) error {
	labels := object.GetLabels()
	if labels[managedLabel] != trueLabel {
		return fmt.Errorf("resource %s is not managed by NamespaceClassBinding", identityForObject(object))
	}
	if labels[classLabel] != resource.className {
		return fmt.Errorf("resource %s is managed for NamespaceClass %q, not %q", identityForObject(object), labels[classLabel], resource.className)
	}
	return nil
}

func identityForObject(object *unstructured.Unstructured) resourceIdentity {
	return resourceIdentity{
		apiVersion: object.GetAPIVersion(),
		kind:       object.GetKind(),
		namespace:  object.GetNamespace(),
		name:       object.GetName(),
	}
}

func (i resourceIdentity) String() string {
	return fmt.Sprintf("%s/%s %s/%s", i.apiVersion, i.kind, i.namespace, i.name)
}

func appliedResourcesFromStatus(binding *akuityiov1alpha1.NamespaceClassBinding) map[resourceIdentity]appliedResource {
	resources := make(map[resourceIdentity]appliedResource)
	for _, namespaceStatus := range binding.Status.Namespaces {
		for _, resourceStatus := range namespaceStatus.Resources {
			identity := resourceIdentity{
				apiVersion: resourceStatus.APIVersion,
				kind:       resourceStatus.Kind,
				namespace:  namespaceStatus.Namespace,
				name:       resourceStatus.Name,
			}
			resources[identity] = appliedResource{
				identity:  identity,
				className: resourceStatus.ClassName,
				uid:       resourceStatus.UID,
			}
		}
	}
	return resources
}

func nextAppliedResourceInventory(
	previousInventory map[resourceIdentity]appliedResource,
	desiredInventory map[resourceIdentity]appliedResource,
	mappingsByNamespace map[string]namespaceClassBindingMapping,
	validClassesByNamespace map[string]map[string]struct{},
	missingNamespaces map[string]struct{},
) map[resourceIdentity]appliedResource {
	nextInventory := make(map[resourceIdentity]appliedResource, len(desiredInventory))
	maps.Copy(nextInventory, desiredInventory)
	for identity, resource := range previousInventory {
		if _, ok := nextInventory[identity]; ok {
			continue
		}
		if _, ok := missingNamespaces[identity.namespace]; ok {
			continue
		}
		mapping, ok := mappingsByNamespace[identity.namespace]
		if !ok {
			continue
		}
		if !slices.Contains(mapping.classNames, resource.className) {
			continue
		}
		if _, ok := validClassesByNamespace[identity.namespace][resource.className]; ok {
			continue
		}
		nextInventory[identity] = resource
	}
	return nextInventory
}

func staleAppliedResourceInventory(previousInventory, nextInventory map[resourceIdentity]appliedResource) map[resourceIdentity]appliedResource {
	staleInventory := make(map[resourceIdentity]appliedResource)
	for identity, resource := range previousInventory {
		if _, ok := nextInventory[identity]; ok {
			continue
		}
		staleInventory[identity] = resource
	}
	return staleInventory
}

func appliedResourcesToNamespaceStatus(resources map[resourceIdentity]appliedResource) []akuityiov1alpha1.NamespaceClassBindingNamespaceStatus {
	if len(resources) == 0 {
		return nil
	}

	resourcesByNamespace := make(map[string][]akuityiov1alpha1.NamespaceClassBindingAppliedResource)
	for _, resource := range sortedAppliedResources(resources) {
		resourcesByNamespace[resource.identity.namespace] = append(resourcesByNamespace[resource.identity.namespace], akuityiov1alpha1.NamespaceClassBindingAppliedResource{
			APIVersion: resource.identity.apiVersion,
			Kind:       resource.identity.kind,
			Name:       resource.identity.name,
			ClassName:  resource.className,
			UID:        resource.uid,
		})
	}

	namespaces := make([]string, 0, len(resourcesByNamespace))
	for namespace := range resourcesByNamespace {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)

	status := make([]akuityiov1alpha1.NamespaceClassBindingNamespaceStatus, 0, len(namespaces))
	for _, namespace := range namespaces {
		status = append(status, akuityiov1alpha1.NamespaceClassBindingNamespaceStatus{
			Namespace: namespace,
			Resources: resourcesByNamespace[namespace],
		})
	}
	return status
}

func sortedAppliedResources(resources map[resourceIdentity]appliedResource) []appliedResource {
	sortedResources := make([]appliedResource, 0, len(resources))
	for _, resource := range resources {
		sortedResources = append(sortedResources, resource)
	}
	sort.Slice(sortedResources, func(i, j int) bool {
		left := sortedResources[i].identity
		right := sortedResources[j].identity
		return strings.Compare(fmt.Sprintf("%s/%s/%s/%s", left.namespace, left.apiVersion, left.kind, left.name), fmt.Sprintf("%s/%s/%s/%s", right.namespace, right.apiVersion, right.kind, right.name)) < 0
	})
	return sortedResources
}

func (r *NamespaceClassBindingReconciler) failBinding(ctx context.Context, binding *akuityiov1alpha1.NamespaceClassBinding, reason, message string, err error) (ctrl.Result, error) {
	if statusErr := r.setBindingStatus(ctx, binding, metav1.ConditionFalse, reason, message, appliedResourcesFromStatus(binding)); statusErr != nil && err == nil {
		return ctrl.Result{}, statusErr
	}
	return ctrl.Result{}, err
}

func (r *NamespaceClassBindingReconciler) setBindingStatus(ctx context.Context, binding *akuityiov1alpha1.NamespaceClassBinding, status metav1.ConditionStatus, reason, message string, resources map[resourceIdentity]appliedResource) error {
	original := binding.DeepCopy()
	binding.Status.ObservedGeneration = binding.Generation
	binding.Status.Namespaces = appliedResourcesToNamespaceStatus(resources)
	apimeta.SetStatusCondition(&binding.Status.Conditions, metav1.Condition{
		Type:               akuityiov1alpha1.NamespaceClassBindingReadyCondition,
		Status:             status,
		ObservedGeneration: binding.Generation,
		Reason:             reason,
		Message:            message,
	})
	if equality.Semantic.DeepEqual(&original.Status, &binding.Status) {
		return nil
	}
	if err := r.Status().Patch(ctx, binding, client.MergeFrom(original)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *NamespaceClassBindingReconciler) mapNamespaceClassToBindings(ctx context.Context, obj client.Object) []reconcile.Request {
	binding := &akuityiov1alpha1.NamespaceClassBinding{}
	if err := r.Get(ctx, client.ObjectKey{Name: akuityiov1alpha1.NamespaceClassBindingDefaultName}, binding); err != nil {
		if !apierrors.IsNotFound(err) {
			ctrl.LoggerFrom(ctx).Error(err, "Could not get NamespaceClassBinding for NamespaceClass watch")
		}
		return nil
	}

	for _, mapping := range namespaceClassBindingMappings(binding.Spec.Mappings) {
		if slices.Contains(mapping.classNames, obj.GetName()) {
			return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: binding.Name}}}
		}
	}
	return nil
}

func (r *NamespaceClassBindingReconciler) mapNamespaceToBinding(ctx context.Context, obj client.Object) []reconcile.Request {
	binding := &akuityiov1alpha1.NamespaceClassBinding{}
	if err := r.Get(ctx, client.ObjectKey{Name: akuityiov1alpha1.NamespaceClassBindingDefaultName}, binding); err != nil {
		if !apierrors.IsNotFound(err) {
			ctrl.LoggerFrom(ctx).Error(err, "Could not get NamespaceClassBinding for Namespace watch")
		}
		return nil
	}
	for _, mapping := range binding.Spec.Mappings {
		if mapping.Namespace == obj.GetName() {
			return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: binding.Name}}}
		}
	}
	return nil
}

type namespaceCreateOnlyPredicate struct{}

func (namespaceCreateOnlyPredicate) Create(event.CreateEvent) bool {
	return true
}

func (namespaceCreateOnlyPredicate) Update(event.UpdateEvent) bool {
	return false
}

func (namespaceCreateOnlyPredicate) Delete(event.DeleteEvent) bool {
	return false
}

func (namespaceCreateOnlyPredicate) Generic(event.GenericEvent) bool {
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespaceClassBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.RESTMapper == nil {
		r.RESTMapper = mgr.GetRESTMapper()
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&akuityiov1alpha1.NamespaceClassBinding{}).
		Watches(&akuityiov1alpha1.NamespaceClass{}, handler.EnqueueRequestsFromMapFunc(r.mapNamespaceClassToBindings)).
		Watches(
			&corev1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(r.mapNamespaceToBinding),
			builder.WithPredicates(namespaceCreateOnlyPredicate{}),
		).
		Named("namespaceclassbinding").
		Complete(r)
}

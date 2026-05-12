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
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	akuityiov1alpha1 "github.com/maxcaresyww/nsclass/api/v1alpha1"
)

const (
	namespaceClassNamesLabel = "namespaceclass.akuity.io/name"

	managedLabel   = "namespaceclass.akuity.io/managed"
	classLabel     = "namespaceclass.akuity.io/class"
	namespaceLabel = "namespaceclass.akuity.io/namespace"

	templateIDAnnotation = "namespaceclass.akuity.io/template-id"

	fieldManager = "namespaceclass-controller"

	managedResourceSyncPeriod = 5 * time.Minute
)

// NamespaceReconciler reconciles resources derived from NamespaceClass membership labels.
type NamespaceReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	RESTMapper apimeta.RESTMapper
	Discovery  discovery.DiscoveryInterface
	Recorder   events.EventRecorder
}

type desiredResource struct {
	object    *unstructured.Unstructured
	identity  resourceIdentity
	className string
}

type resourceIdentity struct {
	apiVersion string
	kind       string
	namespace  string
	name       string
}

// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=akuity.io,resources=namespaceclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=*,resources=*,verbs=get;list;watch;create;update;patch;delete

func (r *NamespaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	namespace := &corev1.Namespace{}
	if err := r.Get(ctx, req.NamespacedName, namespace); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !namespace.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	classNames, err := parseNamespaceClassNames(namespace.Labels[namespaceClassNamesLabel])
	if err != nil {
		r.recordWarning(namespace, "InvalidNamespaceClassMembership", err.Error())
		return ctrl.Result{}, nil
	}

	existingObjects, err := r.listManagedObjectsForNamespace(ctx, namespace.Name)
	if err != nil {
		r.recordWarning(namespace, "ManagedResourceListFailed", err.Error())
		return ctrl.Result{}, err
	}

	if len(classNames) == 0 {
		return ctrl.Result{}, r.deleteObjects(ctx, namespace, existingObjects)
	}

	if err := r.deleteObjectsNotInClassList(ctx, namespace, existingObjects, classNames); err != nil {
		return ctrl.Result{}, err
	}

	desiredResources, validClasses, err := r.desiredResourcesForClasses(ctx, namespace, classNames)
	if err != nil {
		return ctrl.Result{}, err
	}

	desiredByIdentity := make(map[resourceIdentity]desiredResource, len(desiredResources))
	for _, desired := range desiredResources {
		if conflicting, ok := desiredByIdentity[desired.identity]; ok {
			message := fmt.Sprintf("NamespaceClasses %q and %q both define %s", conflicting.className, desired.className, desired.identity)
			r.recordWarning(namespace, "DuplicateDesiredObject", message)
			return ctrl.Result{}, nil
		}
		desiredByIdentity[desired.identity] = desired
	}

	for _, desired := range desiredResources {
		if err := r.applyObject(ctx, namespace, desired); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.deleteStaleObjects(ctx, namespace, existingObjects, desiredByIdentity, validClasses); err != nil {
		return ctrl.Result{}, err
	}
	if len(desiredResources) == 0 {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: managedResourceSyncPeriod}, nil
}

func parseNamespaceClassNames(value string) ([]string, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		return nil, nil
	}
	if strings.ContainsAny(name, ",; ") {
		return nil, fmt.Errorf("%s must reference exactly one NamespaceClass", namespaceClassNamesLabel)
	}
	return []string{name}, nil
}

func (r *NamespaceReconciler) listManagedObjectsForNamespace(ctx context.Context, namespaceName string) ([]unstructured.Unstructured, error) {
	gvks, err := r.namespacedResourceGVKs()
	if err != nil {
		return nil, err
	}

	var objects []unstructured.Unstructured
	for _, gvk := range gvks {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   gvk.Group,
			Version: gvk.Version,
			Kind:    gvk.Kind + "List",
		})

		if err := r.List(ctx, list, client.InNamespace(namespaceName), client.MatchingLabels{
			managedLabel:   "true",
			namespaceLabel: namespaceName,
		}); err != nil {
			if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
				continue
			}
			return nil, fmt.Errorf("list managed resources for %s %s in namespace %q: %w", gvk.GroupVersion().String(), gvk.Kind, namespaceName, err)
		}
		for _, item := range list.Items {
			item.SetGroupVersionKind(gvk)
			objects = append(objects, item)
		}
	}
	return objects, nil
}

func (r *NamespaceReconciler) namespacedResourceGVKs() ([]schema.GroupVersionKind, error) {
	if r.Discovery == nil {
		return r.templateGVKsFromNamespaceClasses(context.Background())
	}

	resourceLists, err := r.Discovery.ServerPreferredResources()
	if err != nil && !discovery.IsGroupDiscoveryFailedError(err) {
		return nil, err
	}

	gvks := make([]schema.GroupVersionKind, 0)
	seen := make(map[schema.GroupVersionKind]struct{})
	for _, resourceList := range resourceLists {
		groupVersion, err := schema.ParseGroupVersion(resourceList.GroupVersion)
		if err != nil {
			continue
		}
		for _, apiResource := range resourceList.APIResources {
			if !shouldListManagedResource(groupVersion, apiResource) {
				continue
			}
			gvk := groupVersion.WithKind(apiResource.Kind)
			if _, ok := seen[gvk]; ok {
				continue
			}
			seen[gvk] = struct{}{}
			gvks = append(gvks, gvk)
		}
	}
	return gvks, nil
}

func shouldListManagedResource(groupVersion schema.GroupVersion, apiResource metav1.APIResource) bool {
	if !apiResource.Namespaced || strings.Contains(apiResource.Name, "/") || !supportsVerb(apiResource.Verbs, "list") {
		return false
	}
	return !isCoreV1Endpoints(groupVersion, apiResource)
}

func isCoreV1Endpoints(groupVersion schema.GroupVersion, apiResource metav1.APIResource) bool {
	return groupVersion.Group == "" && groupVersion.Version == "v1" && apiResource.Name == "endpoints"
}

func supportsVerb(verbs []string, verb string) bool {
	return slices.Contains(verbs, verb)
}

func (r *NamespaceReconciler) templateGVKsFromNamespaceClasses(ctx context.Context) ([]schema.GroupVersionKind, error) {
	namespaceClasses := &akuityiov1alpha1.NamespaceClassList{}
	if err := r.List(ctx, namespaceClasses); err != nil {
		return nil, err
	}

	gvks := make([]schema.GroupVersionKind, 0)
	seen := make(map[schema.GroupVersionKind]struct{})
	for _, namespaceClass := range namespaceClasses.Items {
		for _, resource := range namespaceClass.Spec.Resources {
			obj, err := decodeResourceTemplate(resource)
			if err != nil {
				continue
			}
			gvk := obj.GroupVersionKind()
			if gvk.Empty() {
				continue
			}
			if _, ok := seen[gvk]; ok {
				continue
			}
			seen[gvk] = struct{}{}
			gvks = append(gvks, gvk)
		}
	}
	return gvks, nil
}

func (r *NamespaceReconciler) deleteObjectsNotInClassList(ctx context.Context, namespace *corev1.Namespace, objects []unstructured.Unstructured, classNames []string) error {
	classSet := namesSet(classNames)
	var toDelete []unstructured.Unstructured
	for _, object := range objects {
		if _, ok := classSet[object.GetLabels()[classLabel]]; !ok {
			toDelete = append(toDelete, object)
		}
	}
	return r.deleteObjects(ctx, namespace, toDelete)
}

func (r *NamespaceReconciler) desiredResourcesForClasses(ctx context.Context, namespace *corev1.Namespace, classNames []string) ([]desiredResource, map[string]struct{}, error) {
	var desiredResources []desiredResource
	validClasses := make(map[string]struct{})

	for _, className := range classNames {
		namespaceClass := &akuityiov1alpha1.NamespaceClass{}
		if err := r.Get(ctx, client.ObjectKey{Name: className}, namespaceClass); err != nil {
			if apierrors.IsNotFound(err) {
				r.recordWarning(namespace, "MissingNamespaceClass", fmt.Sprintf("NamespaceClass %q does not exist", className))
				continue
			}
			return nil, nil, err
		}

		if !namespaceClassReady(namespaceClass) {
			r.recordWarning(namespace, "NamespaceClassNotReady", fmt.Sprintf("NamespaceClass %q is not Ready for generation %d", className, namespaceClass.Generation))
			continue
		}

		classResources, err := r.desiredResourcesForClass(namespace.Name, namespaceClass)
		if err != nil {
			r.recordWarning(namespace, validationReason(err), err.Error())
			return nil, nil, err
		}
		validClasses[className] = struct{}{}
		desiredResources = append(desiredResources, classResources...)
	}

	return desiredResources, validClasses, nil
}

func namespaceClassReady(namespaceClass *akuityiov1alpha1.NamespaceClass) bool {
	if namespaceClass.Status.ObservedGeneration != namespaceClass.Generation {
		return false
	}
	ready := apimeta.FindStatusCondition(namespaceClass.Status.Conditions, akuityiov1alpha1.NamespaceClassReadyCondition)
	return ready != nil && ready.Status == metav1.ConditionTrue
}

func (r *NamespaceReconciler) desiredResourcesForClass(namespaceName string, namespaceClass *akuityiov1alpha1.NamespaceClass) ([]desiredResource, error) {
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

func (r *NamespaceReconciler) prepareDesiredObject(namespaceName, className string, index int, resource runtime.RawExtension) (*unstructured.Unstructured, error) {
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
	labels[managedLabel] = "true"
	labels[classLabel] = className
	labels[namespaceLabel] = namespaceName
	obj.SetLabels(labels)

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[templateIDAnnotation] = templateIDForObject(obj)
	obj.SetAnnotations(annotations)

	return obj, nil
}

func (r *NamespaceReconciler) applyObject(ctx context.Context, namespace *corev1.Namespace, desired desiredResource) error {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.object.GroupVersionKind())
	if err := r.Get(ctx, client.ObjectKeyFromObject(desired.object), existing); err != nil {
		if !apierrors.IsNotFound(err) {
			r.recordWarning(namespace, "ManagedResourceApplyFailed", fmt.Sprintf("Could not get %s: %v", desired.identity, err))
			return err
		}
	} else if err := ensureManagedByClass(existing, namespace.Name, desired.className); err != nil {
		r.recordWarning(namespace, "ManagedResourceConflict", err.Error())
		return err
	}

	applyConfig := client.ApplyConfigurationFromUnstructured(desired.object)
	if err := r.Apply(ctx, applyConfig, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
		r.recordWarning(namespace, "ManagedResourceApplyFailed", fmt.Sprintf("Could not apply %s: %v", desired.identity, err))
		return err
	}
	return nil
}

func ensureManagedByClass(object *unstructured.Unstructured, namespaceName, className string) error {
	labels := object.GetLabels()
	if labels[managedLabel] != "true" || labels[namespaceLabel] != namespaceName {
		return fmt.Errorf("resource %s is not managed by NamespaceClass controller", identityForObject(object))
	}
	if labels[classLabel] != className {
		return fmt.Errorf("resource %s is already managed for NamespaceClass %q", identityForObject(object), labels[classLabel])
	}
	return nil
}

func (r *NamespaceReconciler) deleteStaleObjects(
	ctx context.Context,
	namespace *corev1.Namespace,
	objects []unstructured.Unstructured,
	desiredByIdentity map[resourceIdentity]desiredResource,
	validClasses map[string]struct{},
) error {
	var toDelete []unstructured.Unstructured
	for _, object := range objects {
		className := object.GetLabels()[classLabel]
		if _, ok := validClasses[className]; !ok {
			continue
		}
		if _, ok := desiredByIdentity[identityForObject(&object)]; ok {
			continue
		}
		toDelete = append(toDelete, object)
	}
	return r.deleteObjects(ctx, namespace, toDelete)
}

func (r *NamespaceReconciler) deleteObjects(ctx context.Context, namespace *corev1.Namespace, objects []unstructured.Unstructured) error {
	for _, object := range objects {
		obj := object.DeepCopy()
		if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			r.recordWarning(namespace, "ManagedResourceDeleteFailed", fmt.Sprintf("Could not delete %s: %v", identityForObject(obj), err))
			return err
		}
	}
	return nil
}

func namesSet(names []string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}

func identityForObject(object *unstructured.Unstructured) resourceIdentity {
	return resourceIdentity{
		apiVersion: object.GetAPIVersion(),
		kind:       object.GetKind(),
		namespace:  object.GetNamespace(),
		name:       object.GetName(),
	}
}

func templateIDForObject(object *unstructured.Unstructured) string {
	return fmt.Sprintf("%s/%s/%s", object.GetAPIVersion(), object.GetKind(), object.GetName())
}

func (i resourceIdentity) String() string {
	return fmt.Sprintf("%s/%s %s/%s", i.apiVersion, i.kind, i.namespace, i.name)
}

func (r *NamespaceReconciler) recordWarning(namespace *corev1.Namespace, reason, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(namespace, nil, corev1.EventTypeWarning, reason, reason, message)
}

func (r *NamespaceReconciler) mapNamespaceClassToNamespaces(ctx context.Context, obj client.Object) []reconcile.Request {
	namespaceList := &corev1.NamespaceList{}
	if err := r.List(ctx, namespaceList); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Could not list Namespaces for NamespaceClass watch")
		return nil
	}

	requests := make([]reconcile.Request, 0)
	for _, namespace := range namespaceList.Items {
		classNames, err := parseNamespaceClassNames(namespace.Labels[namespaceClassNamesLabel])
		if err != nil {
			continue
		}
		if slices.Contains(classNames, obj.GetName()) {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKey{Name: namespace.Name}})
		}
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.RESTMapper == nil {
		r.RESTMapper = mgr.GetRESTMapper()
	}
	if r.Discovery == nil {
		discoveryClient, err := discovery.NewDiscoveryClientForConfig(mgr.GetConfig())
		if err != nil {
			return err
		}
		r.Discovery = discoveryClient
	}
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorder("namespace-controller")
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}).
		Watches(&akuityiov1alpha1.NamespaceClass{}, handler.EnqueueRequestsFromMapFunc(r.mapNamespaceClassToNamespaces)).
		Named("namespace").
		Complete(r)
}

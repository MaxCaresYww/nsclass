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
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	akuityiov1alpha1 "github.com/maxcaresyww/nsclass/api/v1alpha1"
)

const (
	namespaceClassFinalizer = "namespaceclass.akuity.io/finalizer"
	deletionBlockedRequeue  = 30 * time.Second
)

// NamespaceClassReconciler reconciles a NamespaceClass object
type NamespaceClassReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	RESTMapper apimeta.RESTMapper
}

// +kubebuilder:rbac:groups=akuity.io,resources=namespaceclasses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=akuity.io,resources=namespaceclasses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=akuity.io,resources=namespaceclasses/finalizers,verbs=update
// +kubebuilder:rbac:groups=akuity.io,resources=namespaceclassbindings,verbs=get

func (r *NamespaceClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	namespaceClass := &akuityiov1alpha1.NamespaceClass{}
	if err := r.Get(ctx, req.NamespacedName, namespaceClass); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !namespaceClass.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, namespaceClass)
	}

	if !controllerutil.ContainsFinalizer(namespaceClass, namespaceClassFinalizer) {
		original := namespaceClass.DeepCopy()
		controllerutil.AddFinalizer(namespaceClass, namespaceClassFinalizer)
		if err := r.Patch(ctx, namespaceClass, client.MergeFrom(original)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	originalStatus := namespaceClass.Status.DeepCopy()
	namespaceClass.Status.ObservedGeneration = namespaceClass.Generation

	condition := metav1.Condition{
		Type:               akuityiov1alpha1.NamespaceClassReadyCondition,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: namespaceClass.Generation,
		Reason:             "TemplatesValid",
		Message:            "All resource templates are valid",
	}
	if err := r.validateNamespaceClass(namespaceClass); err != nil {
		condition.Status = metav1.ConditionFalse
		condition.Reason = validationReason(err)
		condition.Message = err.Error()
	}
	apimeta.SetStatusCondition(&namespaceClass.Status.Conditions, condition)

	if equality.Semantic.DeepEqual(originalStatus, &namespaceClass.Status) {
		return ctrl.Result{}, nil
	}
	if err := r.Status().Update(ctx, namespaceClass); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *NamespaceClassReconciler) reconcileDelete(ctx context.Context, namespaceClass *akuityiov1alpha1.NamespaceClass) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(namespaceClass, namespaceClassFinalizer) {
		return ctrl.Result{}, nil
	}

	bindingNames, err := r.bindingNamesForClass(ctx, namespaceClass.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(bindingNames) > 0 {
		return ctrl.Result{RequeueAfter: deletionBlockedRequeue}, r.setDeleteBlockedStatus(ctx, namespaceClass, bindingNames)
	}

	controllerutil.RemoveFinalizer(namespaceClass, namespaceClassFinalizer)
	if err := r.Update(ctx, namespaceClass); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *NamespaceClassReconciler) bindingNamesForClass(ctx context.Context, className string) ([]string, error) {
	binding := &akuityiov1alpha1.NamespaceClassBinding{}
	if err := r.Get(ctx, client.ObjectKey{Name: akuityiov1alpha1.NamespaceClassBindingDefaultName}, binding); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	mappings := namespaceClassBindingMappings(binding.Spec.Mappings)
	bindingNames := make([]string, 0, len(mappings))
	for _, mapping := range mappings {
		if slices.Contains(mapping.classNames, className) {
			bindingNames = append(bindingNames, mapping.namespace)
		}
	}
	sort.Strings(bindingNames)
	return bindingNames, nil
}

func (r *NamespaceClassReconciler) setDeleteBlockedStatus(ctx context.Context, namespaceClass *akuityiov1alpha1.NamespaceClass, bindingNames []string) error {
	originalStatus := namespaceClass.Status.DeepCopy()
	namespaceClass.Status.ObservedGeneration = namespaceClass.Generation
	apimeta.SetStatusCondition(&namespaceClass.Status.Conditions, metav1.Condition{
		Type:               akuityiov1alpha1.NamespaceClassReadyCondition,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: namespaceClass.Generation,
		Reason:             "NamespaceClassBindingsStillUseClass",
		Message: fmt.Sprintf("Namespaces %s in NamespaceClassBindings %s still reference this NamespaceClass",
			strings.Join(bindingNames, ", "), akuityiov1alpha1.NamespaceClassBindingDefaultName),
	})
	if equality.Semantic.DeepEqual(originalStatus, &namespaceClass.Status) {
		return nil
	}
	if err := r.Status().Update(ctx, namespaceClass); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *NamespaceClassReconciler) validateNamespaceClass(namespaceClass *akuityiov1alpha1.NamespaceClass) error {
	for i, resource := range namespaceClass.Spec.Resources {
		if err := r.validateResourceTemplate(i, resource); err != nil {
			return err
		}
	}
	return nil
}

func (r *NamespaceClassReconciler) validateResourceTemplate(index int, resource runtime.RawExtension) error {
	_, err := decodeAndValidateResourceTemplate(r.RESTMapper, index, resource, "")
	return err
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespaceClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.RESTMapper == nil {
		r.RESTMapper = mgr.GetRESTMapper()
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&akuityiov1alpha1.NamespaceClass{}).
		Named("namespaceclass").
		Complete(r)
}

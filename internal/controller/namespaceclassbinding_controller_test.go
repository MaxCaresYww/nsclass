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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	akuityiov1alpha1 "github.com/maxcaresyww/nsclass/api/v1alpha1"
)

var _ = Describe("NamespaceClassBinding Controller", func() {
	ctx := context.Background()

	It("adds the finalizer before reconciling resources", func() {
		namespace := createTestNamespace(ctx, "nscb-finalizer-first")
		createNamespaceClass(ctx, "finalizer-binding-class", configMapTemplate("finalizer-binding-config", "state", "managed"))
		binding := createNamespaceClassBinding(ctx, namespace.Name, "finalizer-binding-class")

		result, err := reconcileNamespaceClassBindingResult(ctx, binding.Namespace, binding.Name)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))
		reconciled := &akuityiov1alpha1.NamespaceClassBinding{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Name}, reconciled)).To(Succeed())
		Expect(reconciled.Finalizers).To(ContainElement(namespaceClassBindingFinalizer))
		Expect(reconciled.Status.ObservedGeneration).To(BeZero())
		Expect(reconciled.Status.Conditions).To(BeEmpty())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "finalizer-binding-config"}, &corev1.ConfigMap{})).To(MatchError(errors.IsNotFound, "IsNotFound"))
	})

	It("applies resources from Ready NamespaceClasses", func() {
		namespace := createTestNamespace(ctx, "nscb-apply")
		namespaceClass := createNamespaceClass(ctx, "binding-public-network", configMapTemplate("namespaceclass-sample", "example", "managed"))
		reconcileNamespaceClass(ctx, namespaceClass.Name)
		binding := createNamespaceClassBinding(ctx, namespace.Name, namespaceClass.Name)

		result := reconcileNamespaceClassBindingController(ctx, binding.Namespace, binding.Name)

		Expect(result).To(Equal(reconcile.Result{RequeueAfter: managedResourceSyncPeriod}))
		configMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "namespaceclass-sample"}, configMap)).To(Succeed())
		Expect(configMap.Data).To(HaveKeyWithValue("example", "managed"))
		Expect(configMap.Labels).To(HaveKeyWithValue(managedLabel, "true"))
		Expect(configMap.Labels).To(HaveKeyWithValue(classLabel, namespaceClass.Name))

		reconciled := &akuityiov1alpha1.NamespaceClassBinding{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(binding), reconciled)).To(Succeed())
		ready := apimeta.FindStatusCondition(reconciled.Status.Conditions, akuityiov1alpha1.NamespaceClassBindingReadyCondition)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))
		Expect(ready.Reason).To(Equal("ResourcesApplied"))
		Expect(reconciled.Status.Namespaces).To(Equal([]akuityiov1alpha1.NamespaceClassBindingNamespaceStatus{{
			Namespace: namespace.Name,
			Resources: []akuityiov1alpha1.NamespaceClassBindingAppliedResource{{
				APIVersion: "v1",
				Kind:       "ConfigMap",
				Name:       "namespaceclass-sample",
				ClassName:  namespaceClass.Name,
				UID:        configMap.UID,
			}},
		}}))
	})

	It("patches status when the cached binding resourceVersion is stale", func() {
		namespace := createTestNamespace(ctx, "nscb-stale-status")
		binding := createNamespaceClassBinding(ctx, namespace.Name, "stale-status-class")
		stale := &akuityiov1alpha1.NamespaceClassBinding{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(binding), stale)).To(Succeed())
		latest := &akuityiov1alpha1.NamespaceClassBinding{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(binding), latest)).To(Succeed())
		latest.Labels = map[string]string{"namespaceclassbinding-test": "stale-status"}
		Expect(k8sClient.Update(ctx, latest)).To(Succeed())
		controllerReconciler := &NamespaceClassBindingReconciler{
			Client: k8sClient,
		}

		err := controllerReconciler.setBindingStatus(ctx, stale, metav1.ConditionFalse, "StaleResourceVersion", "Status update tolerates stale resource versions", nil)

		Expect(err).NotTo(HaveOccurred())
		reconciled := &akuityiov1alpha1.NamespaceClassBinding{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(binding), reconciled)).To(Succeed())
		Expect(reconciled.Labels).To(HaveKeyWithValue("namespaceclassbinding-test", "stale-status"))
		ready := apimeta.FindStatusCondition(reconciled.Status.Conditions, akuityiov1alpha1.NamespaceClassBindingReadyCondition)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal("StaleResourceVersion"))
	})

	It("applies resources from multiple Ready NamespaceClasses", func() {
		namespace := createTestNamespace(ctx, "nscb-apply-many")
		publicNetwork := createNamespaceClass(ctx, "binding-public-network-many", configMapTemplate("public-config", "class", "public"))
		registryPush := createNamespaceClass(ctx, "binding-registry-push-many", configMapTemplate("registry-config", "class", "registry"))
		reconcileNamespaceClass(ctx, publicNetwork.Name)
		reconcileNamespaceClass(ctx, registryPush.Name)
		binding := createNamespaceClassBinding(ctx, namespace.Name, publicNetwork.Name, registryPush.Name)

		result := reconcileNamespaceClassBindingController(ctx, binding.Namespace, binding.Name)

		Expect(result).To(Equal(reconcile.Result{RequeueAfter: managedResourceSyncPeriod}))
		publicConfig := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "public-config"}, publicConfig)).To(Succeed())
		Expect(publicConfig.Data).To(HaveKeyWithValue("class", "public"))
		Expect(publicConfig.Labels).To(HaveKeyWithValue(classLabel, publicNetwork.Name))
		registryConfig := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "registry-config"}, registryConfig)).To(Succeed())
		Expect(registryConfig.Data).To(HaveKeyWithValue("class", "registry"))
		Expect(registryConfig.Labels).To(HaveKeyWithValue(classLabel, registryPush.Name))
	})

	It("converts CRD-admitted namespace mappings", func() {
		mappings := namespaceClassBindingMappings([]akuityiov1alpha1.NamespaceClassBindingMapping{{
			Namespace:  "first-namespace",
			ClassNames: []string{"first-class"},
		}, {
			Namespace:  "second-namespace",
			ClassNames: []string{"second-class"},
		}})

		Expect(mappings).To(Equal([]namespaceClassBindingMapping{{
			namespace:  "first-namespace",
			classNames: []string{"first-class"},
		}, {
			namespace:  "second-namespace",
			classNames: []string{"second-class"},
		}}))
	})

	It("deletes resources from classes removed from binding membership", func() {
		namespace := createTestNamespace(ctx, "nscb-membership")
		firstClass := createNamespaceClass(ctx, "binding-first-class", configMapTemplate("first-config", "class", "first"))
		secondClass := createNamespaceClass(ctx, "binding-second-class", configMapTemplate("second-config", "class", "second"))
		reconcileNamespaceClass(ctx, firstClass.Name)
		reconcileNamespaceClass(ctx, secondClass.Name)
		binding := createNamespaceClassBinding(ctx, namespace.Name, firstClass.Name)
		reconcileNamespaceClassBinding(ctx, binding.Namespace, binding.Name)

		updateNamespaceClassBindingClassNames(ctx, binding.Namespace, binding.Name, secondClass.Name)
		reconcileNamespaceClassBinding(ctx, binding.Namespace, binding.Name)

		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "first-config"}, &corev1.ConfigMap{})).To(MatchError(errors.IsNotFound, "IsNotFound"))
		secondConfig := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "second-config"}, secondConfig)).To(Succeed())
		Expect(secondConfig.Data).To(HaveKeyWithValue("class", "second"))
		expectNamespaceClassBindingInventory(ctx, binding.Name, []akuityiov1alpha1.NamespaceClassBindingNamespaceStatus{{
			Namespace: namespace.Name,
			Resources: []akuityiov1alpha1.NamespaceClassBindingAppliedResource{{
				APIVersion: "v1",
				Kind:       "ConfigMap",
				Name:       "second-config",
				ClassName:  secondClass.Name,
				UID:        secondConfig.UID,
			}},
		}})
	})

	It("deletes resources from namespaces removed from binding mappings", func() {
		namespace := createTestNamespace(ctx, "nscb-mapping-removed")
		namespaceClass := createNamespaceClass(ctx, "binding-mapping-removed-class", configMapTemplate("removed-mapping-config", "state", "managed"))
		reconcileNamespaceClass(ctx, namespaceClass.Name)
		binding := createNamespaceClassBinding(ctx, namespace.Name, namespaceClass.Name)
		reconcileNamespaceClassBinding(ctx, binding.Namespace, binding.Name)
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "removed-mapping-config"}, &corev1.ConfigMap{})).To(Succeed())

		removeNamespaceClassBindingMapping(ctx, binding.Name, namespace.Name)
		reconcileNamespaceClassBinding(ctx, binding.Namespace, binding.Name)

		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "removed-mapping-config"}, &corev1.ConfigMap{})).To(MatchError(errors.IsNotFound, "IsNotFound"))
		expectNamespaceClassBindingInventory(ctx, binding.Name, nil)
	})

	It("keeps resources from a listed class that is not Ready", func() {
		namespace := createTestNamespace(ctx, "nscb-not-ready")
		binding := createNamespaceClassBinding(ctx, namespace.Name, "binding-not-ready-class")
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace.Name,
				Name:      "kept-config",
				Labels: map[string]string{
					managedLabel: "true",
					classLabel:   "binding-not-ready-class",
				},
			},
			Data: map[string]string{"existing": "true"},
		}
		Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
		namespaceClass := createNamespaceClass(ctx, "binding-not-ready-class", configMapTemplate("kept-config", "existing", "false"))

		reconcileNamespaceClassBinding(ctx, binding.Namespace, binding.Name)

		reconciledConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "kept-config"}, reconciledConfigMap)).To(Succeed())
		Expect(reconciledConfigMap.Data).To(HaveKeyWithValue("existing", "true"))
		Expect(namespaceClass.Status.ObservedGeneration).To(BeZero())

		reconciledBinding := &akuityiov1alpha1.NamespaceClassBinding{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(binding), reconciledBinding)).To(Succeed())
		ready := apimeta.FindStatusCondition(reconciledBinding.Status.Conditions, akuityiov1alpha1.NamespaceClassBindingReadyCondition)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal("NamespaceClassNotReady"))
	})

	It("adds, updates, and removes resources when a NamespaceClass changes", func() {
		namespace := createTestNamespace(ctx, "nscb-class-update")
		namespaceClass := createNamespaceClass(ctx, "binding-mutable-class",
			configMapTemplate("updated-config", "version", "one"),
			configMapTemplate("removed-config", "state", "old"),
		)
		reconcileNamespaceClass(ctx, namespaceClass.Name)
		binding := createNamespaceClassBinding(ctx, namespace.Name, namespaceClass.Name)
		reconcileNamespaceClassBinding(ctx, binding.Namespace, binding.Name)

		updateNamespaceClassResources(ctx, namespaceClass.Name,
			configMapTemplate("updated-config", "version", "two"),
			configMapTemplate("added-config", "state", "new"),
		)
		reconcileNamespaceClass(ctx, namespaceClass.Name)
		reconcileNamespaceClassBinding(ctx, binding.Namespace, binding.Name)

		updatedConfig := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "updated-config"}, updatedConfig)).To(Succeed())
		Expect(updatedConfig.Data).To(HaveKeyWithValue("version", "two"))

		addedConfig := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "added-config"}, addedConfig)).To(Succeed())
		Expect(addedConfig.Data).To(HaveKeyWithValue("state", "new"))

		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "removed-config"}, &corev1.ConfigMap{})).To(MatchError(errors.IsNotFound, "IsNotFound"))
		expectNamespaceClassBindingInventory(ctx, binding.Name, []akuityiov1alpha1.NamespaceClassBindingNamespaceStatus{{
			Namespace: namespace.Name,
			Resources: []akuityiov1alpha1.NamespaceClassBindingAppliedResource{{
				APIVersion: "v1",
				Kind:       "ConfigMap",
				Name:       "added-config",
				ClassName:  namespaceClass.Name,
				UID:        addedConfig.UID,
			}, {
				APIVersion: "v1",
				Kind:       "ConfigMap",
				Name:       "updated-config",
				ClassName:  namespaceClass.Name,
				UID:        updatedConfig.UID,
			}},
		}})
	})

	It("maps namespace events to the default binding for configured namespaces", func() {
		namespace := createTestNamespace(ctx, "nscb-namespace-watch")
		binding := createNamespaceClassBinding(ctx, namespace.Name, "namespace-watch-class")
		controllerReconciler := &NamespaceClassBindingReconciler{
			Client: k8sClient,
		}

		requests := controllerReconciler.mapNamespaceToBinding(ctx, namespace)

		Expect(requests).To(Equal([]reconcile.Request{{
			NamespacedName: types.NamespacedName{Name: binding.Name},
		}}))
	})

	It("reports invalid templates discovered during binding reconciliation", func() {
		namespace := createTestNamespace(ctx, "nscb-invalid-template")
		namespaceClass := createNamespaceClass(ctx, "binding-invalid-template-class", runtime.RawExtension{
			Raw: []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{}}`),
		})
		markNamespaceClassReady(ctx, namespaceClass.Name)
		binding := createNamespaceClassBinding(ctx, namespace.Name, namespaceClass.Name)

		err := reconcileNamespaceClassBindingError(ctx, binding.Namespace, binding.Name)

		Expect(err).To(MatchError(ContainSubstring("metadata.name")))
		expectNamespaceClassBindingNotReadyCondition(ctx, binding, "TemplateInvalid")
	})

	It("reports cluster-scoped templates discovered during binding reconciliation", func() {
		namespace := createTestNamespace(ctx, "nscb-cluster-template")
		namespaceClass := createNamespaceClass(ctx, "binding-cluster-template-class", runtime.RawExtension{
			Raw: []byte(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"managed-namespace"}}`),
		})
		markNamespaceClassReady(ctx, namespaceClass.Name)
		binding := createNamespaceClassBinding(ctx, namespace.Name, namespaceClass.Name)

		err := reconcileNamespaceClassBindingError(ctx, binding.Namespace, binding.Name)

		Expect(err).To(MatchError(ContainSubstring("cluster-scoped kind")))
		expectNamespaceClassBindingNotReadyCondition(ctx, binding, "UnsupportedClusterScopedResource")
	})

	It("does not apply duplicate desired object identities", func() {
		namespace := createTestNamespace(ctx, "nscb-duplicate")
		namespaceClass := createNamespaceClass(ctx, "binding-duplicate-class",
			configMapTemplate("duplicate-config", "first", "one"),
			configMapTemplate("duplicate-config", "second", "two"),
		)
		reconcileNamespaceClass(ctx, namespaceClass.Name)
		binding := createNamespaceClassBinding(ctx, namespace.Name, namespaceClass.Name)

		err := reconcileNamespaceClassBindingError(ctx, binding.Namespace, binding.Name)

		Expect(err).NotTo(HaveOccurred())
		expectNamespaceClassBindingNotReadyCondition(ctx, binding, "DuplicateDesiredObject")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "duplicate-config"}, &corev1.ConfigMap{})).To(MatchError(errors.IsNotFound, "IsNotFound"))
	})

	It("does not adopt unmanaged objects with matching identities", func() {
		namespace := createTestNamespace(ctx, "nscb-unmanaged-conflict")
		namespaceClass := createNamespaceClass(ctx, "binding-conflict-class", configMapTemplate("conflict-config", "class", "managed"))
		reconcileNamespaceClass(ctx, namespaceClass.Name)
		binding := createNamespaceClassBinding(ctx, namespace.Name, namespaceClass.Name)
		unmanagedConfig := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace.Name,
				Name:      "conflict-config",
			},
			Data: map[string]string{"user": "owned"},
		}
		Expect(k8sClient.Create(ctx, unmanagedConfig)).To(Succeed())

		err := reconcileNamespaceClassBindingError(ctx, binding.Namespace, binding.Name)

		Expect(err).To(MatchError(ContainSubstring("not managed by NamespaceClassBinding")))
		expectNamespaceClassBindingNotReadyCondition(ctx, binding, "ManagedResourceConflict")
		reconciledConfig := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "conflict-config"}, reconciledConfig)).To(Succeed())
		Expect(reconciledConfig.Data).To(HaveKeyWithValue("user", "owned"))
		Expect(reconciledConfig.Data).NotTo(HaveKey("class"))
	})

	It("deletes managed resources when the binding is deleted", func() {
		namespace := createTestNamespace(ctx, "nscb-delete")
		namespaceClass := createNamespaceClass(ctx, "binding-delete-class", configMapTemplate("deleted-config", "state", "managed"))
		reconcileNamespaceClass(ctx, namespaceClass.Name)
		binding := createNamespaceClassBinding(ctx, namespace.Name, namespaceClass.Name)
		reconcileNamespaceClassBinding(ctx, binding.Namespace, binding.Name)
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "deleted-config"}, &corev1.ConfigMap{})).To(Succeed())

		reconciled := &akuityiov1alpha1.NamespaceClassBinding{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(binding), reconciled)).To(Succeed())
		Expect(k8sClient.Delete(ctx, reconciled)).To(Succeed())
		_, err := reconcileNamespaceClassBindingResult(ctx, binding.Namespace, binding.Name)

		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "deleted-config"}, &corev1.ConfigMap{})).To(MatchError(errors.IsNotFound, "IsNotFound"))
	})

	It("deletes applied resources without the binding object", func() {
		namespace := createTestNamespace(ctx, "nscb-delete-direct")
		className := "binding-delete-direct-class"
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace.Name,
				Name:      "delete-direct-config",
				Labels: map[string]string{
					managedLabel: "true",
					classLabel:   className,
				},
			},
		}
		Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
		identity := resourceIdentity{
			apiVersion: "v1",
			kind:       "ConfigMap",
			namespace:  namespace.Name,
			name:       configMap.Name,
		}
		controllerReconciler := &NamespaceClassBindingReconciler{
			Client: k8sClient,
		}

		err := controllerReconciler.deleteAppliedResources(ctx, map[resourceIdentity]appliedResource{
			identity: {
				identity:  identity,
				className: className,
				uid:       configMap.UID,
			},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: configMap.Name}, &corev1.ConfigMap{})).To(MatchError(errors.IsNotFound, "IsNotFound"))
	})

	It("does not delete stale status resources that no longer have binding ownership", func() {
		namespace := createTestNamespace(ctx, "nscb-delete-conflict")
		namespaceClass := createNamespaceClass(ctx, "binding-delete-conflict-class", configMapTemplate("delete-conflict-config", "state", "managed"))
		reconcileNamespaceClass(ctx, namespaceClass.Name)
		binding := createNamespaceClassBinding(ctx, namespace.Name, namespaceClass.Name)
		reconcileNamespaceClassBinding(ctx, binding.Namespace, binding.Name)

		configMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "delete-conflict-config"}, configMap)).To(Succeed())
		delete(configMap.Labels, managedLabel)
		Expect(k8sClient.Update(ctx, configMap)).To(Succeed())

		removeNamespaceClassBindingMapping(ctx, binding.Name, namespace.Name)
		_, err := reconcileNamespaceClassBindingResult(ctx, binding.Namespace, binding.Name)

		Expect(err).To(MatchError(ContainSubstring("not managed by NamespaceClassBinding")))
		expectNamespaceClassBindingNotReadyCondition(ctx, binding, "ManagedResourceDeleteFailed")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "delete-conflict-config"}, configMap)).To(Succeed())
		expectNamespaceClassBindingInventory(ctx, binding.Name, []akuityiov1alpha1.NamespaceClassBindingNamespaceStatus{{
			Namespace: namespace.Name,
			Resources: []akuityiov1alpha1.NamespaceClassBindingAppliedResource{{
				APIVersion: "v1",
				Kind:       "ConfigMap",
				Name:       "delete-conflict-config",
				ClassName:  namespaceClass.Name,
				UID:        configMap.UID,
			}},
		}})
	})
})

func createTestNamespace(ctx context.Context, name string) *corev1.Namespace {
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
	DeferCleanup(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, namespace))).To(Succeed())
	})
	return namespace
}

func createNamespaceClassBinding(ctx context.Context, namespaceName string, classNames ...string) *akuityiov1alpha1.NamespaceClassBinding {
	binding := &akuityiov1alpha1.NamespaceClassBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: akuityiov1alpha1.NamespaceClassBindingDefaultName,
		},
		Spec: akuityiov1alpha1.NamespaceClassBindingSpec{
			Mappings: []akuityiov1alpha1.NamespaceClassBindingMapping{{
				Namespace:  namespaceName,
				ClassNames: classNames,
			}},
		},
	}
	existing := &akuityiov1alpha1.NamespaceClassBinding{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: akuityiov1alpha1.NamespaceClassBindingDefaultName}, existing)
	if errors.IsNotFound(err) {
		Expect(k8sClient.Create(ctx, binding)).To(Succeed())
	} else {
		Expect(err).NotTo(HaveOccurred())
		upsertNamespaceClassBindingMapping(existing, namespaceName, classNames...)
		Expect(k8sClient.Update(ctx, existing)).To(Succeed())
		binding = existing
	}
	DeferCleanup(func() {
		cleanupNamespaceClassBinding(ctx, akuityiov1alpha1.NamespaceClassBindingDefaultName)
	})
	return binding
}

func updateNamespaceClassBindingClassNames(ctx context.Context, namespaceName, name string, classNames ...string) {
	binding := &akuityiov1alpha1.NamespaceClassBinding{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, binding)).To(Succeed())
	if namespaceName == "" {
		Expect(binding.Spec.Mappings).To(HaveLen(1))
		namespaceName = binding.Spec.Mappings[0].Namespace
	}
	upsertNamespaceClassBindingMapping(binding, namespaceName, classNames...)
	Expect(k8sClient.Update(ctx, binding)).To(Succeed())
}

func upsertNamespaceClassBindingMapping(binding *akuityiov1alpha1.NamespaceClassBinding, namespaceName string, classNames ...string) {
	for i := range binding.Spec.Mappings {
		if binding.Spec.Mappings[i].Namespace == namespaceName {
			binding.Spec.Mappings[i].ClassNames = classNames
			return
		}
	}
	binding.Spec.Mappings = append(binding.Spec.Mappings, akuityiov1alpha1.NamespaceClassBindingMapping{
		Namespace:  namespaceName,
		ClassNames: classNames,
	})
}

func removeNamespaceClassBindingMapping(ctx context.Context, name, namespaceName string) {
	binding := &akuityiov1alpha1.NamespaceClassBinding{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, binding)).To(Succeed())
	mappings := binding.Spec.Mappings[:0]
	for _, mapping := range binding.Spec.Mappings {
		if mapping.Namespace != namespaceName {
			mappings = append(mappings, mapping)
		}
	}
	binding.Spec.Mappings = mappings
	Expect(k8sClient.Update(ctx, binding)).To(Succeed())
}

func cleanupNamespaceClassBinding(ctx context.Context, name string) {
	binding := &akuityiov1alpha1.NamespaceClassBinding{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, binding); err != nil {
		Expect(client.IgnoreNotFound(err)).To(Succeed())
		return
	}
	binding.Finalizers = nil
	Expect(k8sClient.Update(ctx, binding)).To(Succeed())
	Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, binding))).To(Succeed())
	Eventually(func() bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, &akuityiov1alpha1.NamespaceClassBinding{})
		return errors.IsNotFound(err)
	}).Should(BeTrue())
}

func updateNamespaceClassResources(ctx context.Context, name string, resources ...runtime.RawExtension) {
	namespaceClass := &akuityiov1alpha1.NamespaceClass{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, namespaceClass)).To(Succeed())
	namespaceClass.Spec.Resources = resources
	Expect(k8sClient.Update(ctx, namespaceClass)).To(Succeed())
}

func markNamespaceClassReady(ctx context.Context, name string) {
	namespaceClass := &akuityiov1alpha1.NamespaceClass{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, namespaceClass)).To(Succeed())
	namespaceClass.Status.ObservedGeneration = namespaceClass.Generation
	namespaceClass.Status.Conditions = []metav1.Condition{{
		Type:               akuityiov1alpha1.NamespaceClassReadyCondition,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: namespaceClass.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             "TemplatesValid",
		Message:            "All resource templates are valid",
	}}
	Expect(k8sClient.Status().Update(ctx, namespaceClass)).To(Succeed())
}

func expectNamespaceClassBindingNotReadyCondition(ctx context.Context, binding *akuityiov1alpha1.NamespaceClassBinding, reason string) {
	reconciled := &akuityiov1alpha1.NamespaceClassBinding{}
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(binding), reconciled)).To(Succeed())
	ready := apimeta.FindStatusCondition(reconciled.Status.Conditions, akuityiov1alpha1.NamespaceClassBindingReadyCondition)
	Expect(ready).NotTo(BeNil())
	Expect(ready.Status).To(Equal(metav1.ConditionFalse))
	Expect(ready.Reason).To(Equal(reason))
}

func expectNamespaceClassBindingInventory(ctx context.Context, name string, namespaces []akuityiov1alpha1.NamespaceClassBindingNamespaceStatus) {
	reconciled := &akuityiov1alpha1.NamespaceClassBinding{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, reconciled)).To(Succeed())
	Expect(reconciled.Status.Namespaces).To(Equal(namespaces))
}

func reconcileNamespaceClassBinding(ctx context.Context, namespaceName, name string) {
	reconcileNamespaceClassBindingController(ctx, namespaceName, name)
}

func reconcileNamespaceClassBindingController(ctx context.Context, namespaceName, name string) reconcile.Result {
	_, err := reconcileNamespaceClassBindingResult(ctx, namespaceName, name)
	Expect(err).NotTo(HaveOccurred())
	result, err := reconcileNamespaceClassBindingResult(ctx, namespaceName, name)
	Expect(err).NotTo(HaveOccurred())
	return result
}

func reconcileNamespaceClassBindingError(ctx context.Context, namespaceName, name string) error {
	_, err := reconcileNamespaceClassBindingResult(ctx, namespaceName, name)
	Expect(err).NotTo(HaveOccurred())
	_, err = reconcileNamespaceClassBindingResult(ctx, namespaceName, name)
	return err
}

func reconcileNamespaceClassBindingResult(ctx context.Context, namespaceName, name string) (reconcile.Result, error) {
	controllerReconciler := &NamespaceClassBindingReconciler{
		Client:     k8sClient,
		Scheme:     k8sClient.Scheme(),
		RESTMapper: newTestRESTMapper(),
	}

	result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: namespaceName, Name: name},
	})
	return result, err
}

func configMapTemplate(name string, key string, value string) runtime.RawExtension {
	return runtime.RawExtension{
		Raw: []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"` + name + `"},"data":{"` + key + `":"` + value + `"}}`),
	}
}

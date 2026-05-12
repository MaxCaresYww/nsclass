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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	akuityiov1alpha1 "github.com/maxcaresyww/nsclass/api/v1alpha1"
)

var _ = Describe("Namespace Controller", func() {
	ctx := context.Background()

	It("applies resources from Ready NamespaceClasses", func() {
		namespace := createNamespace(ctx, "nsclass-apply", "public-network")
		namespaceClass := createNamespaceClass(ctx, "public-network", configMapTemplate("namespaceclass-sample", "example", "managed"))
		reconcileNamespaceClass(ctx, namespaceClass.Name)

		result := reconcileNamespaceController(ctx, namespace.Name, nil)

		Expect(result).To(Equal(reconcile.Result{RequeueAfter: managedResourceSyncPeriod}))
		configMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "namespaceclass-sample"}, configMap)).To(Succeed())
		Expect(configMap.Data).To(HaveKeyWithValue("example", "managed"))
		Expect(configMap.Labels).To(HaveKeyWithValue(managedLabel, "true"))
		Expect(configMap.Labels).To(HaveKeyWithValue(classLabel, namespaceClass.Name))
		Expect(configMap.Labels).To(HaveKeyWithValue(namespaceLabel, namespace.Name))
		Expect(configMap.Annotations).To(HaveKeyWithValue(templateIDAnnotation, "v1/ConfigMap/namespaceclass-sample"))
	})

	It("applies resources from Ready NamespaceClasses listed in the annotation", func() {
		namespace := createNamespaceWithClassAnnotation(ctx, "nsclass-apply-many", "annotation-public-network, annotation-registry-push")
		publicNetwork := createNamespaceClass(ctx, "annotation-public-network", configMapTemplate("public-config", "class", "public"))
		registryPush := createNamespaceClass(ctx, "annotation-registry-push", configMapTemplate("registry-config", "class", "registry"))
		reconcileNamespaceClass(ctx, publicNetwork.Name)
		reconcileNamespaceClass(ctx, registryPush.Name)

		result := reconcileNamespaceController(ctx, namespace.Name, nil)

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

	It("ignores the legacy namespace class label", func() {
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "nsclass-legacy-label-ignored",
				Labels: map[string]string{
					"namespaceclass.akuity.io/name": "legacy-class",
				},
			},
		}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, namespace))).To(Succeed())
		})
		legacyClass := createNamespaceClass(ctx, "legacy-class", configMapTemplate("legacy-config", "class", "legacy"))
		reconcileNamespaceClass(ctx, legacyClass.Name)

		result := reconcileNamespaceController(ctx, namespace.Name, nil)

		Expect(result).To(Equal(reconcile.Result{}))
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "legacy-config"}, &corev1.ConfigMap{})).To(MatchError(errors.IsNotFound, "IsNotFound"))
	})

	It("parses comma-separated namespace class memberships from the annotation", func() {
		classNames, err := parseNamespaceClassNamesAnnotation("first-class, second-class")

		Expect(err).NotTo(HaveOccurred())
		Expect(classNames).To(Equal([]string{"first-class", "second-class"}))
	})

	It("rejects empty and duplicate namespace class memberships in the annotation", func() {
		classNames, err := parseNamespaceClassNamesAnnotation("first-class,,second-class")

		Expect(err).To(MatchError(ContainSubstring("empty NamespaceClass names")))
		Expect(classNames).To(BeNil())

		classNames, err = parseNamespaceClassNamesAnnotation("first-class,second-class,first-class")

		Expect(err).To(MatchError(ContainSubstring("duplicate NamespaceClass")))
		Expect(classNames).To(BeNil())

		classNames, err = parseNamespaceClassNamesAnnotation("first-class second-class")

		Expect(err).To(MatchError(ContainSubstring("invalid NamespaceClass name")))
		Expect(classNames).To(BeNil())
	})

	It("deletes resources from classes removed from namespace membership", func() {
		namespace := createNamespace(ctx, "nsclass-membership", "first-class")
		firstClass := createNamespaceClass(ctx, "first-class", configMapTemplate("first-config", "class", "first"))
		secondClass := createNamespaceClass(ctx, "second-class", configMapTemplate("second-config", "class", "second"))
		reconcileNamespaceClass(ctx, firstClass.Name)
		reconcileNamespaceClass(ctx, secondClass.Name)

		reconcileNamespace(ctx, namespace.Name, nil)

		updateNamespaceClassAnnotation(ctx, namespace.Name, "second-class")
		reconcileNamespace(ctx, namespace.Name, nil)

		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "first-config"}, &corev1.ConfigMap{})).To(MatchError(errors.IsNotFound, "IsNotFound"))
		secondConfig := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "second-config"}, secondConfig)).To(Succeed())
		Expect(secondConfig.Data).To(HaveKeyWithValue("class", "second"))
	})

	It("deletes resources from classes removed from annotation membership", func() {
		namespace := createNamespaceWithClassAnnotation(ctx, "nsclass-annotation-membership", "annotation-first-class,annotation-second-class")
		firstClass := createNamespaceClass(ctx, "annotation-first-class", configMapTemplate("first-config-annotation", "class", "first"))
		secondClass := createNamespaceClass(ctx, "annotation-second-class", configMapTemplate("second-config-annotation", "class", "second"))
		reconcileNamespaceClass(ctx, firstClass.Name)
		reconcileNamespaceClass(ctx, secondClass.Name)

		reconcileNamespace(ctx, namespace.Name, nil)

		updateNamespaceClassAnnotation(ctx, namespace.Name, "annotation-second-class")
		reconcileNamespace(ctx, namespace.Name, nil)

		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "first-config-annotation"}, &corev1.ConfigMap{})).To(MatchError(errors.IsNotFound, "IsNotFound"))
		secondConfig := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "second-config-annotation"}, secondConfig)).To(Succeed())
		Expect(secondConfig.Data).To(HaveKeyWithValue("class", "second"))
	})

	It("skips namespaces that are terminating", func() {
		deletionTime := metav1.Now()
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "nsclass-terminating",
				Annotations: map[string]string{
					namespaceClassNamesAnnotation: "terminating-class",
				},
				DeletionTimestamp: &deletionTime,
				Finalizers:        []string{"test.example.com/finalizer"},
			},
		}
		namespaceClass := &akuityiov1alpha1.NamespaceClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "terminating-class",
				Generation: 1,
			},
			Spec: akuityiov1alpha1.NamespaceClassSpec{
				Resources: []runtime.RawExtension{configMapTemplate("ignored-config", "state", "ignored")},
			},
			Status: akuityiov1alpha1.NamespaceClassStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{{
					Type:               akuityiov1alpha1.NamespaceClassReadyCondition,
					Status:             metav1.ConditionTrue,
					ObservedGeneration: 1,
					LastTransitionTime: metav1.Now(),
					Reason:             "TemplatesValid",
					Message:            "All resource templates are valid",
				}},
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithScheme(k8sClient.Scheme()).
			WithObjects(namespace, namespaceClass).
			Build()
		controllerReconciler := &NamespaceReconciler{
			Client: fakeClient,
			Scheme: k8sClient.Scheme(),
		}

		_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: namespace.Name},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(fakeClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "ignored-config"}, &corev1.ConfigMap{})).To(MatchError(errors.IsNotFound, "IsNotFound"))
	})

	It("keeps resources from a listed class that is not Ready", func() {
		namespace := createNamespace(ctx, "nsclass-not-ready", "not-ready-class")
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace.Name,
				Name:      "kept-config",
				Labels: map[string]string{
					managedLabel:   "true",
					classLabel:     "not-ready-class",
					namespaceLabel: namespace.Name,
				},
			},
			Data: map[string]string{"existing": "true"},
		}
		Expect(k8sClient.Create(ctx, configMap)).To(Succeed())

		namespaceClass := createNamespaceClass(ctx, "not-ready-class", configMapTemplate("kept-config", "existing", "false"))
		recorder := events.NewFakeRecorder(10)
		reconcileNamespace(ctx, namespace.Name, recorder)

		reconciledConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "kept-config"}, reconciledConfigMap)).To(Succeed())
		Expect(reconciledConfigMap.Data).To(HaveKeyWithValue("existing", "true"))
		Expect(namespaceClass.Status.ObservedGeneration).To(BeZero())
		Expect(<-recorder.Events).To(ContainSubstring("NamespaceClassNotReady"))
	})

	It("adds, updates, and removes resources when a NamespaceClass changes", func() {
		namespace := createNamespace(ctx, "nsclass-class-update", "mutable-class")
		namespaceClass := createNamespaceClass(ctx, "mutable-class",
			configMapTemplate("updated-config", "version", "one"),
			configMapTemplate("removed-config", "state", "old"),
		)
		reconcileNamespaceClass(ctx, namespaceClass.Name)
		reconcileNamespace(ctx, namespace.Name, nil)

		updateNamespaceClassResources(ctx, namespaceClass.Name,
			configMapTemplate("updated-config", "version", "two"),
			configMapTemplate("added-config", "state", "new"),
		)
		reconcileNamespaceClass(ctx, namespaceClass.Name)
		reconcileNamespace(ctx, namespace.Name, nil)

		updatedConfig := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "updated-config"}, updatedConfig)).To(Succeed())
		Expect(updatedConfig.Data).To(HaveKeyWithValue("version", "two"))

		addedConfig := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "added-config"}, addedConfig)).To(Succeed())
		Expect(addedConfig.Data).To(HaveKeyWithValue("state", "new"))

		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "removed-config"}, &corev1.ConfigMap{})).To(MatchError(errors.IsNotFound, "IsNotFound"))
	})

	It("reports invalid templates discovered during namespace reconciliation", func() {
		namespace := createNamespace(ctx, "nsclass-invalid-template", "invalid-template-class")
		namespaceClass := createNamespaceClass(ctx, "invalid-template-class", runtime.RawExtension{
			Raw: []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{}}`),
		})
		markNamespaceClassReady(ctx, namespaceClass.Name)

		recorder := events.NewFakeRecorder(10)
		err := reconcileNamespaceResult(ctx, namespace.Name, recorder)

		Expect(err).To(MatchError(ContainSubstring("metadata.name")))
		Eventually(recorder.Events).Should(Receive(ContainSubstring("TemplateInvalid")))
	})

	It("reports cluster-scoped templates discovered during namespace reconciliation", func() {
		namespace := createNamespace(ctx, "nsclass-cluster-template", "cluster-template-class")
		namespaceClass := createNamespaceClass(ctx, "cluster-template-class", runtime.RawExtension{
			Raw: []byte(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"managed-namespace"}}`),
		})
		markNamespaceClassReady(ctx, namespaceClass.Name)

		recorder := events.NewFakeRecorder(10)
		err := reconcileNamespaceResult(ctx, namespace.Name, recorder)

		Expect(err).To(MatchError(ContainSubstring("cluster-scoped kind")))
		Eventually(recorder.Events).Should(Receive(ContainSubstring("UnsupportedClusterScopedResource")))
	})

	It("does not apply duplicate desired object identities", func() {
		namespace := createNamespace(ctx, "nsclass-duplicate", "duplicate-class")
		namespaceClass := createNamespaceClass(ctx, "duplicate-class",
			configMapTemplate("duplicate-config", "first", "one"),
			configMapTemplate("duplicate-config", "second", "two"),
		)
		reconcileNamespaceClass(ctx, namespaceClass.Name)

		recorder := events.NewFakeRecorder(10)
		err := reconcileNamespaceResult(ctx, namespace.Name, recorder)

		Expect(err).NotTo(HaveOccurred())
		Eventually(recorder.Events).Should(Receive(ContainSubstring("DuplicateDesiredObject")))
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "duplicate-config"}, &corev1.ConfigMap{})).To(MatchError(errors.IsNotFound, "IsNotFound"))
	})

	It("does not adopt unmanaged objects with matching identities", func() {
		namespace := createNamespace(ctx, "nsclass-unmanaged-conflict", "conflict-class")
		namespaceClass := createNamespaceClass(ctx, "conflict-class", configMapTemplate("conflict-config", "class", "managed"))
		reconcileNamespaceClass(ctx, namespaceClass.Name)
		unmanagedConfig := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace.Name,
				Name:      "conflict-config",
			},
			Data: map[string]string{"user": "owned"},
		}
		Expect(k8sClient.Create(ctx, unmanagedConfig)).To(Succeed())

		recorder := events.NewFakeRecorder(10)
		err := reconcileNamespaceResult(ctx, namespace.Name, recorder)

		Expect(err).To(MatchError(ContainSubstring("not managed by NamespaceClass controller")))
		Eventually(recorder.Events).Should(Receive(ContainSubstring("ManagedResourceConflict")))
		reconciledConfig := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: "conflict-config"}, reconciledConfig)).To(Succeed())
		Expect(reconciledConfig.Data).To(HaveKeyWithValue("user", "owned"))
		Expect(reconciledConfig.Data).NotTo(HaveKey("class"))
	})

	It("skips deprecated core Endpoints when discovering managed resources", func() {
		endpoints := metav1.APIResource{
			Name:       "endpoints",
			Kind:       "Endpoints",
			Namespaced: true,
			Verbs:      metav1.Verbs{"list"},
		}
		endpointSlices := metav1.APIResource{
			Name:       "endpointslices",
			Kind:       "EndpointSlice",
			Namespaced: true,
			Verbs:      metav1.Verbs{"list"},
		}

		Expect(shouldListManagedResource(schema.GroupVersion{Version: "v1"}, endpoints)).To(BeFalse())
		Expect(shouldListManagedResource(schema.GroupVersion{Group: "discovery.k8s.io", Version: "v1"}, endpointSlices)).To(BeTrue())
	})
})

func createNamespace(ctx context.Context, name, classNames string) *corev1.Namespace {
	return createNamespaceWithClassAnnotation(ctx, name, classNames)
}

func createNamespaceWithClassAnnotation(ctx context.Context, name, classNames string) *corev1.Namespace {
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				namespaceClassNamesAnnotation: classNames,
			},
		},
	}
	Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
	DeferCleanup(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, namespace))).To(Succeed())
	})
	return namespace
}

func updateNamespaceClassAnnotation(ctx context.Context, namespaceName, classNames string) {
	namespace := &corev1.Namespace{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: namespaceName}, namespace)).To(Succeed())
	if namespace.Annotations == nil {
		namespace.Annotations = make(map[string]string)
	}
	namespace.Annotations[namespaceClassNamesAnnotation] = classNames
	Expect(k8sClient.Update(ctx, namespace)).To(Succeed())
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

func reconcileNamespace(ctx context.Context, name string, recorder events.EventRecorder) {
	Expect(reconcileNamespaceResult(ctx, name, recorder)).To(Succeed())
}

func reconcileNamespaceResult(ctx context.Context, name string, recorder events.EventRecorder) error {
	_, err := reconcileNamespaceControllerResult(ctx, name, recorder)
	return err
}

func reconcileNamespaceController(ctx context.Context, name string, recorder events.EventRecorder) reconcile.Result {
	result, err := reconcileNamespaceControllerResult(ctx, name, recorder)
	Expect(err).NotTo(HaveOccurred())
	return result
}

func reconcileNamespaceControllerResult(ctx context.Context, name string, recorder events.EventRecorder) (reconcile.Result, error) {
	controllerReconciler := &NamespaceReconciler{
		Client:     k8sClient,
		Scheme:     k8sClient.Scheme(),
		RESTMapper: newTestRESTMapper(),
		Recorder:   recorder,
	}

	result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name},
	})
	return result, err
}

func configMapTemplate(name string, key string, value string) runtime.RawExtension {
	return runtime.RawExtension{
		Raw: []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"` + name + `"},"data":{"` + key + `":"` + value + `"}}`),
	}
}

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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	akuityiov1alpha1 "github.com/maxcaresyww/nsclass/api/v1alpha1"
)

var _ = Describe("NamespaceClass Controller", func() {
	ctx := context.Background()

	It("marks the class Ready when all templates are valid namespaced resources", func() {
		namespaceClass := createNamespaceClass(ctx, "valid-class", runtime.RawExtension{
			Raw: []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"example"},"data":{"key":"value"}}`),
		})

		reconciled := reconcileNamespaceClass(ctx, namespaceClass.Name)

		Expect(reconciled.Status.ObservedGeneration).To(Equal(reconciled.Generation))
		ready := apimeta.FindStatusCondition(reconciled.Status.Conditions, akuityiov1alpha1.NamespaceClassReadyCondition)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))
		Expect(ready.Reason).To(Equal("TemplatesValid"))
	})

	It("marks the class not Ready when a template is missing metadata.name", func() {
		namespaceClass := createNamespaceClass(ctx, "missing-name-class", runtime.RawExtension{
			Raw: []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{}}`),
		})

		reconciled := reconcileNamespaceClass(ctx, namespaceClass.Name)

		Expect(reconciled.Status.ObservedGeneration).To(Equal(reconciled.Generation))
		ready := apimeta.FindStatusCondition(reconciled.Status.Conditions, akuityiov1alpha1.NamespaceClassReadyCondition)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal("TemplateInvalid"))
		Expect(ready.Message).To(ContainSubstring("metadata.name"))
	})

	It("marks the class not Ready when a template is cluster-scoped", func() {
		namespaceClass := createNamespaceClass(ctx, "cluster-scoped-class", runtime.RawExtension{
			Raw: []byte(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"example"}}`),
		})

		reconciled := reconcileNamespaceClass(ctx, namespaceClass.Name)

		Expect(reconciled.Status.ObservedGeneration).To(Equal(reconciled.Generation))
		ready := apimeta.FindStatusCondition(reconciled.Status.Conditions, akuityiov1alpha1.NamespaceClassReadyCondition)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal("UnsupportedClusterScopedResource"))
		Expect(ready.Message).To(ContainSubstring("cluster-scoped"))
	})
})

func createNamespaceClass(ctx context.Context, name string, resources ...runtime.RawExtension) *akuityiov1alpha1.NamespaceClass {
	namespaceClass := &akuityiov1alpha1.NamespaceClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: akuityiov1alpha1.NamespaceClassSpec{
			Resources: resources,
		},
	}
	Expect(k8sClient.Create(ctx, namespaceClass)).To(Succeed())
	DeferCleanup(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, namespaceClass))).To(Succeed())
	})
	return namespaceClass
}

func reconcileNamespaceClass(ctx context.Context, name string) *akuityiov1alpha1.NamespaceClass {
	httpClient, err := rest.HTTPClientFor(cfg)
	Expect(err).NotTo(HaveOccurred())

	restMapper, err := apiutil.NewDynamicRESTMapper(cfg, httpClient)
	Expect(err).NotTo(HaveOccurred())

	controllerReconciler := &NamespaceClassReconciler{
		Client:     k8sClient,
		Scheme:     k8sClient.Scheme(),
		RESTMapper: restMapper,
	}

	_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name},
	})
	Expect(err).NotTo(HaveOccurred())

	namespaceClass := &akuityiov1alpha1.NamespaceClass{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, namespaceClass)).To(Succeed())
	return namespaceClass
}

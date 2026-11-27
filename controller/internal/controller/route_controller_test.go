/*
Copyright 2026.

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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gatewayv1alpha1 "gateway/controller/api/v1alpha1"
)

var _ = Describe("Route controller", func() {
	const namespace = "default"
	var reconciler *RouteReconciler

	BeforeEach(func() {
		var routes gatewayv1alpha1.RouteList
		Expect(k8sClient.List(context.Background(), &routes, client.InNamespace(namespace))).To(Succeed())
		for i := range routes.Items {
			Expect(client.IgnoreNotFound(k8sClient.Delete(context.Background(), &routes.Items[i]))).To(Succeed())
		}
		var cm corev1.ConfigMap
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Namespace: namespace, Name: defaultConfigMapName,
		}, &cm)
		Expect(client.IgnoreNotFound(err)).To(Succeed())
		if err == nil {
			Expect(k8sClient.Delete(context.Background(), &cm)).To(Succeed())
		}
		fakeRecorder := record.NewFakeRecorder(10)
		reconciler = &RouteReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: fakeRecorder.Eventf,
		}
	})

	It("creates and removes routes in the aggregated ConfigMap", func() {
		created := routeObject("users", "/api/users")
		Expect(k8sClient.Create(context.Background(), created)).To(Succeed())
		reconcileRoute(reconciler, namespace)

		cm := getConfigMap(namespace)
		Expect(cm.Data[defaultConfigMapKey]).To(ContainSubstring("prefix: /api/users"))

		Expect(k8sClient.Delete(context.Background(), created)).To(Succeed())
		reconcileRoute(reconciler, namespace)
		cm = getConfigMap(namespace)
		Expect(cm.Data[defaultConfigMapKey]).NotTo(ContainSubstring("prefix: /api/users"))
		Expect(cm.Data[defaultConfigMapKey]).To(ContainSubstring("listen: :8080"))
	})

	It("reports duplicate prefixes as conflicts", func() {
		oldRoute := routeObject("a-old", "/api")
		newRoute := routeObject("z-new", "/api")
		Expect(k8sClient.Create(context.Background(), oldRoute)).To(Succeed())
		Expect(k8sClient.Create(context.Background(), newRoute)).To(Succeed())
		reconcileRoute(reconciler, namespace)

		var winner, loser gatewayv1alpha1.Route
		Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(oldRoute), &winner)).To(Succeed())
		Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(newRoute), &loser)).To(Succeed())
		Expect(meta.IsStatusConditionTrue(winner.Status.Conditions, conflictCondition)).To(BeFalse())
		Expect(meta.IsStatusConditionTrue(loser.Status.Conditions, conflictCondition)).To(BeTrue())
		Expect(loser.Status.ObservedGeneration).To(Equal(loser.Generation))
		cm := getConfigMap(namespace)
		Expect(strings.Count(cm.Data[defaultConfigMapKey], "prefix: /api")).To(Equal(1))
	})

	It("rejects invalid Route specs at the API server", func() {
		invalid := routeObject("invalid", "api")
		err := k8sClient.Create(context.Background(), invalid)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected Invalid, got %v", err)

		invalid = routeObject("missing-upstreams", "/api")
		invalid.Spec.Upstreams = nil
		err = k8sClient.Create(context.Background(), invalid)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected Invalid, got %v", err)
	})
})

func routeObject(name, prefix string) *gatewayv1alpha1.Route {
	return &gatewayv1alpha1.Route{
		Name: name, Namespace: "default",
		Spec: gatewayv1alpha1.RouteSpec{
			Prefix:    prefix,
			Upstreams: []string{"http://upstream-" + name + ":8080"},
		},
	}
}

func reconcileRoute(reconciler *RouteReconciler, namespace string) {
	_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
		Namespace: namespace, Name: "route-event",
	})
	Expect(err).NotTo(HaveOccurred())
}

func getConfigMap(namespace string) *corev1.ConfigMap {
	var cm corev1.ConfigMap
	Expect(k8sClient.Get(context.Background(), types.NamespacedName{
		Namespace: namespace, Name: defaultConfigMapName,
	}, &cm)).To(Succeed())
	return &cm
}

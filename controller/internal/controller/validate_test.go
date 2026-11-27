package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	gatewayv1alpha1 "gateway/controller/api/v1alpha1"
)

// These specs cover M1: Ready means the gateway accepts the config. Routes are
// checked with the gateway's own config rules (gateway/pkg/config), which are
// stricter than the CRD schema, for example on upstream URLs.
var _ = Describe("Route controller validation", func() {
	const (
		baseName   = "gateway"
		mergedName = "gateway-merged"
	)
	var reconciler *RouteReconciler

	BeforeEach(func() {
		bg := context.Background()
		var routes gatewayv1alpha1.RouteList
		Expect(k8sClient.List(bg, &routes, client.InNamespace(mergeNamespace))).To(Succeed())
		for i := range routes.Items {
			Expect(client.IgnoreNotFound(k8sClient.Delete(bg, &routes.Items[i]))).To(Succeed())
		}
		for _, name := range []string{baseName, mergedName} {
			cm := &corev1.ConfigMap{Name: name, Namespace: mergeNamespace}
			Expect(client.IgnoreNotFound(k8sClient.Delete(bg, cm))).To(Succeed())
		}
		reconciler = &RouteReconciler{
			Client:            k8sClient,
			Scheme:            k8sClient.Scheme(),
			Recorder:          record.NewFakeRecorder(50).Eventf,
			ConfigMapName:     mergedName,
			BaseConfigMapName: baseName,
		}
	})

	createBase := func(body string) {
		Expect(k8sClient.Create(context.Background(), &corev1.ConfigMap{
			Name: baseName, Namespace: mergeNamespace,
			Data: map[string]string{defaultConfigMapKey: body},
		})).To(Succeed())
	}
	merged := func() string {
		var cm corev1.ConfigMap
		Expect(k8sClient.Get(context.Background(),
			types.NamespacedName{Namespace: mergeNamespace, Name: mergedName}, &cm)).To(Succeed())
		return cm.Data[defaultConfigMapKey]
	}
	getRoute := func(name string) *gatewayv1alpha1.Route {
		var route gatewayv1alpha1.Route
		Expect(k8sClient.Get(context.Background(),
			types.NamespacedName{Namespace: mergeNamespace, Name: name}, &route)).To(Succeed())
		return &route
	}
	badRoute := func(name, prefix string) *gatewayv1alpha1.Route {
		route := routeObject(name, prefix)
		route.Spec.Upstreams = []string{"ftp://not-http:21"} // passes the CRD, not the gateway
		return route
	}

	It("leaves a Route the gateway would reject out of the config and reports it", func() {
		createBase(testBase)
		Expect(k8sClient.Create(context.Background(), routeObject("good", "/api/good"))).To(Succeed())
		Expect(k8sClient.Create(context.Background(), badRoute("bad", "/api/bad"))).To(Succeed())
		reconcileMerged(reconciler)

		Expect(merged()).To(ContainSubstring("prefix: /api/good"))
		Expect(merged()).NotTo(ContainSubstring("/api/bad"))

		bad := getRoute("bad")
		ready := meta.FindStatusCondition(bad.Status.Conditions, readyCondition)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal("InvalidConfig"))
		Expect(ready.Message).To(ContainSubstring("invalid upstream"))
		Expect(meta.IsStatusConditionTrue(getRoute("good").Status.Conditions, readyCondition)).To(BeTrue())

		By("fixing the Route")
		bad.Spec.Upstreams = []string{"http://fixed:8080"}
		Expect(k8sClient.Update(context.Background(), bad)).To(Succeed())
		reconcileMerged(reconciler)
		Expect(merged()).To(ContainSubstring("prefix: /api/bad"))
		Expect(meta.IsStatusConditionTrue(getRoute("bad").Status.Conditions, readyCondition)).To(BeTrue())
	})

	It("validates without a base ConfigMap too", func() {
		reconciler.ConfigMapName = defaultConfigMapName
		reconciler.BaseConfigMapName = ""
		Expect(k8sClient.Create(context.Background(), routeObject("good", "/api/good"))).To(Succeed())
		Expect(k8sClient.Create(context.Background(), badRoute("bad", "/api/bad"))).To(Succeed())
		reconcileMerged(reconciler)

		var cm corev1.ConfigMap
		Expect(k8sClient.Get(context.Background(),
			types.NamespacedName{Namespace: mergeNamespace, Name: defaultConfigMapName}, &cm)).To(Succeed())
		Expect(cm.Data[defaultConfigMapKey]).To(ContainSubstring("prefix: /api/good"))
		Expect(cm.Data[defaultConfigMapKey]).NotTo(ContainSubstring("/api/bad"))
		Expect(meta.IsStatusConditionTrue(getRoute("bad").Status.Conditions, readyCondition)).To(BeFalse())
		Expect(k8sClient.Delete(context.Background(), &cm)).To(Succeed())
	})

	It("keeps the last config when the base itself is rejected by the gateway", func() {
		createBase(testBase)
		reconcileMerged(reconciler)
		before := merged()

		var base corev1.ConfigMap
		Expect(k8sClient.Get(context.Background(),
			types.NamespacedName{Namespace: mergeNamespace, Name: baseName}, &base)).To(Succeed())
		base.Data[defaultConfigMapKey] = "routes:\n  - prefix: /\n    upstreams: [not-a-url]\n"
		Expect(k8sClient.Update(context.Background(), &base)).To(Succeed())
		Expect(k8sClient.Create(context.Background(), routeObject("good", "/api/good"))).To(Succeed())

		_, err := reconciler.Reconcile(context.Background(), mergedRequest())
		Expect(err).To(MatchError(ContainSubstring("invalid upstream")))
		Expect(merged()).To(Equal(before))
		ready := meta.FindStatusCondition(getRoute("good").Status.Conditions, readyCondition)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal("InvalidBaseConfig"))
	})
})

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gatewayv1alpha1 "gateway/controller/api/v1alpha1"
)

// mergeNamespace is where the base ConfigMap specs run.
const mergeNamespace = "default"

// These specs cover the chart-owned base ConfigMap (H1): Helm writes the base,
// the controller writes only the merged ConfigMap the gateway mounts.
var _ = Describe("Route controller with a base ConfigMap", func() {
	const (
		namespace  = mergeNamespace
		baseName   = "gateway"
		mergedName = "gateway-merged"
	)
	var reconciler *RouteReconciler

	BeforeEach(func() {
		bg := context.Background()
		var routes gatewayv1alpha1.RouteList
		Expect(k8sClient.List(bg, &routes, client.InNamespace(namespace))).To(Succeed())
		for i := range routes.Items {
			Expect(client.IgnoreNotFound(k8sClient.Delete(bg, &routes.Items[i]))).To(Succeed())
		}
		for _, name := range []string{baseName, mergedName} {
			cm := &corev1.ConfigMap{Name: name, Namespace: namespace}
			Expect(client.IgnoreNotFound(k8sClient.Delete(bg, cm))).To(Succeed())
		}
		reconciler = &RouteReconciler{
			Client:            k8sClient,
			Scheme:            k8sClient.Scheme(),
			Recorder:          record.NewFakeRecorder(20).Eventf,
			ConfigMapName:     mergedName,
			BaseConfigMapName: baseName,
		}
	})

	applyBase := func(body string) {
		bg := context.Background()
		var cm corev1.ConfigMap
		err := k8sClient.Get(bg, types.NamespacedName{Namespace: namespace, Name: baseName}, &cm)
		if err == nil {
			cm.Data = map[string]string{defaultConfigMapKey: body}
			Expect(k8sClient.Update(bg, &cm)).To(Succeed())
			return
		}
		cm = corev1.ConfigMap{
			Name: baseName, Namespace: namespace,
			Data: map[string]string{defaultConfigMapKey: body},
		}
		Expect(k8sClient.Create(bg, &cm)).To(Succeed())
	}
	getCM := func(name string) *corev1.ConfigMap {
		var cm corev1.ConfigMap
		Expect(k8sClient.Get(context.Background(),
			types.NamespacedName{Namespace: namespace, Name: name}, &cm)).To(Succeed())
		return &cm
	}

	It("keeps chart routes and settings next to Route routes, and survives a base re-render", func() {
		applyBase(testBase)
		Expect(k8sClient.Create(context.Background(), routeObject("users", "/api/users"))).To(Succeed())
		reconcileMerged(reconciler)

		merged := getCM(mergedName).Data[defaultConfigMapKey]
		Expect(merged).To(ContainSubstring("http://chart-default:8080"))
		Expect(merged).To(ContainSubstring("failure_threshold: 7"))
		Expect(merged).To(ContainSubstring("prefix: /api/users"))
		Expect(getCM(baseName).Data[defaultConfigMapKey]).To(Equal(testBase),
			"the controller must not write the chart-owned base")

		By("re-rendering the base, as helm upgrade does")
		applyBase(testBase + "  - prefix: /new\n    upstreams: [http://chart-new:8080]\n")
		reconcileMerged(reconciler)
		merged = getCM(mergedName).Data[defaultConfigMapKey]
		Expect(merged).To(ContainSubstring("prefix: /new"))
		Expect(merged).To(ContainSubstring("prefix: /api/users"))
	})

	It("owns the merged ConfigMap through the base, so uninstalling the chart removes it", func() {
		applyBase(testBase)
		reconcileMerged(reconciler)

		base := getCM(baseName)
		owners := getCM(mergedName).OwnerReferences
		Expect(owners).To(HaveLen(1))
		Expect(owners[0].Kind).To(Equal("ConfigMap"))
		Expect(owners[0].UID).To(Equal(base.UID))
	})

	It("marks a Route that collides with a chart route as a conflict", func() {
		applyBase(testBase)
		Expect(k8sClient.Create(context.Background(), routeObject("root", "/"))).To(Succeed())
		reconcileMerged(reconciler)

		var root gatewayv1alpha1.Route
		Expect(k8sClient.Get(context.Background(),
			types.NamespacedName{Namespace: namespace, Name: "root"}, &root)).To(Succeed())
		Expect(meta.IsStatusConditionTrue(root.Status.Conditions, conflictCondition)).To(BeTrue())
		Expect(meta.IsStatusConditionTrue(root.Status.Conditions, readyCondition)).To(BeFalse())
		Expect(getCM(mergedName).Data[defaultConfigMapKey]).NotTo(ContainSubstring("upstream-root"))
	})

	It("waits for the base instead of writing a config without it", func() {
		Expect(k8sClient.Create(context.Background(), routeObject("users", "/api/users"))).To(Succeed())
		reconcileMerged(reconciler)

		var cm corev1.ConfigMap
		err := k8sClient.Get(context.Background(),
			types.NamespacedName{Namespace: namespace, Name: mergedName}, &cm)
		Expect(err).To(HaveOccurred(), "no merged ConfigMap should exist before the base")
	})

	It("keeps the last merged config when the base is invalid", func() {
		applyBase(testBase)
		reconcileMerged(reconciler)
		before := getCM(mergedName).Data[defaultConfigMapKey]

		applyBase("routes: {not: a list}\n")
		_, err := reconciler.Reconcile(context.Background(), mergedRequest())
		Expect(err).To(HaveOccurred())
		Expect(getCM(mergedName).Data[defaultConfigMapKey]).To(Equal(before))
	})

	It("maps only the base and merged ConfigMaps to reconcile requests", func() {
		for name, want := range map[string]int{baseName: 1, mergedName: 1, "unrelated": 0} {
			cm := &corev1.ConfigMap{Name: name, Namespace: namespace}
			Expect(reconciler.configMapRequests(context.Background(), cm)).To(HaveLen(want), name)
		}
	})
})

// mergedRequest is a reconcile request for the namespace these specs use. The
// controller reconciles a whole namespace, so the name is not significant.
func mergedRequest() reconcile.Request {
	return reconcile.Request{Namespace: mergeNamespace, Name: "route-event"}
}

func reconcileMerged(reconciler *RouteReconciler) {
	_, err := reconciler.Reconcile(context.Background(), mergedRequest())
	Expect(err).NotTo(HaveOccurred())
}

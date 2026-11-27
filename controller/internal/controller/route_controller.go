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
	"errors"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gatewayv1alpha1 "gateway/controller/api/v1alpha1"
)

const (
	defaultConfigMapName = "gateway"
	defaultConfigMapKey  = "gateway.yaml"
	readyCondition       = "Ready"
	conflictCondition    = "Conflict"
)

type eventRecorder func(runtime.Object, string, string, string, ...any)

// RouteReconciler reconciles a Route object
type RouteReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder eventRecorder
	// ConfigMapName is the ConfigMap the controller writes and the gateway
	// mounts. With a base ConfigMap set, this is the merged output.
	ConfigMapName string
	ConfigMapKey  string
	// BaseConfigMapName, when set, names a ConfigMap owned by someone else (the
	// Helm chart) whose ConfigMapKey holds the base gateway config. The
	// controller only reads it, merges Route routes into it, and writes the
	// result to ConfigMapName. RenderOptions are not used in this mode.
	BaseConfigMapName string
	RenderOptions     RenderOptions
}

// +kubebuilder:rbac:groups=gateway.gateway.local,resources=routes,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.gateway.local,resources=routes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch;update

func (r *RouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var routeList gatewayv1alpha1.RouteList
	if err := r.List(ctx, &routeList, client.InNamespace(req.Namespace)); err != nil {
		return ctrl.Result{}, fmt.Errorf("list routes: %w", err)
	}
	var (
		rendered  []byte
		conflicts []Conflict
		owner     *corev1.ConfigMap
		err       error
	)
	if r.BaseConfigMapName == "" {
		rendered, conflicts, err = Render(routeList.Items, r.RenderOptions)
	} else {
		owner, err = r.getBase(ctx, req.Namespace)
		if owner == nil && err == nil {
			// No base here yet (first install, or a namespace without a
			// gateway). The base watch reconciles again once it exists.
			logf.FromContext(ctx).Info("base ConfigMap not found; not writing gateway config",
				"namespace", req.Namespace, "name", r.BaseConfigMapName)
			return ctrl.Result{}, nil
		}
		if err == nil {
			rendered, conflicts, err = Merge([]byte(owner.Data[r.configMapKey()]), routeList.Items,
				req.Namespace+"/"+r.BaseConfigMapName)
		}
	}
	if err != nil {
		r.recordRenderErrors(&routeList, err)
		return ctrl.Result{}, err
	}
	if err := r.writeConfigMap(ctx, req.Namespace, rendered, owner); err != nil {
		r.recordRenderErrors(&routeList, err)
		return ctrl.Result{}, err
	}
	if err := r.updateStatuses(ctx, routeList.Items, conflicts); err != nil {
		return ctrl.Result{}, fmt.Errorf("update route status: %w", err)
	}
	return ctrl.Result{}, nil
}

// getBase returns the base ConfigMap, or nil without an error when it does
// not exist.
func (r *RouteReconciler) getBase(ctx context.Context, namespace string) (*corev1.ConfigMap, error) {
	var base corev1.ConfigMap
	key := client.ObjectKey{Namespace: namespace, Name: r.BaseConfigMapName}
	if err := r.Get(ctx, key, &base); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get base ConfigMap %s: %w", key, err)
	}
	if _, ok := base.Data[r.configMapKey()]; !ok {
		return nil, fmt.Errorf("base ConfigMap %s has no key %q", key, r.configMapKey())
	}
	return &base, nil
}

func (r *RouteReconciler) configMapName() string {
	if r.ConfigMapName == "" {
		return defaultConfigMapName
	}
	return r.ConfigMapName
}

func (r *RouteReconciler) configMapKey() string {
	if r.ConfigMapKey == "" {
		return defaultConfigMapKey
	}
	return r.ConfigMapKey
}

// writeConfigMap writes rendered to the output ConfigMap. A non-nil owner (the
// base ConfigMap) becomes an owner reference, so deleting the base, for example
// with helm uninstall, garbage-collects the output.
func (r *RouteReconciler) writeConfigMap(
	ctx context.Context,
	namespace string,
	rendered []byte,
	owner *corev1.ConfigMap,
) error {
	name := r.configMapName()
	key := r.configMapKey()

	var cm corev1.ConfigMap
	keyObject := client.ObjectKey{Namespace: namespace, Name: name}
	err := r.Get(ctx, keyObject, &cm)
	switch {
	case apierrors.IsNotFound(err):
		cm = corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels:    map[string]string{"app.kubernetes.io/managed-by": "edge-gateway-controller"},
			},
			Data: map[string]string{key: string(rendered)},
		}
		if owner != nil {
			if err := controllerutil.SetOwnerReference(owner, &cm, r.Scheme); err != nil {
				return fmt.Errorf("set owner of ConfigMap %s: %w", keyObject, err)
			}
		}
		if err := r.Create(ctx, &cm); err != nil {
			return fmt.Errorf("create ConfigMap %s: %w", keyObject, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("get ConfigMap %s: %w", keyObject, err)
	}
	ownersBefore := len(cm.OwnerReferences)
	if owner != nil {
		if err := controllerutil.SetOwnerReference(owner, &cm, r.Scheme); err != nil {
			return fmt.Errorf("set owner of ConfigMap %s: %w", keyObject, err)
		}
	}
	ownerChanged := len(cm.OwnerReferences) != ownersBefore
	if cm.Data != nil && cm.Data[key] == string(rendered) && !ownerChanged {
		return nil
	}
	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data[key] = string(rendered)
	if err := r.Update(ctx, &cm); err != nil {
		return fmt.Errorf("update ConfigMap %s: %w", keyObject, err)
	}
	return nil
}

func (r *RouteReconciler) updateStatuses(
	ctx context.Context,
	routes []gatewayv1alpha1.Route,
	conflicts []Conflict,
) error {
	conflictByRoute := make(map[string]Conflict, len(conflicts))
	for _, conflict := range conflicts {
		conflictByRoute[conflict.Route] = conflict
	}
	var statusErrors []error
	for i := range routes {
		route := &routes[i]
		before := route.Status
		before.Conditions = append([]metav1.Condition(nil), route.Status.Conditions...)
		route.Status.ObservedGeneration = route.Generation
		key := routeKey(*route)
		conflict, conflicted := conflictByRoute[key]
		if conflicted {
			meta.SetStatusCondition(&route.Status.Conditions, metav1.Condition{
				Type:               conflictCondition,
				Status:             metav1.ConditionTrue,
				ObservedGeneration: route.Generation,
				Reason:             "PrefixConflict",
				Message:            fmt.Sprintf("prefix %q is owned by %s", conflict.Prefix, conflict.Winner),
			})
			meta.SetStatusCondition(&route.Status.Conditions, metav1.Condition{
				Type:               readyCondition,
				Status:             metav1.ConditionFalse,
				ObservedGeneration: route.Generation,
				Reason:             "PrefixConflict",
				Message:            fmt.Sprintf("prefix %q conflicts with %s", conflict.Prefix, conflict.Winner),
			})
		} else {
			meta.SetStatusCondition(&route.Status.Conditions, metav1.Condition{
				Type:               conflictCondition,
				Status:             metav1.ConditionFalse,
				ObservedGeneration: route.Generation,
				Reason:             "UniquePrefix",
				Message:            "route prefix is unique",
			})
			meta.SetStatusCondition(&route.Status.Conditions, metav1.Condition{
				Type:               readyCondition,
				Status:             metav1.ConditionTrue,
				ObservedGeneration: route.Generation,
				Reason:             "Rendered",
				Message:            "route is present in the gateway configuration",
			})
		}
		if reflect.DeepEqual(before, route.Status) {
			continue
		}
		wasConflict := meta.IsStatusConditionTrue(before.Conditions, conflictCondition)
		if err := r.Status().Update(ctx, route); err != nil {
			statusErrors = append(statusErrors, fmt.Errorf("%s: %w", key, err))
			continue
		}
		if conflicted && !wasConflict && r.Recorder != nil {
			r.Recorder(route, corev1.EventTypeWarning, "RouteConflict",
				"prefix %q is already owned by %s", conflict.Prefix, conflict.Winner)
		}
	}
	return errors.Join(statusErrors...)
}

func (r *RouteReconciler) recordRenderErrors(routes *gatewayv1alpha1.RouteList, renderErr error) {
	if r.Recorder == nil {
		return
	}
	for i := range routes.Items {
		r.Recorder(&routes.Items[i], corev1.EventTypeWarning, "RenderFailed",
			"failed to render gateway ConfigMap: %v", renderErr)
	}
}

// configMapRequests maps events on the base or output ConfigMap to a
// reconcile of their namespace, so a helm upgrade that re-renders the base, or
// an edit to the output, is reconciled without waiting for a Route change.
func (r *RouteReconciler) configMapRequests(_ context.Context, obj client.Object) []reconcile.Request {
	if r.BaseConfigMapName == "" {
		return nil
	}
	if name := obj.GetName(); name != r.BaseConfigMapName && name != r.configMapName() {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(obj)}}
}

// SetupWithManager sets up the controller with the Manager.
func (r *RouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1alpha1.Route{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("route")
	if r.BaseConfigMapName != "" {
		b = b.Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.configMapRequests))
	}
	return b.Complete(r)
}

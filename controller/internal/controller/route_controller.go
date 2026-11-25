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
	"sigs.k8s.io/controller-runtime/pkg/predicate"

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
	Scheme        *runtime.Scheme
	Recorder      eventRecorder
	ConfigMapName string
	ConfigMapKey  string
	RenderOptions RenderOptions
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
	rendered, conflicts, err := Render(routeList.Items, r.RenderOptions)
	if err != nil {
		r.recordRenderErrors(&routeList, err)
		return ctrl.Result{}, err
	}
	if err := r.writeConfigMap(ctx, req.Namespace, rendered); err != nil {
		r.recordRenderErrors(&routeList, err)
		return ctrl.Result{}, err
	}
	if err := r.updateStatuses(ctx, routeList.Items, conflicts); err != nil {
		return ctrl.Result{}, fmt.Errorf("update route status: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *RouteReconciler) writeConfigMap(ctx context.Context, namespace string, rendered []byte) error {
	name := r.ConfigMapName
	if name == "" {
		name = defaultConfigMapName
	}
	key := r.ConfigMapKey
	if key == "" {
		key = defaultConfigMapKey
	}

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
		if err := r.Create(ctx, &cm); err != nil {
			return fmt.Errorf("create ConfigMap %s: %w", keyObject, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("get ConfigMap %s: %w", keyObject, err)
	}
	if cm.Data != nil && cm.Data[key] == string(rendered) {
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

// SetupWithManager sets up the controller with the Manager.
func (r *RouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1alpha1.Route{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("route").
		Complete(r)
}

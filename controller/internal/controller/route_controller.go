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
	gatewayconfig "gateway/pkg/config"
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
	var owner *corev1.ConfigMap
	if r.BaseConfigMapName != "" {
		var err error
		owner, err = r.getBase(ctx, req.Namespace)
		if err != nil {
			r.recordRenderErrors(&routeList, err)
			return ctrl.Result{}, err
		}
		if owner == nil {
			// No base here yet (first install, or a namespace without a
			// gateway). The base watch reconciles again once it exists.
			logf.FromContext(ctx).Info("base ConfigMap not found; not writing gateway config",
				"namespace", req.Namespace, "name", r.BaseConfigMapName)
			return ctrl.Result{}, nil
		}
	}
	build := func(routes []gatewayv1alpha1.Route) ([]byte, []Conflict, error) {
		if owner == nil {
			return Render(routes, r.RenderOptions)
		}
		return Merge([]byte(owner.Data[r.configMapKey()]), routes, req.Namespace+"/"+r.BaseConfigMapName)
	}

	rendered, conflicts, invalid, err := buildAccepted(routeList.Items, build)
	if err != nil {
		r.recordRenderErrors(&routeList, err)
		if baseErr, ok := errors.AsType[*invalidBaseError](err); ok {
			if statusErr := r.updateStatuses(ctx, routeList.Items, nil, nil, baseErr); statusErr != nil {
				err = errors.Join(err, fmt.Errorf("update route status: %w", statusErr))
			}
		}
		return ctrl.Result{}, err
	}
	if err := r.writeConfigMap(ctx, req.Namespace, rendered, owner); err != nil {
		r.recordRenderErrors(&routeList, err)
		return ctrl.Result{}, err
	}
	if err := r.updateStatuses(ctx, routeList.Items, conflicts, invalid, nil); err != nil {
		return ctrl.Result{}, fmt.Errorf("update route status: %w", err)
	}
	return ctrl.Result{}, nil
}

// invalidBaseError means the gateway rejects the config even without any
// Route, so the fault is in the base (or the render options), not a Route.
type invalidBaseError struct{ err error }

func (e *invalidBaseError) Error() string { return "gateway rejects the base config: " + e.err.Error() }
func (e *invalidBaseError) Unwrap() error { return e.err }

// buildAccepted builds the gateway config and checks it with the gateway's own
// rules, so Ready means the gateway will load it. When the full config is
// rejected, each Route is checked on its own on top of the base; the ones the
// gateway rejects are left out and returned in invalid (Route key -> reason).
func buildAccepted(
	routes []gatewayv1alpha1.Route,
	build func([]gatewayv1alpha1.Route) ([]byte, []Conflict, error),
) ([]byte, []Conflict, map[string]string, error) {
	rendered, conflicts, err := build(routes)
	if err != nil {
		return nil, nil, nil, err
	}
	if _, err := gatewayconfig.Parse(rendered); err == nil {
		return rendered, conflicts, nil, nil
	}

	empty, _, err := build(nil)
	if err != nil {
		return nil, nil, nil, err
	}
	if _, err := gatewayconfig.Parse(empty); err != nil {
		return nil, nil, nil, &invalidBaseError{err: err}
	}
	invalid := make(map[string]string)
	accepted := make([]gatewayv1alpha1.Route, 0, len(routes))
	for _, route := range routes {
		single, _, err := build([]gatewayv1alpha1.Route{route})
		if err != nil {
			return nil, nil, nil, err
		}
		if _, err := gatewayconfig.Parse(single); err != nil {
			invalid[routeKey(route)] = err.Error()
			continue
		}
		accepted = append(accepted, route)
	}
	rendered, conflicts, err = build(accepted)
	if err != nil {
		return nil, nil, nil, err
	}
	if _, err := gatewayconfig.Parse(rendered); err != nil {
		return nil, nil, nil, fmt.Errorf("gateway rejects the combined config: %w", err)
	}
	return rendered, conflicts, invalid, nil
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
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "edge-gateway-controller"},
			Data:      map[string]string{key: string(rendered)},
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
	invalid map[string]string,
	baseErr error,
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
		setCondition := func(conditionType string, status metav1.ConditionStatus, reason, message string) {
			meta.SetStatusCondition(&route.Status.Conditions, metav1.Condition{
				Type:               conditionType,
				Status:             status,
				ObservedGeneration: route.Generation,
				Reason:             reason,
				Message:            message,
			})
		}
		conflict, conflicted := conflictByRoute[key]
		reason, isInvalid := invalid[key]
		switch {
		case baseErr != nil:
			// Nothing was written, so no Route is live in an accepted config.
			// The Conflict condition is left as it was.
			setCondition(readyCondition, metav1.ConditionFalse, "InvalidBaseConfig", baseErr.Error())
		case conflicted:
			setCondition(conflictCondition, metav1.ConditionTrue, "PrefixConflict",
				fmt.Sprintf("prefix %q is owned by %s", conflict.Prefix, conflict.Winner))
			setCondition(readyCondition, metav1.ConditionFalse, "PrefixConflict",
				fmt.Sprintf("prefix %q conflicts with %s", conflict.Prefix, conflict.Winner))
		case isInvalid:
			setCondition(conflictCondition, metav1.ConditionFalse, "UniquePrefix", "route prefix is unique")
			setCondition(readyCondition, metav1.ConditionFalse, "InvalidConfig",
				"the gateway rejects this route: "+reason)
		default:
			setCondition(conflictCondition, metav1.ConditionFalse, "UniquePrefix", "route prefix is unique")
			setCondition(readyCondition, metav1.ConditionTrue, "Rendered",
				"route is present in the gateway configuration")
		}
		if reflect.DeepEqual(before, route.Status) {
			continue
		}
		wasConflict := meta.IsStatusConditionTrue(before.Conditions, conflictCondition)
		wasReason := ""
		if ready := meta.FindStatusCondition(before.Conditions, readyCondition); ready != nil {
			wasReason = ready.Reason
		}
		if err := r.Status().Update(ctx, route); err != nil {
			statusErrors = append(statusErrors, fmt.Errorf("%s: %w", key, err))
			continue
		}
		if r.Recorder == nil {
			continue
		}
		if conflicted && baseErr == nil && !wasConflict {
			r.Recorder(route, corev1.EventTypeWarning, "RouteConflict",
				"prefix %q is already owned by %s", conflict.Prefix, conflict.Winner)
		}
		if isInvalid && baseErr == nil && !conflicted && wasReason != "InvalidConfig" {
			r.Recorder(route, corev1.EventTypeWarning, "RouteInvalid",
				"the gateway rejects this route: %s", reason)
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

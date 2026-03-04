package controllers

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"k8s-vault-webhook/provider"
)

const (
	AnnotationRotationInterval = "vault.opstree.secret.manager/rotation-interval"
	AnnotationInjectedVersion  = "vault.opstree.secret.manager/injected-version"
)

// SecretRotationReconciler reconciles a Pod object
type SecretRotationReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	ProviderRegistry *provider.Registry
}

// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;update;patch

// Reconcile checks if a pod needs to be rotated because underlying secrets have changed
func (r *SecretRotationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	intervalStr := pod.Annotations[AnnotationRotationInterval]
	if intervalStr == "" {
		return ctrl.Result{}, nil
	}

	interval, err := time.ParseDuration(intervalStr)
	if err != nil {
		logger.Error(err, "invalid rotation-interval duration format", "annotation_value", intervalStr)
		return ctrl.Result{}, nil // don't requeue just for bad format
	}

	injectedVersion := pod.Annotations[AnnotationInjectedVersion]
	if injectedVersion == "" {
		// The pod was not injected by the webhook, or we failed to set the version.
		return ctrl.Result{RequeueAfter: interval}, nil
	}

	providers := r.ProviderRegistry.BuildEnabledProviders(pod.Annotations)
	if len(providers) == 0 {
		return ctrl.Result{}, nil
	}

	for _, p := range providers {
		currentVersion, err := p.GetCurrentVersion(ctx, pod.Annotations)
		if err != nil {
			logger.Error(err, "failed to fetch current secret version", "provider", p.Name())
			// Requeue backoff behavior
			return ctrl.Result{RequeueAfter: interval}, err
		}

		if currentVersion != "" && currentVersion != injectedVersion {
			logger.Info("Secret version mismatch detected -> initiating rotation restart",
				"provider", p.Name(),
				"injected", injectedVersion,
				"current", currentVersion)

			if err := r.triggerRollingRestart(ctx, &pod); err != nil {
				logger.Error(err, "failed to trigger rolling restart")
				return ctrl.Result{RequeueAfter: interval}, err
			}
			logger.Info("Successfully triggered rolling restart for pod owner")
			// We skip the remaining interval loops because the pod will be replaced.
			return ctrl.Result{}, nil
		}
	}

	// Normal wait queue
	return ctrl.Result{RequeueAfter: interval}, nil
}

func (r *SecretRotationReconciler) triggerRollingRestart(ctx context.Context, pod *corev1.Pod) error {
	if len(pod.OwnerReferences) == 0 {
		return nil
	}
	owner := pod.OwnerReferences[0]

	restartJSON := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"%s"}}}}}`, time.Now().Format(time.RFC3339))
	patch := client.RawPatch(types.StrategicMergePatchType, []byte(restartJSON))

	var obj client.Object
	switch owner.Kind {
	case "ReplicaSet":
		var rs appsv1.ReplicaSet
		if err := r.Get(ctx, types.NamespacedName{Name: owner.Name, Namespace: pod.Namespace}, &rs); err != nil {
			return err
		}
		if len(rs.OwnerReferences) > 0 && rs.OwnerReferences[0].Kind == "Deployment" {
			deployName := rs.OwnerReferences[0].Name
			obj = &appsv1.Deployment{}
			obj.SetName(deployName)
			obj.SetNamespace(pod.Namespace)
		} else {
			return nil
		}
	case "StatefulSet":
		obj = &appsv1.StatefulSet{}
		obj.SetName(owner.Name)
		obj.SetNamespace(pod.Namespace)
	case "DaemonSet":
		obj = &appsv1.DaemonSet{}
		obj.SetName(owner.Name)
		obj.SetNamespace(pod.Namespace)
	case "Deployment":
		obj = &appsv1.Deployment{}
		obj.SetName(owner.Name)
		obj.SetNamespace(pod.Namespace)
	default:
		return nil
	}

	return r.Patch(ctx, obj, patch)
}

func (r *SecretRotationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Complete(r)
}

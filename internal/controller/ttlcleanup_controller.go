/*
Copyright 2026.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	fleetguardv1alpha1 "github.com/franklynux/fleetguard-operator/api/v1alpha1"
)

// fallbackRequeue is used when a TTLCleanup currently has no matching
// objects, so we still periodically re-list in case new objects appear.
const fallbackRequeue = time.Hour

// TTLCleanupReconciler reconciles a TTLCleanup object.
type TTLCleanupReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=fleetguard.io,resources=ttlcleanups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=fleetguard.io,resources=ttlcleanups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=fleetguard.io,resources=ttlcleanups/finalizers,verbs=update
// +kubebuilder:rbac:groups="*",resources="*",verbs=get;list;watch;delete

func (r *TTLCleanupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var ttlCleanup fleetguardv1alpha1.TTLCleanup
	if err := r.Get(ctx, req.NamespacedName, &ttlCleanup); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching TTLCleanup: %w", err)
	}

	targetRef := ttlCleanup.Spec.TargetRef
	ttl := ttlCleanup.Spec.TTL.Duration

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.FromAPIVersionAndKind(targetRef.APIVersion, targetRef.Kind+"List"))

	listOpts := []client.ListOption{}
	if targetRef.Namespace != "" {
		listOpts = append(listOpts, client.InNamespace(targetRef.Namespace))
	}
	if targetRef.LabelSelector != nil {
		selector, err := metav1.LabelSelectorAsSelector(targetRef.LabelSelector)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("invalid labelSelector: %w", err)
		}
		listOpts = append(listOpts, client.MatchingLabelsSelector{Selector: selector})
	}

	if err := r.List(ctx, list, listOpts...); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing target objects (%s %s): %w", targetRef.APIVersion, targetRef.Kind, err)
	}

	now := time.Now()
	var (
		matched       = int32(len(list.Items))
		deleted       = int32(0)
		soonestRemain = fallbackRequeue
		haveUnexpired = false
	)

	for i := range list.Items {
		obj := &list.Items[i]
		age := now.Sub(obj.GetCreationTimestamp().Time)
		remaining := ttl - age

		if remaining > 0 {
			haveUnexpired = true
			if remaining < soonestRemain {
				soonestRemain = remaining
			}
			continue
		}

		// Expired.
		if ttlCleanup.Spec.DryRun {
			r.Recorder.Eventf(&ttlCleanup, corev1.EventTypeNormal, "WouldDelete",
				"would delete %s/%s (age %s, ttl %s)", obj.GetKind(), obj.GetName(), age.Round(time.Second), ttl)
			continue
		}

		if err := r.Delete(ctx, obj); err != nil {
			if apierrors.IsNotFound(err) {
				// Already gone — not an error.
				continue
			}
			logger.Error(err, "failed to delete expired object", "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace())
			r.Recorder.Eventf(&ttlCleanup, corev1.EventTypeWarning, "DeleteFailed",
				"failed to delete %s/%s: %v", obj.GetKind(), obj.GetName(), err)
			continue
		}

		deleted++
		r.Recorder.Eventf(&ttlCleanup, corev1.EventTypeNormal, "Deleted",
			"deleted %s/%s (age %s, ttl %s)", obj.GetKind(), obj.GetName(), age.Round(time.Second), ttl)
	}

	nowMeta := metav1.NewTime(now)
	ttlCleanup.Status.LastCleanupTime = &nowMeta
	ttlCleanup.Status.ObjectsMatched = matched
	ttlCleanup.Status.ObjectsDeleted = deleted

	if err := r.Status().Update(ctx, &ttlCleanup); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating TTLCleanup status: %w", err)
	}

	requeueAfter := fallbackRequeue
	if haveUnexpired {
		requeueAfter = soonestRemain
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TTLCleanupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorderFor("ttlcleanup-controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&fleetguardv1alpha1.TTLCleanup{}).
		Complete(r)
}

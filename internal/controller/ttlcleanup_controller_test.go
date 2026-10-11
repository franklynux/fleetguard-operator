/*
Copyright 2026.
*/

package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	fleetguardv1alpha1 "github.com/franklynux/fleetguard-operator/api/v1alpha1"
)

// selectorLabelKey is the label key every test's Jobs and TTLCleanup
// LabelSelectors agree on. The VALUE is unique per test (see
// newTestJob/newTestTTLCleanup below), so one test's objects can never be
// matched by another test's selector even if cleanup from an earlier spec
// hasn't fully landed yet — tests stay isolated by construction, not by
// hoping cleanup timing works out.
const selectorLabelKey = "ttl-cleanup"

// newTestJob builds a minimal Job carrying the label
// {selectorLabelKey: labelValue}. envtest only runs the API server + etcd
// (no kubelet, no Job controller), so this Job never actually executes —
// it just needs to exist as an object our reconciler can list and delete.
func newTestJob(name, namespace, labelValue string) *batchv1.Job {
	backoffLimit := int32(0)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{selectorLabelKey: labelValue},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "sleep",
							Image:   "busybox:1.36",
							Command: []string{"sh", "-c", "echo done"},
						},
					},
				},
			},
		},
	}
}

// newTestTTLCleanup builds a TTLCleanup targeting Jobs labeled
// {selectorLabelKey: labelValue} in namespace, with the given ttl and
// dryRun. Pass the TTLCleanup's own name as labelValue so each test's
// TTLCleanup only ever matches the Job(s) that same test created.
func newTestTTLCleanup(name, namespace, labelValue string, ttl time.Duration, dryRun bool) *fleetguardv1alpha1.TTLCleanup {
	return &fleetguardv1alpha1.TTLCleanup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: fleetguardv1alpha1.TTLCleanupSpec{
			TargetRef: fleetguardv1alpha1.TargetReference{
				APIVersion: "batch/v1",
				Kind:       "Job",
				Namespace:  namespace,
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{selectorLabelKey: labelValue},
				},
			},
			TTL:    metav1.Duration{Duration: ttl},
			DryRun: dryRun,
		},
	}
}

var _ = Describe("TTLCleanup controller", func() {
	const namespace = "default"

	var (
		reconciler *TTLCleanupReconciler
		recorder   *record.FakeRecorder
	)

	BeforeEach(func() {
		recorder = record.NewFakeRecorder(10)
		reconciler = &TTLCleanupReconciler{
			Client:   k8sClient,
			Scheme:   scheme.Scheme,
			Recorder: recorder,
		}
	})

	Context("when the TTLCleanup resource no longer exists", func() {
		It("returns without error and without requeueing", func() {
			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "does-not-exist", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})
	})

	Context("when a matched object has not yet expired", func() {
		It("does not delete it and requeues before the TTL elapses", func() {
			const name = "unexpired"
			job := newTestJob(name+"-job", namespace, name)
			Expect(k8sClient.Create(ctx, job)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, job)).To(Succeed())
			})

			ttlCleanup := newTestTTLCleanup(name+"-ttlcleanup", namespace, name, time.Hour, false)
			Expect(k8sClient.Create(ctx, ttlCleanup)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, ttlCleanup)).To(Succeed())
			})

			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: ttlCleanup.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))
			Expect(result.RequeueAfter).To(BeNumerically("<=", time.Hour))

			By("the Job still existing")
			fetched := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: namespace}, fetched)).To(Succeed())

			By("status reflecting a match with no deletion")
			updated := &fleetguardv1alpha1.TTLCleanup{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ttlCleanup.Name, Namespace: namespace}, updated)).To(Succeed())
			Expect(updated.Status.ObjectsMatched).To(Equal(int32(1)))
			Expect(updated.Status.ObjectsDeleted).To(Equal(int32(0)))
		})
	})

	// NOTE: metav1.Time (and so .status.creationTimestamp as returned by the
	// API server) only has second-level precision over the wire. A TTL
	// shorter than ~1s is unreliable to test against because of that
	// truncation, so these "expired" cases use a short-but-second-scale TTL
	// and sleep past it rather than racing sub-second timing.

	Context("when a matched object has expired and DryRun is true", func() {
		It("records a WouldDelete event and leaves the object in place", func() {
			const name = "dryrun-expired"
			job := newTestJob(name+"-job", namespace, name)
			Expect(k8sClient.Create(ctx, job)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, job)).To(Succeed())
			})

			ttlCleanup := newTestTTLCleanup(name+"-ttlcleanup", namespace, name, time.Second, true)
			Expect(k8sClient.Create(ctx, ttlCleanup)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, ttlCleanup)).To(Succeed())
			})

			time.Sleep(2 * time.Second)

			_, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: ttlCleanup.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("the Job still existing")
			fetched := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: namespace}, fetched)).To(Succeed())

			By("status reflecting a match with no deletion")
			updated := &fleetguardv1alpha1.TTLCleanup{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ttlCleanup.Name, Namespace: namespace}, updated)).To(Succeed())
			Expect(updated.Status.ObjectsMatched).To(Equal(int32(1)))
			Expect(updated.Status.ObjectsDeleted).To(Equal(int32(0)))

			By("a WouldDelete event being recorded")
			Expect(recorder.Events).To(Receive(ContainSubstring("WouldDelete")))
		})
	})

	Context("when a matched object has expired and DryRun is false", func() {
		It("deletes the object and records a Deleted event", func() {
			const name = "delete-expired"
			job := newTestJob(name+"-job", namespace, name)
			Expect(k8sClient.Create(ctx, job)).To(Succeed())
			// No cleanup registered for the Job itself: the whole point of
			// this test is that the reconciler deletes it.

			ttlCleanup := newTestTTLCleanup(name+"-ttlcleanup", namespace, name, time.Second, false)
			Expect(k8sClient.Create(ctx, ttlCleanup)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, ttlCleanup)).To(Succeed())
			})

			time.Sleep(2 * time.Second)

			_, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: ttlCleanup.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("the Job's deletion being initiated by the reconciler")
			// Kubernetes stamps every Job with a batch.kubernetes.io/job-tracking
			// finalizer at creation time (stable since 1.26). That finalizer is
			// only cleared by the real Job controller, which lives in
			// kube-controller-manager — a component envtest does not run (it's
			// API server + etcd only, no controller-manager/scheduler/kubelet).
			// So a deleted Job in envtest gets deletionTimestamp set but can
			// never fully disappear here, unlike on a real cluster (confirmed
			// manually against kind earlier). What our reconciler is
			// responsible for is calling Delete successfully — full removal
			// afterward is Kubernetes' own GC concern — so accept either full
			// removal or a deletionTimestamp having been set as evidence the
			// delete took effect.
			Eventually(func() bool {
				fetched := &batchv1.Job{}
				getErr := k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: namespace}, fetched)
				if apierrors.IsNotFound(getErr) {
					return true
				}
				return getErr == nil && fetched.DeletionTimestamp != nil
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue(), "expected the Job to be deleted or marked for deletion")

			By("status reflecting the deletion")
			updated := &fleetguardv1alpha1.TTLCleanup{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ttlCleanup.Name, Namespace: namespace}, updated)).To(Succeed())
			Expect(updated.Status.ObjectsMatched).To(Equal(int32(1)))
			Expect(updated.Status.ObjectsDeleted).To(Equal(int32(1)))

			By("a Deleted event being recorded")
			Expect(recorder.Events).To(Receive(ContainSubstring("Deleted")))
		})
	})

	Context("when no objects match the selector", func() {
		It("reports zero matches and requeues using the fallback interval", func() {
			const name = "no-match"
			ttlCleanup := newTestTTLCleanup(name+"-ttlcleanup", namespace, name, time.Hour, false)
			Expect(k8sClient.Create(ctx, ttlCleanup)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, ttlCleanup)).To(Succeed())
			})

			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: ttlCleanup.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(fallbackRequeue))

			updated := &fleetguardv1alpha1.TTLCleanup{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ttlCleanup.Name, Namespace: namespace}, updated)).To(Succeed())
			Expect(updated.Status.ObjectsMatched).To(Equal(int32(0)))
			Expect(updated.Status.ObjectsDeleted).To(Equal(int32(0)))
		})
	})
})

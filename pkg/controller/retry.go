/*
Copyright 2026 The kubepkg Authors.

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
	"fmt"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kuberoot-dev/kubepkg/api/v1"
)

const (
	// DefaultRetryBackoff is the wait before a failed change is tried
	// again for the first time.
	DefaultRetryBackoff = 30 * time.Second
	// DefaultRetryBackoffMax caps the wait, however often it failed.
	DefaultRetryBackoffMax = 10 * time.Minute

	// AnnotationAttempt on a revision counts the tries at its desired
	// state: 1 for the first, one more for each try after a failure.
	AnnotationAttempt = "kubepkg.dev/attempt"
	// AnnotationFailedAt on a failed revision records when it failed, the
	// time the wait before the next try starts from.
	AnnotationFailedAt = "kubepkg.dev/failed-at"
	// ReasonInvalidConfiguration: the desired state could not be worked
	// out, e.g. a chart could not be fetched or components form a cycle.
	ReasonInvalidConfiguration = "InvalidConfiguration"
)

func (r *PackageReconciler) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// backoff is the wait after the given failed attempt: the base, doubled
// for every attempt before it, at most the maximum.
func (r *PackageReconciler) backoff(attempt int) time.Duration {
	base, ceiling := r.RetryBackoff, r.RetryBackoffMax
	if base <= 0 {
		base = DefaultRetryBackoff
	}
	if ceiling <= 0 {
		ceiling = DefaultRetryBackoffMax
	}
	wait := base
	for i := 1; i < attempt && wait < ceiling; i++ {
		wait *= 2
	}
	return min(wait, ceiling)
}

// scheduleRetry records in the Package status when the failed revision is
// tried again and returns how long that is from now; zero or less means
// it is due.
func (r *PackageReconciler) scheduleRetry(pkg *v1.Package, failed *v1.PackageRevision) time.Duration {
	attempt := attemptOf(failed)
	next := failedAt(failed).Add(r.backoff(attempt))
	pkg.Status.Retry = &v1.RetryStatus{Attempts: int32(attempt), NextAttempt: &metav1.Time{Time: next.UTC().Truncate(time.Second)}}
	return next.Sub(r.clock())
}

// failedAttempt is the newest failed try at the desired state with the
// given digest, or nil when the latest revisions were made for another one.
func failedAttempt(revs []v1.PackageRevision, digest string) *v1.PackageRevision {
	for i := len(revs) - 1; i >= 0; i-- {
		rev := &revs[i]
		if rev.Annotations[AnnotationDesiredDigest] != digest {
			return nil
		}
		if rev.Status.Phase == v1.PhaseFailed {
			return rev
		}
	}
	return nil
}

// attemptOf reads a revision's attempt number; revisions written before
// it was recorded count as the first.
func attemptOf(rev *v1.PackageRevision) int {
	n, err := strconv.Atoi(rev.Annotations[AnnotationAttempt])
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// failedAt is when a revision failed; revisions written before that was
// recorded fall back to when they were made.
func failedAt(rev *v1.PackageRevision) time.Time {
	if t, err := time.Parse(time.RFC3339, rev.Annotations[AnnotationFailedAt]); err == nil {
		return t
	}
	return rev.CreationTimestamp.Time
}

// preparationRetry is when to look again at a package whose desired state
// could not be worked out, typically because a chart could not be
// fetched: no revision records those tries, so the wait grows with how
// long the package has been failing, from the backoff up to its maximum.
func (r *PackageReconciler) preparationRetry(pkg *v1.Package) time.Duration {
	c := meta.FindStatusCondition(pkg.Status.Conditions, "Ready")
	if c == nil || c.Reason != ReasonInvalidConfiguration {
		return 0
	}
	base, ceiling := r.backoff(1), r.backoff(1<<20)
	return min(max(r.clock().Sub(c.LastTransitionTime.Time), base), ceiling)
}

// retryNote tells how a failed change is tried again.
func retryNote(pkg *v1.Package) string {
	rt := pkg.Status.Retry
	if rt == nil || rt.NextAttempt == nil {
		return "; change the Package or set " + AnnotationRetry + " to try again"
	}
	return fmt.Sprintf("; failed %d time(s), trying again at %s, or set %s to try now",
		rt.Attempts, rt.NextAttempt.UTC().Format(time.RFC3339), AnnotationRetry)
}

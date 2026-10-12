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
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kuberoot-dev/kubepkg/api/v1"
)

// clockAt makes the reconciler's clock a variable the test moves.
func (e *env) clockAt(t time.Time) *time.Time {
	now := t
	e.r.now = func() time.Time { return now }
	return &now
}

func TestBackoffDoublesUpToTheCap(t *testing.T) {
	r := &PackageReconciler{}
	for attempt, want := range map[int]time.Duration{
		1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 5: 8 * time.Minute, 6: 10 * time.Minute, 1000: 10 * time.Minute,
	} {
		if got := r.backoff(attempt); got != want {
			t.Errorf("backoff(%d) = %s, want %s", attempt, got, want)
		}
	}
	r = &PackageReconciler{RetryBackoff: time.Second, RetryBackoffMax: 5 * time.Second}
	if got := r.backoff(10); got != 5*time.Second {
		t.Errorf("custom cap: %s", got)
	}
}

func TestFailedInstallIsRetriedWithBackoff(t *testing.T) {
	e := newEnv(t)
	now := e.clockAt(time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC))
	e.be.failOn["app"] = true
	e.create(mkSource("app", "1.0.0", true, "app"), &v1.Package{ObjectMeta: metav1.ObjectMeta{Name: "app"}})

	res := e.reconcile("app")
	if res.RequeueAfter != 30*time.Second {
		t.Fatalf("first retry in %s, want 30s", res.RequeueAfter)
	}
	p := e.pkg("app")
	if p.Status.Retry == nil || p.Status.Retry.Attempts != 1 || !p.Status.Retry.NextAttempt.Time.Equal(now.Add(30*time.Second)) {
		t.Fatalf("retry status = %+v", p.Status.Retry)
	}
	if _, reason, msg := ready(p); reason != v1.ReasonUpgradeFailed || !strings.Contains(msg, "trying again at 2026-10-12T10:00:30Z") {
		t.Fatalf("condition = %s %s", reason, msg)
	}

	// Before the wait is over nothing is applied, however often the
	// package is reconciled.
	*now = now.Add(10 * time.Second)
	calls := len(e.be.calls)
	if res := e.reconcile("app"); res.RequeueAfter != 20*time.Second {
		t.Fatalf("requeue after %s, want the rest of the wait", res.RequeueAfter)
	}
	if len(e.be.calls) != calls {
		t.Fatalf("applied before the wait was over: %v", e.be.calls[calls:])
	}

	// The second failure waits twice as long.
	*now = now.Add(20 * time.Second)
	if res := e.reconcile("app"); res.RequeueAfter != time.Minute {
		t.Fatalf("second retry in %s, want 1m", res.RequeueAfter)
	}
	if got := e.revs("app"); got != "1:1.0.0:Failed 2:1.0.0:Failed" {
		t.Fatalf("revisions = %s", got)
	}
	if a := e.pkg("app").Status.Retry.Attempts; a != 2 {
		t.Fatalf("attempts = %d", a)
	}

	// Once the cause is gone, the next try succeeds and the retry status
	// goes away.
	e.be.failOn["app"] = false
	*now = now.Add(time.Minute)
	e.reconcile("app")
	p = e.pkg("app")
	if ok, reason, msg := ready(p); !ok {
		t.Fatalf("not ready after the retry: %s %s", reason, msg)
	}
	if p.Status.Retry != nil {
		t.Fatalf("retry status left: %+v", p.Status.Retry)
	}
	rev := &v1.PackageRevision{}
	if err := e.c.Get(context.Background(), types.NamespacedName{Name: "app-3"}, rev); err != nil {
		t.Fatal(err)
	}
	if rev.Annotations[AnnotationAttempt] != "3" {
		t.Fatalf("attempt of revision 3 = %q", rev.Annotations[AnnotationAttempt])
	}
}

func TestFailedUpgradeRolledBackIsRetried(t *testing.T) {
	e := newEnv(t)
	now := e.clockAt(time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC))
	e.create(mkSource("app", "1.0.0", true, "api", "web"), &v1.Package{ObjectMeta: metav1.ObjectMeta{Name: "app"}})
	e.reconcile("app")
	e.setVersion("app", "1.1.0", true)
	e.be.failOn["web"] = true
	if res := e.reconcile("app"); res.RequeueAfter != 30*time.Second {
		t.Fatalf("retry in %s", res.RequeueAfter)
	}
	if _, reason, _ := ready(e.pkg("app")); reason != v1.ReasonUpgradeRolledBack {
		t.Fatalf("reason = %s", reason)
	}
	e.be.failOn["web"] = false
	*now = now.Add(31 * time.Second)
	e.reconcile("app")
	if ok, reason, msg := ready(e.pkg("app")); !ok {
		t.Fatalf("retry did not succeed: %s %s", reason, msg)
	}
	if got := e.revs("app"); got != "1:1.0.0:Superseded 2:1.1.0:Failed 3:1.0.0:Superseded<-1 4:1.1.0:Applied" {
		t.Fatalf("revisions = %s", got)
	}
}

func TestANewBuildIsTriedAtOnce(t *testing.T) {
	e := newEnv(t)
	e.clockAt(time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC))
	e.be.failOn["app"] = true
	e.create(mkSource("app", "1.0.0", true, "app"), &v1.Package{ObjectMeta: metav1.ObjectMeta{Name: "app"}})
	e.reconcile("app")

	// A fixed build of the same version changes the desired state: no
	// waiting for the retry.
	e.be.failOn["app"] = false
	src := &v1.PackageSource{}
	if err := e.c.Get(context.Background(), types.NamespacedName{Name: "app"}, src); err != nil {
		t.Fatal(err)
	}
	src.Spec.Build = 2
	if err := e.c.Update(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	e.reconcile("app")
	if ok, reason, msg := ready(e.pkg("app")); !ok {
		t.Fatalf("new build not applied at once: %s %s", reason, msg)
	}
	rev := &v1.PackageRevision{}
	if err := e.c.Get(context.Background(), types.NamespacedName{Name: "app-2"}, rev); err != nil {
		t.Fatal(err)
	}
	if rev.Annotations[AnnotationAttempt] != "1" {
		t.Fatalf("a new desired state starts counting again, got attempt %q", rev.Annotations[AnnotationAttempt])
	}
}

func TestRetriesKeepTheHistoryBounded(t *testing.T) {
	e := newEnv(t)
	now := e.clockAt(time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC))
	limit := int32(3)
	e.create(mkSource("app", "1.0.0", true, "app"), &v1.Package{ObjectMeta: metav1.ObjectMeta{Name: "app"}, Spec: v1.PackageSpec{RevisionHistoryLimit: &limit}})
	e.reconcile("app")
	e.setVersion("app", "1.1.0", true)
	e.be.failOn["app"] = true
	for range 10 {
		e.reconcile("app")
		*now = now.Add(time.Hour)
	}
	var list v1.PackageRevisionList
	if err := e.c.List(context.Background(), &list, client.MatchingLabels{v1.LabelPackage: "app"}); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) > int(limit)+2 {
		t.Fatalf("%d revisions kept with a limit of %d: %s", len(list.Items), limit, e.revs("app"))
	}
	if a := e.pkg("app").Status.Retry.Attempts; a != 10 {
		t.Fatalf("attempts = %d, want 10: %s", a, e.revs("app"))
	}
	// What a failed upgrade returns to is never pruned.
	if e.be.chartOf("ns-app/app") != "app@1.0.0" {
		t.Fatalf("chart = %s", e.be.chartOf("ns-app/app"))
	}
}

func TestRequestedRollbackIsNotRetried(t *testing.T) {
	e := newEnv(t)
	now := e.clockAt(time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC))
	e.create(mkSource("app", "1.0.0", true, "app"), &v1.Package{ObjectMeta: metav1.ObjectMeta{Name: "app"}})
	e.reconcile("app")
	e.setVersion("app", "1.1.0", true)
	e.be.failOn["app"] = true
	e.reconcile("app") // fails, rolled back to 1.0.0
	p := e.pkg("app")
	p.Annotations = map[string]string{AnnotationRollbackTo: "1"}
	if err := e.c.Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	e.reconcile("app")
	e.be.failOn["app"] = false
	*now = now.Add(time.Hour)
	if res := e.reconcile("app"); res.RequeueAfter != 0 {
		t.Fatalf("a requested rollback waits for a retry: %s", res.RequeueAfter)
	}
	if e.be.chartOf("ns-app/app") != "app@1.0.0" {
		t.Fatalf("a requested rollback was undone: %s", e.be.chartOf("ns-app/app"))
	}
	if e.pkg("app").Status.Retry != nil {
		t.Fatal("a requested rollback reports a retry")
	}
}

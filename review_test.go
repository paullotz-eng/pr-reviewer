package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

const sampleDiff = `diff --git a/src/app.ts b/src/app.ts
index 111..222 100644
--- a/src/app.ts
+++ b/src/app.ts
@@ -10,3 +10,4 @@ function main() {
 const a = 1
-const b = 2
+const b = 3
+--already-dashed
 const c = 4
diff --git a/yarn.lock b/yarn.lock
--- a/yarn.lock
+++ b/yarn.lock
@@ -1,1 +1,1 @@
-old
+new
diff --git a/src/gone.ts b/src/gone.ts
deleted file mode 100644
--- a/src/gone.ts
+++ /dev/null
@@ -1,1 +0,0 @@
-bye
`

func TestAnnotateDiff(t *testing.T) {
	out, valid := annotateDiff(sampleDiff)
	for _, want := range []string{"   10  const a = 1", "      -const b = 2", "   11 +const b = 3", "   12 +--already-dashed", "   13  const c = 4"} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("annotated diff missing %q:\n%s", want, out)
		}
	}
	for line, want := range map[int]bool{9: false, 10: true, 11: true, 12: true, 13: true, 14: false} {
		if valid["src/app.ts"][line] != want {
			t.Errorf("valid[src/app.ts][%d] = %v, want %v", line, !want, want)
		}
	}
	if len(valid["src/gone.ts"]) != 0 {
		t.Errorf("deleted file should have no commentable lines: %v", valid["src/gone.ts"])
	}
}

func TestFilterDiff(t *testing.T) {
	kept, skipped := filterDiff(sampleDiff)
	if !slices.Equal(skipped, []string{"yarn.lock"}) {
		t.Errorf("skipped = %v", skipped)
	}
	if strings.Contains(kept, "yarn.lock") || !strings.Contains(kept, "src/app.ts") || !strings.Contains(kept, "src/gone.ts") {
		t.Errorf("wrong files kept:\n%s", kept)
	}
	for _, p := range []string{"a/__generated__/x.ts", "schema.generated.ts", "x/__snapshots__/a.test.tsx.snap", "pnpm-lock.yaml"} {
		if !generatedRe.MatchString(p) {
			t.Errorf("%s should be filtered", p)
		}
	}
	for _, p := range []string{"src/generator.ts", "src/dist-utils.ts", "UserProfile.tsx"} {
		if generatedRe.MatchString(p) {
			t.Errorf("%s should be kept", p)
		}
	}
}

func TestIsFrontend(t *testing.T) {
	pr := func(paths ...string) *PRDetails {
		d := &PRDetails{}
		for _, p := range paths {
			d.Files = append(d.Files, struct {
				Path string `json:"path"`
			}{p})
		}
		return d
	}
	if !isFrontend(pr("api/a.go", "web/UserCard.tsx")) || !isFrontend(pr("hooks/use-user.ts")) {
		t.Error("expected frontend")
	}
	if isFrontend(pr("api/user.ts", "README.md", "src/user-use.ts")) {
		t.Error("expected not frontend")
	}
}

func TestRemoteMatches(t *testing.T) {
	for _, u := range []string{"git@github.com:eversport/eversports.git", "https://github.com/Eversport/Eversports"} {
		if !remoteMatches(u, "eversport/eversports") {
			t.Errorf("%s should match", u)
		}
	}
	if remoteMatches("git@github.com:other/eversports.git", "eversport/eversports") {
		t.Error("other owner should not match")
	}
}

func TestGuard(t *testing.T) {
	h := guard("127.0.0.1:7777", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	cases := []struct {
		name, method, host, origin, ctype string
		want                              int
	}{
		{"page load", "GET", "127.0.0.1:7777", "", "", 200},
		{"localhost alias", "GET", "localhost:7777", "", "", 200},
		{"dns rebinding", "GET", "evil.example:7777", "", "", 403},
		{"app POST", "POST", "127.0.0.1:7777", "http://127.0.0.1:7777", "application/json", 200},
		{"cross-site POST", "POST", "127.0.0.1:7777", "https://evil.example", "application/json", 403},
		{"no origin POST", "POST", "127.0.0.1:7777", "", "application/json", 403},
		{"text/plain form POST", "POST", "127.0.0.1:7777", "http://127.0.0.1:7777", "text/plain", 415},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "/api/comment", nil)
		req.Host = c.host
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		if c.ctype != "" {
			req.Header.Set("Content-Type", c.ctype)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, rec.Code, c.want)
		}
	}
}

func TestApplyChallenge(t *testing.T) {
	r := &Review{Verdict: "request_changes", Comments: []Comment{
		{Subject: "a", Severity: "high", Decorations: []string{"blocking"}},
		{Subject: "b", Severity: "medium", Decorations: []string{"blocking"}},
		{Subject: "c", Severity: "critical", Decorations: []string{"non-blocking"}},
		{Subject: "d", Severity: "low"},
	}}
	applyChallenge(r, []challengeResult{
		{ID: 0, Outcome: "refuted", Reason: "guarded in caller"},
		{ID: 1, Outcome: "unresolved", Reason: "depends on config"},
		{ID: 2, Outcome: "confirmed", Reason: "traced"},
		{ID: 3, Outcome: "refuted", Reason: "not challenged, must be ignored"},
		{ID: 9, Outcome: "refuted", Reason: "out of range"},
	})
	var subjects []string
	for _, c := range r.Comments {
		subjects = append(subjects, c.Subject)
	}
	if !slices.Equal(subjects, []string{"b", "c", "d"}) {
		t.Errorf("kept = %v", subjects)
	}
	if len(r.Rejected) != 1 || r.Rejected[0].Subject != "a" || r.Rejected[0].Challenge != "refuted: guarded in caller" {
		t.Errorf("rejected = %+v", r.Rejected)
	}
	if !slices.Equal(r.Comments[0].Decorations, []string{"non-blocking"}) {
		t.Errorf("unresolved finding should be non-blocking: %v", r.Comments[0].Decorations)
	}
	if r.Comments[2].Challenge != "" {
		t.Errorf("unchallenged finding got a challenge note")
	}
	if r.Verdict != "comment" {
		t.Errorf("verdict = %s, want comment once nothing blocks", r.Verdict)
	}
	if r.Challenge != "challenger: 1 confirmed, 1 unresolved, 1 refuted" {
		t.Errorf("challenge = %q", r.Challenge)
	}
}

func TestWatcherDue(t *testing.T) {
	now := time.Now()
	w := &watcher{since: now.Add(-24 * time.Hour), me: "me", members: map[string]bool{"me": true, "ana": true},
		known: func(key string) bool { return key == "o/r#2" }}
	cases := []struct {
		name string
		pr   PR
		want bool
	}{
		{"new team PR", PR{Repo: "o/r", Number: 1, Author: "ana", CreatedAt: now}, true},
		{"already reviewed", PR{Repo: "o/r", Number: 2, Author: "ana", CreatedAt: now}, false},
		{"outside team", PR{Repo: "o/r", Number: 3, Author: "bob", CreatedAt: now}, false},
		{"my own PR", PR{Repo: "o/r", Number: 4, Author: "me", CreatedAt: now}, false},
		{"draft", PR{Repo: "o/r", Number: 5, Author: "ana", CreatedAt: now, IsDraft: true}, false},
		{"too old", PR{Repo: "o/r", Number: 6, Author: "ana", CreatedAt: now.Add(-48 * time.Hour)}, false},
	}
	for _, c := range cases {
		if got := w.due(c.pr); got != c.want {
			t.Errorf("%s: due = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRunnerDedupes(t *testing.T) {
	r := &runner{running: map[string]*call{}}
	release, calls := make(chan struct{}), 0
	done := make(chan *Review)
	go func() {
		rev, _ := r.do("k", func() (*Review, error) { calls++; <-release; return &Review{Verdict: "approve"}, nil })
		done <- rev
	}()
	for !r.isRunning("k") {
		time.Sleep(time.Millisecond)
	}
	go func() { rev, _ := r.do("k", func() (*Review, error) { calls++; return nil, nil }); done <- rev }()
	time.Sleep(10 * time.Millisecond)
	close(release)
	a, b := <-done, <-done
	if calls != 1 || a != b || a.Verdict != "approve" {
		t.Errorf("calls = %d, results %v %v", calls, a, b)
	}
}

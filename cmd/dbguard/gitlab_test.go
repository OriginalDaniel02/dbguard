package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OriginalDaniel02/dbguard/internal/report"
)

func glEnv(t *testing.T, srv *httptest.Server) {
	t.Setenv("CI_API_V4_URL", srv.URL)
	t.Setenv("CI_PROJECT_ID", "42")
	t.Setenv("CI_MERGE_REQUEST_IID", "7")
	t.Setenv("DBGUARD_GITLAB_TOKEN", "glpat-secret")
}

func TestGitlabCommentCreatesThenSkipsWhenNoMigrations(t *testing.T) {
	var bodies []string
	var notes []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			json.NewEncoder(w).Encode(notes)
		case "POST", "PUT":
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			bodies = append(bodies, r.Method+": "+b["body"])
			if r.Method == "POST" {
				notes = append(notes, map[string]any{"id": 1, "body": b["body"]})
			}
			io.WriteString(w, "{}")
		}
	}))
	defer srv.Close()
	glEnv(t, srv)

	var o, e bytes.Buffer
	body := report.Marker + "\n## DB Guard: 1 risky migration change(s)"
	if code := gitlabCommentCmd(nil, strings.NewReader(body), &o, &e); code != 0 || !strings.Contains(o.String(), "created") {
		t.Fatalf("code=%d out=%s err=%s", code, o.String(), e.String())
	}
	if code := gitlabCommentCmd(nil, strings.NewReader(body+" v2"), &o, &e); code != 0 || !strings.HasPrefix(bodies[len(bodies)-1], "PUT") {
		t.Fatalf("second call must update in place: %v", bodies)
	}
	if code := gitlabCommentCmd([]string{"--no-migrations"}, strings.NewReader(""), &o, &e); code != 0 ||
		!strings.Contains(bodies[len(bodies)-1], "no migration files changed") {
		t.Fatalf("--no-migrations must resolve the old warning: %v", bodies)
	}

	notes = nil
	n := len(bodies)
	gitlabCommentCmd([]string{"--no-migrations"}, strings.NewReader(""), &o, &e)
	if len(bodies) != n {
		t.Error("--no-migrations must never create a comment when none exists")
	}
}

func TestGitlabCommentErrors(t *testing.T) {
	var o, e bytes.Buffer
	for _, k := range []string{"CI_API_V4_URL", "CI_PROJECT_ID", "CI_MERGE_REQUEST_IID", "DBGUARD_GITLAB_TOKEN"} {
		t.Setenv(k, "")
	}
	if code := gitlabCommentCmd(nil, strings.NewReader("x"), &o, &e); code != 2 || !strings.Contains(e.String(), "merge request pipeline") {
		t.Fatalf("outside an MR pipeline: code=%d %s", code, e.String())
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv.Close()
	glEnv(t, srv)
	e.Reset()
	if code := gitlabCommentCmd(nil, strings.NewReader("x"), &o, &e); code != 2 || strings.Contains(e.String(), "glpat-secret") {
		t.Fatalf("auth failure: code=%d %s", code, e.String())
	}
	if code := gitlabCommentCmd(nil, strings.NewReader(""), &o, &e); code != 2 {
		t.Errorf("empty body must be rejected, got %d", code)
	}
}

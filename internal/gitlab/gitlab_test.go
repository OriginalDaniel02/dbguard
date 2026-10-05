package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const marker = "<!-- dbguard-report -->"

// fakeGitLab is a minimal MR notes API with pagination and token checking.
type fakeGitLab struct {
	notes    []note
	calls    []string
	pageSize int
}

func (f *fakeGitLab) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("PRIVATE-TOKEN") != "glpat-secret" {
			w.WriteHeader(401)
			return
		}
		base := "/projects/42/merge_requests/7/notes"
		switch {
		case r.Method == "GET" && r.URL.Path == base:
			page := 1
			fmt.Sscan(r.URL.Query().Get("page"), &page)
			size := f.pageSize
			if size == 0 {
				size = 100
			}
			lo, hi := (page-1)*size, page*size
			if lo > len(f.notes) {
				lo = len(f.notes)
			}
			if hi > len(f.notes) {
				hi = len(f.notes)
			}
			if hi < len(f.notes) {
				w.Header().Set("X-Next-Page", fmt.Sprint(page+1))
			}
			json.NewEncoder(w).Encode(f.notes[lo:hi])
		case r.Method == "POST" && r.URL.Path == base:
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			f.notes = append(f.notes, note{ID: 1000 + len(f.notes), Body: b["body"]})
			w.WriteHeader(201)
			io.WriteString(w, "{}")
		case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, base+"/"):
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			for i := range f.notes {
				if r.URL.Path == fmt.Sprintf("%s/%d", base, f.notes[i].ID) {
					f.notes[i].Body = b["body"]
				}
			}
			io.WriteString(w, "{}")
		default:
			w.WriteHeader(404)
		}
	})
}

func client(srv *httptest.Server, token string) *Client {
	return &Client{BaseURL: srv.URL, Token: token, ProjectID: "42", MRIID: "7"}
}

func TestCreatesThenUpdatesSingleComment(t *testing.T) {
	f := &fakeGitLab{notes: []note{{ID: 1, Body: "human comment"}}}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	c := client(srv, "glpat-secret")

	if act, err := c.Upsert(context.Background(), marker, marker+"\nfirst", false); err != nil || act != "created" {
		t.Fatalf("create: %q %v", act, err)
	}
	if act, err := c.Upsert(context.Background(), marker, marker+"\nsecond", false); err != nil || act != "updated" {
		t.Fatalf("update: %q %v", act, err)
	}
	var ours []note
	for _, n := range f.notes {
		if strings.Contains(n.Body, marker) {
			ours = append(ours, n)
		}
	}
	if len(ours) != 1 || !strings.Contains(ours[0].Body, "second") {
		t.Fatalf("want one updated DB Guard comment, got %+v", f.notes)
	}
	if f.notes[0].Body != "human comment" {
		t.Error("must never touch other people's comments")
	}
}

func TestOnlyUpdateNeverCreates(t *testing.T) {
	f := &fakeGitLab{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	c := client(srv, "glpat-secret")
	if act, err := c.Upsert(context.Background(), marker, "x", true); err != nil || act != "skipped" || len(f.notes) != 0 {
		t.Fatalf("act=%q err=%v notes=%v", act, err, f.notes)
	}
	f.notes = []note{{ID: 5, Body: marker + " old warning"}}
	if act, _ := c.Upsert(context.Background(), marker, marker+" resolved", true); act != "updated" || !strings.Contains(f.notes[0].Body, "resolved") {
		t.Fatalf("stale comment must be updated: %q %+v", act, f.notes)
	}
}

func TestFindsCommentOnLaterPage(t *testing.T) {
	f := &fakeGitLab{pageSize: 2}
	for i := 0; i < 5; i++ {
		f.notes = append(f.notes, note{ID: i + 1, Body: fmt.Sprintf("n%d", i)})
	}
	f.notes = append(f.notes, note{ID: 99, Body: marker + " old"})
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	if act, err := client(srv, "glpat-secret").Upsert(context.Background(), marker, marker+" new", false); err != nil || act != "updated" {
		t.Fatalf("pagination: %q %v", act, err)
	}
}

func TestAuthErrorsAreActionableAndLeakNothing(t *testing.T) {
	f := &fakeGitLab{}
	srv := httptest.NewServer(f.handler(t))
	_, err := client(srv, "wrong-token").Upsert(context.Background(), marker, "x", false)
	if err == nil || !strings.Contains(err.Error(), "api") || strings.Contains(err.Error(), "wrong-token") {
		t.Fatalf("401 should explain the token scope: %v", err)
	}
	srv.Close()
	_, err = client(srv, "glpat-secret").Upsert(context.Background(), marker, "x", false)
	if err == nil || strings.Contains(err.Error(), "glpat-secret") {
		t.Fatalf("transport error: %v", err)
	}
}

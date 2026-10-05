package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSlackPostsJSONText(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("method/content-type: %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
	}))
	defer srv.Close()
	if err := Slack(context.Background(), srv.URL, "drift in prod"); err != nil {
		t.Fatal(err)
	}
	if got["text"] != "drift in prod" {
		t.Errorf("payload: %v", got)
	}
}

func TestSlackErrorsDoNotLeakWebhook(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	url := srv.URL + "/services/T000/B000/SECRETTOKEN"
	err := Slack(context.Background(), url, "x")
	if err == nil || strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Fatalf("non-2xx must error without the URL: %v", err)
	}
	srv.Close()
	err = Slack(context.Background(), url, "x") // connection refused: error text embeds the URL
	if err == nil || strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Fatalf("transport error leaked webhook: %v", err)
	}
}

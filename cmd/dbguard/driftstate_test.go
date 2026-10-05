package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OriginalDaniel02/dbguard/internal/snapshot"
)

// harness runs `dbguard drift` repeatedly against a mutable fake database and
// records every Slack message, so we can assert exactly when it speaks.
type harness struct {
	t     *testing.T
	db    map[string]*snapshot.Schema
	sent  []string
	fail  bool
	now   time.Time
	state string
	exp   string
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, db: map[string]*snapshot.Schema{}, now: time.Date(2026, 10, 6, 6, 0, 0, 0, time.UTC)}
	h.state = filepath.Join(t.TempDir(), "state.json")
	h.exp = expectedFile(t, schemaWith(colID, colEmail))
	fakeDB(t, h.db)
	t.Setenv("PROD_DSN", "dsn-prod")
	t.Setenv("HOOK", "https://hooks.slack.test/x")

	oldSlack, oldNow := sendSlack, nowFn
	sendSlack = func(_ context.Context, _ string, text string) error {
		if h.fail {
			return errors.New("slack down")
		}
		h.sent = append(h.sent, text)
		return nil
	}
	nowFn = func() time.Time { return h.now }
	t.Cleanup(func() { sendSlack, nowFn = oldSlack, oldNow })
	return h
}

func (h *harness) run(extra ...string) int {
	h.t.Helper()
	args := append([]string{"--expected", h.exp, "--env", "prod=PROD_DSN", "--slack-env", "HOOK", "--state-file", h.state}, extra...)
	code, _, _ := runDrift(h.t, args...)
	return code
}

func (h *harness) set(s *snapshot.Schema) { h.db["dsn-prod"] = s }

func TestAlertsOnlyWhenDriftIsNewChangedOrResolved(t *testing.T) {
	h := newHarness(t)

	h.set(schemaWith(colID, colEmail)) // matches expected
	if h.run() != 0 || len(h.sent) != 0 {
		t.Fatalf("clean run must be silent: %v", h.sent)
	}

	h.set(schemaWith(colID, colEmail, colHot)) // incident: manual ALTER
	if h.run() != 1 || len(h.sent) != 1 || !strings.Contains(h.sent[0], "hotfix_flag") {
		t.Fatalf("new drift must alert once: %v", h.sent)
	}

	h.now = h.now.Add(24 * time.Hour)
	if h.run() != 1 || len(h.sent) != 1 {
		t.Fatalf("same unresolved drift next day must NOT alert again (exit code still 1): %v", h.sent)
	}

	h.set(schemaWith(colID, colEmail, colHot, snapshot.Column{Name: "other", Type: "int"}))
	h.run()
	if len(h.sent) != 2 || !strings.Contains(h.sent[1], "other") {
		t.Fatalf("a changed drift set must alert again: %v", h.sent)
	}

	h.set(schemaWith(colID, colEmail)) // reconciled
	if h.run() != 0 || len(h.sent) != 3 || !strings.Contains(h.sent[2], "resolved") {
		t.Fatalf("resolution must be announced: %v", h.sent)
	}
	if h.run() != 0 || len(h.sent) != 3 {
		t.Fatalf("resolution is announced only once: %v", h.sent)
	}
}

func TestReminderAfterRealertWindow(t *testing.T) {
	h := newHarness(t)
	h.set(schemaWith(colID, colEmail, colHot))
	h.run("--realert-after", "48h")
	h.now = h.now.Add(24 * time.Hour)
	h.run("--realert-after", "48h")
	if len(h.sent) != 1 {
		t.Fatalf("inside the window: %v", h.sent)
	}
	h.now = h.now.Add(25 * time.Hour)
	h.run("--realert-after", "48h")
	if len(h.sent) != 2 || !strings.Contains(h.sent[1], "still unresolved since 2026-10-06") {
		t.Fatalf("reminder after the window: %v", h.sent)
	}
	// 0 disables reminders entirely.
	h.now = h.now.Add(30 * 24 * time.Hour)
	h.run("--realert-after", "0")
	if len(h.sent) != 2 {
		t.Fatalf("--realert-after 0 must never remind: %v", h.sent)
	}
}

func TestFailedSlackSendIsRetriedNextRun(t *testing.T) {
	h := newHarness(t)
	h.set(schemaWith(colID, colEmail, colHot))
	h.fail = true
	if h.run() != 1 || len(h.sent) != 0 {
		t.Fatal("setup: send should have failed")
	}
	h.fail = false
	h.run()
	if len(h.sent) != 1 {
		t.Fatalf("an alert that failed to send must be retried, not recorded as delivered: %v", h.sent)
	}
}

func TestConnectionErrorDoesNotClearOrAnnounceResolution(t *testing.T) {
	h := newHarness(t)
	h.set(schemaWith(colID, colEmail, colHot))
	h.run()
	delete(h.db, "dsn-prod") // database unreachable this run
	if h.run() != 2 {
		t.Fatal("unreachable env must exit 2")
	}
	if len(h.sent) != 1 {
		t.Fatalf("an outage is not a resolution: %v", h.sent)
	}
	h.set(schemaWith(colID, colEmail, colHot)) // back, still drifted
	h.run()
	if len(h.sent) != 1 {
		t.Fatalf("still the same drift, no new alert: %v", h.sent)
	}
}

func TestWithoutStateFileEveryRunAlerts(t *testing.T) {
	h := newHarness(t)
	h.set(schemaWith(colID, colEmail, colHot))
	args := []string{"--expected", h.exp, "--env", "prod=PROD_DSN", "--slack-env", "HOOK"}
	runDrift(t, args...)
	runDrift(t, args...)
	if len(h.sent) != 2 {
		t.Fatalf("stateless mode keeps the old behavior: %v", h.sent)
	}
}

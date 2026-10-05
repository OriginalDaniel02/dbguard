package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OriginalDaniel02/dbguard/internal/snapshot"
)

func schemaWith(cols ...snapshot.Column) *snapshot.Schema {
	s := &snapshot.Schema{Engine: "postgres", Tables: []snapshot.Table{{Schema: "public", Name: "accounts", Columns: cols}}}
	s.Normalize()
	return s
}

var (
	colID    = snapshot.Column{Name: "id", Type: "bigint", NotNull: true}
	colEmail = snapshot.Column{Name: "email", Type: "text"}
	colHot   = snapshot.Column{Name: "hotfix_flag", Type: "boolean"}
)

// fakeDB maps DSN -> schema (or an error), standing in for real databases.
func fakeDB(t *testing.T, dbs map[string]*snapshot.Schema) {
	t.Helper()
	old := takeSnapshot
	takeSnapshot = func(_ context.Context, dsn string) (*snapshot.Schema, error) {
		if s, ok := dbs[dsn]; ok && s != nil {
			return s, nil
		}
		return nil, errors.New("could not connect to database")
	}
	t.Cleanup(func() { takeSnapshot = old })
}

func expectedFile(t *testing.T, s *snapshot.Schema) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "expected.json")
	f, _ := os.Create(p)
	defer f.Close()
	if err := snapshot.Write(f, s); err != nil {
		t.Fatal(err)
	}
	return p
}

func runDrift(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var o, e bytes.Buffer
	return driftCmd(args, &o, &e), o.String(), e.String()
}

func TestDriftManualChangeDetected(t *testing.T) {
	fakeDB(t, map[string]*snapshot.Schema{
		"dsn-prod":    schemaWith(colID, colEmail, colHot), // manual ALTER on prod
		"dsn-staging": schemaWith(colID, colEmail),
	})
	t.Setenv("PROD_DSN", "dsn-prod")
	t.Setenv("STAGING_DSN", "dsn-staging")
	exp := expectedFile(t, schemaWith(colID, colEmail))

	code, out, _ := runDrift(t, "--expected", exp, "--env", "prod=PROD_DSN", "--env", "staging=STAGING_DSN")
	if code != 1 {
		t.Fatalf("code = %d, want 1\n%s", code, out)
	}
	for _, want := range []string{"prod: 1 difference(s)", "public.accounts.hotfix_flag", "added outside migrations", "staging: no drift"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestDriftNoDriftExitsZero(t *testing.T) {
	fakeDB(t, map[string]*snapshot.Schema{"dsn": schemaWith(colID, colEmail)})
	t.Setenv("PROD_DSN", "dsn")
	exp := expectedFile(t, schemaWith(colID, colEmail))
	if code, out, _ := runDrift(t, "--expected", exp, "--env", "prod=PROD_DSN"); code != 0 {
		t.Fatalf("code=%d\n%s", code, out)
	}
}

func TestDriftBaselineEnvAndIgnore(t *testing.T) {
	fakeDB(t, map[string]*snapshot.Schema{
		"dsn-prod":    schemaWith(colID, colEmail),
		"dsn-staging": schemaWith(colID, colEmail, colHot),
	})
	t.Setenv("PROD_DSN", "dsn-prod")
	t.Setenv("STAGING_DSN", "dsn-staging")
	args := []string{"--baseline-env", "prod", "--env", "prod=PROD_DSN", "--env", "staging=STAGING_DSN"}
	if code, out, _ := runDrift(t, args...); code != 1 || !strings.Contains(out, "prod: baseline") {
		t.Fatalf("env-vs-env: code=%d\n%s", code, out)
	}
	ign := filepath.Join(t.TempDir(), "ignore")
	os.WriteFile(ign, []byte("# intentional\npublic.accounts.hotfix_*\n"), 0o644)
	if code, out, _ := runDrift(t, append(args, "--ignore-file", ign)...); code != 0 {
		t.Fatalf("ignored difference must not drift: code=%d\n%s", code, out)
	}
	if code, _, _ := runDrift(t, append(args, "--ignore", "public.accounts.hotfix_flag")...); code != 0 {
		t.Fatalf("--ignore flag: code=%d", code)
	}
}

func TestDriftErrorsAreExit2AndNeverLeakDSN(t *testing.T) {
	fakeDB(t, map[string]*snapshot.Schema{})
	t.Setenv("PROD_DSN", "postgres://u:topsecretpw@h/db")
	exp := expectedFile(t, schemaWith(colID))
	code, out, errOut := runDrift(t, "--expected", exp, "--env", "prod=PROD_DSN", "--env", "ghost=UNSET_VAR")
	if code != 2 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out, "ERROR") || strings.Contains(out+errOut, "topsecretpw") {
		t.Fatalf("output must report errors without credentials:\n%s\n%s", out, errOut)
	}
	if c, _, _ := runDrift(t, "--env", "prod=PROD_DSN"); c != 2 {
		t.Errorf("missing --expected/--baseline-env: want 2, got %d", c)
	}
	if c, _, _ := runDrift(t, "--expected", exp, "--baseline-env", "prod", "--env", "prod=PROD_DSN"); c != 2 {
		t.Errorf("both --expected and --baseline-env: want 2, got %d", c)
	}
}

func TestDriftSlackAlert(t *testing.T) {
	fakeDB(t, map[string]*snapshot.Schema{"dsn": schemaWith(colID, colEmail, colHot)})
	t.Setenv("PROD_DSN", "dsn")
	t.Setenv("HOOK", "https://hooks.slack.test/services/SECRET")
	exp := expectedFile(t, schemaWith(colID, colEmail))

	var sent, url string
	old := sendSlack
	sendSlack = func(_ context.Context, u, text string) error { url, sent = u, text; return nil }
	defer func() { sendSlack = old }()

	code, out, errOut := runDrift(t, "--expected", exp, "--env", "prod=PROD_DSN", "--slack-env", "HOOK")
	if code != 1 || url != "https://hooks.slack.test/services/SECRET" {
		t.Fatalf("code=%d url=%q", code, url)
	}
	for _, want := range []string{"prod", "public.accounts", "hotfix_flag"} {
		if !strings.Contains(sent, want) {
			t.Errorf("slack message missing %q:\n%s", want, sent)
		}
	}
	if strings.Contains(out+errOut, "SECRET") {
		t.Error("webhook URL leaked to output")
	}

	// No drift => no alert.
	sent = ""
	exp2 := expectedFile(t, schemaWith(colID, colEmail, colHot))
	if code, _, _ := runDrift(t, "--expected", exp2, "--env", "prod=PROD_DSN", "--slack-env", "HOOK"); code != 0 || sent != "" {
		t.Errorf("no drift must not alert: code=%d sent=%q", code, sent)
	}
}

func TestDriftSaveDirAndJSON(t *testing.T) {
	fakeDB(t, map[string]*snapshot.Schema{"dsn": schemaWith(colID, colEmail, colHot)})
	t.Setenv("PROD_DSN", "dsn")
	exp := expectedFile(t, schemaWith(colID, colEmail))
	dir := t.TempDir()
	code, out, _ := runDrift(t, "--expected", exp, "--env", "prod=PROD_DSN", "--save-dir", dir, "--format", "json")
	if code != 1 || !strings.Contains(out, `"column-extra"`) {
		t.Fatalf("code=%d json:\n%s", code, out)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "prod", "*.json"))
	if len(files) != 1 {
		t.Fatalf("expected one saved snapshot, got %v", files)
	}
}

func TestSnapshotCommand(t *testing.T) {
	fakeDB(t, map[string]*snapshot.Schema{"dsn": schemaWith(colID)})
	t.Setenv("DBGUARD_DSN", "dsn")
	out := filepath.Join(t.TempDir(), "s.json")
	var o, e bytes.Buffer
	if code := snapshotCmd([]string{"--out", out}, &o, &e); code != 0 {
		t.Fatalf("code=%d %s", code, e.String())
	}
	f, _ := os.Open(out)
	defer f.Close()
	if s, err := snapshot.Read(f); err != nil || len(s.Tables) != 1 {
		t.Fatalf("round trip: %v %v", s, err)
	}
	t.Setenv("DBGUARD_DSN", "")
	if code := snapshotCmd(nil, &o, &e); code != 2 {
		t.Errorf("unset DSN: want 2, got %d", code)
	}
}

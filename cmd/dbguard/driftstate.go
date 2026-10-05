package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"time"

	"github.com/OriginalDaniel02/dbguard/internal/drift"
)

// nowFn is replaceable in tests.
var nowFn = time.Now

// envState remembers what was last alerted for one environment, so an unfixed
// drift does not page the channel on every scheduled run.
type envState struct {
	Hash        string    `json:"hash"`
	FirstSeen   time.Time `json:"first_seen"`
	LastAlerted time.Time `json:"last_alerted,omitempty"`
}

type stateFile map[string]envState

func loadState(path string) (stateFile, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return stateFile{}, nil
	}
	if err != nil {
		return stateFile{}, err
	}
	var s stateFile
	if err := json.Unmarshal(b, &s); err != nil {
		return stateFile{}, err
	}
	if s == nil {
		s = stateFile{}
	}
	return s, nil
}

func (s stateFile) save(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// hashDiffs is order-independent and identifies one exact set of differences.
func hashDiffs(ds []drift.Difference) string {
	lines := make([]string, len(ds))
	for i, d := range ds {
		lines[i] = d.Kind + "|" + d.Table + "|" + d.Object + "|" + d.Expected + "|" + d.Actual
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// alertPlan says which environments to tell Slack about this run.
type alertPlan struct {
	New       map[string]bool      // drift appeared or changed
	Reminder  map[string]time.Time // still unresolved; value = first seen
	Resolved  []string             // drift cleared since the last alert
	anyAlerts bool
}

func (p alertPlan) empty() bool {
	return len(p.New) == 0 && len(p.Reminder) == 0 && len(p.Resolved) == 0
}

// planAlerts decides what to send. Without a state file (state == nil) every
// drifting environment is reported on every run.
func planAlerts(state stateFile, order []string, results map[string]*envResult, baseline string, now time.Time, realert time.Duration) alertPlan {
	p := alertPlan{New: map[string]bool{}, Reminder: map[string]time.Time{}}
	for _, name := range order {
		r := results[name]
		if name == baseline || r.Error != "" {
			continue // errors say nothing about drift: leave state untouched
		}
		prev, had := state[name]
		switch {
		case len(r.Differences) > 0:
			switch {
			case state == nil, !had, prev.Hash != hashDiffs(r.Differences):
				p.New[name] = true
			case realert > 0 && now.Sub(prev.LastAlerted) >= realert:
				p.Reminder[name] = prev.FirstSeen
			}
		case had && prev.Hash != "":
			p.Resolved = append(p.Resolved, name)
		}
	}
	return p
}

// apply records the outcome in state. Call it only after the alert was delivered
// (or when there is no Slack), so a failed send is retried on the next run.
func (s stateFile) apply(order []string, results map[string]*envResult, baseline string, p alertPlan, now time.Time) {
	for _, name := range order {
		r := results[name]
		if name == baseline || r.Error != "" {
			continue
		}
		if len(r.Differences) == 0 {
			delete(s, name)
			continue
		}
		h := hashDiffs(r.Differences)
		prev, had := s[name]
		cur := envState{Hash: h, FirstSeen: now, LastAlerted: prev.LastAlerted}
		if had && prev.Hash == h {
			cur.FirstSeen = prev.FirstSeen
		}
		if p.New[name] {
			cur.LastAlerted = now
		}
		if _, ok := p.Reminder[name]; ok {
			cur.LastAlerted = now
		}
		s[name] = cur
	}
}

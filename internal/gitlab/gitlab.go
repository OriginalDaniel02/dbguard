// Package gitlab posts DB Guard's merge request comment. It keeps exactly one
// comment per MR (found by a hidden marker) and edits it in place. The access
// token is a credential: it is never logged and is redacted from errors.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks to the GitLab REST API (v4) for one merge request.
type Client struct {
	BaseURL   string // e.g. https://gitlab.com/api/v4 (CI_API_V4_URL)
	Token     string // project/group access token or PAT with the "api" scope
	ProjectID string // CI_PROJECT_ID (numeric id or URL-encoded path)
	MRIID     string // CI_MERGE_REQUEST_IID
	HTTP      *http.Client
}

type note struct {
	ID   int    `json:"id"`
	Body string `json:"body"`
}

// Upsert edits the existing comment containing marker, or creates one. With
// onlyUpdate it never creates a comment (used when there is nothing to report
// but a stale warning from an earlier push may exist). It returns the action taken.
func (c *Client) Upsert(ctx context.Context, marker, body string, onlyUpdate bool) (string, error) {
	existing, err := c.findNote(ctx, marker)
	if err != nil {
		return "", err
	}
	switch {
	case existing != 0:
		return "updated", c.send(ctx, http.MethodPut, c.notesURL()+"/"+strconv.Itoa(existing), body)
	case onlyUpdate:
		return "skipped", nil
	}
	return "created", c.send(ctx, http.MethodPost, c.notesURL(), body)
}

func (c *Client) notesURL() string {
	return fmt.Sprintf("%s/projects/%s/merge_requests/%s/notes",
		strings.TrimRight(c.BaseURL, "/"), url.PathEscape(c.ProjectID), url.PathEscape(c.MRIID))
}

func (c *Client) findNote(ctx context.Context, marker string) (int, error) {
	for page := 1; page < 1000; page++ {
		var notes []note
		next, err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s?per_page=100&page=%d", c.notesURL(), page), nil, &notes)
		if err != nil {
			return 0, err
		}
		for _, n := range notes {
			if strings.Contains(n.Body, marker) {
				return n.ID, nil
			}
		}
		if next == "" {
			return 0, nil
		}
	}
	return 0, nil
}

func (c *Client) send(ctx context.Context, method, u, body string) error {
	payload, _ := json.Marshal(map[string]string{"body": body})
	_, err := c.do(ctx, method, u, payload, nil)
	return err
}

// do performs a request and returns the X-Next-Page header (pagination).
func (c *Client) do(ctx context.Context, method, u string, payload []byte, out any) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
	if err != nil {
		return "", errors.New("invalid GitLab API URL")
	}
	req.Header.Set("PRIVATE-TOKEN", c.Token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	h := c.HTTP
	if h == nil {
		h = http.DefaultClient
	}
	resp, err := h.Do(req)
	if err != nil {
		return "", errors.New("could not reach GitLab: " + c.redact(err.Error()))
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", fmt.Errorf("GitLab API returned HTTP %d: the token needs the \"api\" scope and Developer access to the project (CI_JOB_TOKEN cannot post merge request comments)", resp.StatusCode)
	case resp.StatusCode/100 != 2:
		return "", fmt.Errorf("GitLab API returned HTTP %d", resp.StatusCode)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return "", errors.New("unexpected response from GitLab")
		}
	}
	return resp.Header.Get("X-Next-Page"), nil
}

func (c *Client) redact(s string) string {
	if c.Token != "" {
		s = strings.ReplaceAll(s, c.Token, "[redacted]")
	}
	return s
}

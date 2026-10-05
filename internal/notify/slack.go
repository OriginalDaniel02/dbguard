// Package notify delivers drift alerts. The webhook URL is a credential: it is
// never logged and is redacted from returned errors.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Slack posts text to a Slack incoming webhook.
func Slack(ctx context.Context, webhookURL, text string) error {
	return slack(ctx, http.DefaultClient, webhookURL, text)
}

func slack(ctx context.Context, c *http.Client, webhookURL, text string) error {
	body, _ := json.Marshal(map[string]string{"text": text})
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid Slack webhook URL")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		// net/http errors embed the URL; never let it escape.
		return errors.New("could not reach Slack: " + strings.ReplaceAll(err.Error(), webhookURL, "[redacted]"))
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("Slack webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}

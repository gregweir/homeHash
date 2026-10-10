package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"
)

// MultiNotifier fans one alert out to several notifiers, reporting every
// failure. It lets main combine a webhook with, say, email.
type MultiNotifier []Notifier

// NewMulti wraps the given notifiers in one.
func NewMulti(ns ...Notifier) MultiNotifier { return ns }

// Notify sends to each notifier and joins the errors (nil if all succeed).
func (m MultiNotifier) Notify(ctx context.Context, a Alert) error {
	var errs []error
	for _, n := range m {
		if err := n.Notify(ctx, a); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// LogNotifier writes each alert to the process log. It is the default
// notifier when nothing else is configured.
type LogNotifier struct{}

// Notify logs the alert.
func (LogNotifier) Notify(_ context.Context, a Alert) error {
	log.Printf("ALERT %s", FormatAlert(a))
	return nil
}

// DiscordWebhook posts alerts to a Discord webhook as plain-text messages.
type DiscordWebhook struct {
	url     string
	timeout time.Duration
}

// NewDiscordWebhook creates a webhook client for the given endpoint.
func NewDiscordWebhook(url string, timeout time.Duration) *DiscordWebhook {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &DiscordWebhook{url: url, timeout: timeout}
}

type discordPayload struct {
	Content string `json:"content"`
}

// Notify posts the alert as {"content": ...}; Discord answers 204 on success.
func (w *DiscordWebhook) Notify(ctx context.Context, a Alert) error {
	body, err := json.Marshal(discordPayload{Content: FormatAlert(a)})
	if err != nil {
		return fmt.Errorf("encode discord payload: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build discord request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("post discord webhook: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("discord webhook: unexpected status %d", resp.StatusCode)
	}
	return nil
}

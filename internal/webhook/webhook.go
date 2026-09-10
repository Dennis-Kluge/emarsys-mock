// Package webhook delivers outbound notifications when the mock receives an
// event trigger.
//
// This is the generic alternative to publishing straight to Pub/Sub: the mock
// keeps no cloud dependency, and whatever needs the message subscribes with a
// small adapter. In CI the target is usually an httptest server in the test
// itself.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Sender posts payloads to a configured URL.
//
// Delivery never blocks the API response and never turns a good response into a
// failed one: a mock whose own notifications can fail a client's request is
// worse than no notifications at all.
type Sender struct {
	url     string
	client  *http.Client
	logger  *slog.Logger
	wg      sync.WaitGroup
	timeout time.Duration
}

func New(url string, timeout time.Duration, logger *slog.Logger) *Sender {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Sender{
		url:     url,
		client:  &http.Client{Timeout: timeout},
		logger:  logger,
		timeout: timeout,
	}
}

// Enabled reports whether an outbound URL is configured.
func (s *Sender) Enabled() bool { return s != nil && s.url != "" }

// Send delivers a payload in the background.
func (s *Sender) Send(payload any) {
	if !s.Enabled() {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		s.logger.Error("webhook payload could not be encoded", "err", err)
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
		defer cancel()

		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
		if reqErr != nil {
			s.logger.Error("webhook request could not be built", "err", reqErr)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "emarsys-mock")

		resp, sendErr := s.client.Do(req)
		if sendErr != nil {
			s.logger.Warn("webhook delivery failed", "url", s.url, "err", sendErr)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			s.logger.Warn("webhook target returned an error",
				"url", s.url, "status", resp.StatusCode)
		}
	}()
}

// Wait blocks until in-flight deliveries finish. Tests use it to assert on a
// delivery; shutdown uses it so a trigger accepted just before SIGTERM is not
// dropped.
func (s *Sender) Wait() {
	if s == nil {
		return
	}
	s.wg.Wait()
}

// TriggerPayload is what the mock posts when an external event is triggered.
type TriggerPayload struct {
	Type       string          `json:"type"`
	EventID    int64           `json:"event_id"`
	EventName  string          `json:"event_name"`
	ContactID  *int64          `json:"contact_id,omitempty"`
	ExternalID string          `json:"external_id,omitempty"`
	KeyID      string          `json:"key_id,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	EventTime  string          `json:"event_time,omitempty"`
	TriggerID  string          `json:"trigger_id,omitempty"`
	ReceivedAt string          `json:"received_at"`
}

func (p TriggerPayload) String() string {
	return fmt.Sprintf("event %d (%s) for %s", p.EventID, p.EventName, p.ExternalID)
}

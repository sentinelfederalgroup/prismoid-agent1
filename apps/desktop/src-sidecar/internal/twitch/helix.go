package twitch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const defaultHelixBase = "https://api.twitch.tv/helix"

// ErrUnauthorized is a sentinel returned (wrapped inside [HelixError]) when
// the Helix API responds with 401. Callers use `errors.Is(err, ErrUnauthorized)`
// to decide whether to kick off a token refresh.
var ErrUnauthorized = errors.New("twitch helix: unauthorized")

// ErrRateLimited is returned after a 429 response when the automatic
// one-shot retry has already been consumed. Callers should back off per
// the per-provider token bucket (ADR 27 / PRI-20) before trying again.
var ErrRateLimited = errors.New("twitch helix: rate limit exceeded after retry")

// HelixError is the structured error body Helix returns on 4xx/5xx.
// Docs: https://dev.twitch.tv/docs/api/reference/ (error shape is documented
// per endpoint, but the envelope is always `{error, status, message}`).
type HelixError struct {
	Status  int    `json:"status"`
	Code    string `json:"error"`
	Message string `json:"message"`
}

func (e *HelixError) Error() string {
	return fmt.Sprintf("twitch helix %d %s: %s", e.Status, e.Code, e.Message)
}

// Is lets `errors.Is(err, ErrUnauthorized)` / `errors.Is(err, ErrRateLimited)`
// succeed regardless of how the HelixError is wrapped up the call stack.
func (e *HelixError) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized
	case ErrRateLimited:
		return e.Status == http.StatusTooManyRequests
	}
	return false
}

// HelixClient is a thin HTTP client for the Twitch Helix REST API. It owns
// the auth headers (Client-Id, Bearer token), base URL override for tests,
// and handles the Helix one-shot retry semantics on 429.
//
// Scope is deliberately narrow: no token bucket rate limiter (ADR 27 /
// PRI-20), no OAuth refresh on 401 (lives with the OAuth module). Callers
// receive `ErrUnauthorized` and decide whether to refresh + retry.
type HelixClient struct {
	HTTPClient  *http.Client
	BaseURL     string
	ClientID    string
	AccessToken string
}

// Do sends a request and decodes the JSON response into dst (nil for "don't
// care about the body"). On 429, it sleeps until `Ratelimit-Reset` and
// retries exactly once; a second 429 returns [ErrRateLimited].
//
// Context cancellation during the retry sleep returns `ctx.Err()`.
func (c *HelixClient) Do(ctx context.Context, method, path string, reqBody, dst any) error {
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := c.doOnce(ctx, method, path, reqBody)
		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resetAt := parseRatelimitReset(resp.Header.Get("Ratelimit-Reset"))
			closeBody(resp)
			if attempt == 0 {
				if err := waitUntil(ctx, resetAt); err != nil {
					return err
				}
				continue
			}
			return &HelixError{Status: http.StatusTooManyRequests, Code: "Too Many Requests", Message: "rate limit exceeded"}
		}

		return decodeResponse(resp, dst)
	}
	// Unreachable: the loop either returns or continues, and we only `continue`
	// on the first attempt.
	return errors.New("twitch helix: impossible retry state")
}

// Get is a convenience wrapper for GET requests.
func (c *HelixClient) Get(ctx context.Context, path string, dst any) error {
	return c.Do(ctx, http.MethodGet, path, nil, dst)
}

// Post is a convenience wrapper for POST requests.
func (c *HelixClient) Post(ctx context.Context, path string, reqBody, dst any) error {
	return c.Do(ctx, http.MethodPost, path, reqBody, dst)
}

// Delete is a convenience wrapper for DELETE requests.
func (c *HelixClient) Delete(ctx context.Context, path string, dst any) error {
	return c.Do(ctx, http.MethodDelete, path, nil, dst)
}

func (c *HelixClient) doOnce(ctx context.Context, method, path string, reqBody any) (*http.Response, error) {
	base := c.BaseURL
	if base == "" {
		base = defaultHelixBase
	}

	var body io.Reader
	if reqBody != nil {
		buf, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("marshal helix request body: %w", err)
		}
		body = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return nil, fmt.Errorf("build helix request: %w", err)
	}
	req.Header.Set("Client-Id", c.ClientID)
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("helix request: %w", err)
	}
	return resp, nil
}

// decodeResponse consumes resp.Body. Returns a structured HelixError for any
// non-2xx response; decodes JSON into dst for 2xx. A nil dst means the
// caller does not need the body (202 Accepted etc.).
func decodeResponse(resp *http.Response, dst any) error {
	defer closeBody(resp)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(resp.Body)
		he := &HelixError{Status: resp.StatusCode}
		// Best-effort decode. A non-JSON error body still surfaces via Message
		// with the raw bytes so callers can log it.
		if json.Unmarshal(buf, he) != nil {
			he.Message = string(buf)
		}
		return he
	}

	if dst == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("decode helix response: %w", err)
	}
	return nil
}

func closeBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

// parseRatelimitReset returns the absolute time the bucket refills. Twitch
// sends `Ratelimit-Reset` as a Unix-seconds timestamp. An empty or malformed
// value falls back to "retry immediately" (zero time).
func parseRatelimitReset(header string) time.Time {
	if header == "" {
		return time.Time{}
	}
	ts, err := strconv.ParseInt(header, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(ts, 0)
}

// waitUntil blocks until either the deadline passes or ctx is cancelled.
// A zero-value deadline returns immediately.
func waitUntil(ctx context.Context, deadline time.Time) error {
	if deadline.IsZero() {
		return nil
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Subscribe creates an EventSub subscription for channel.chat.message via
// the Helix API. `helixBase` can be overridden for testing; pass "" to use
// the default. The signature is kept for the existing caller in eventsub.go;
// internally it now goes through [HelixClient].
func Subscribe(ctx context.Context, helixBase, sessionID, broadcasterID, userID, accessToken, clientID string) error {
	client := &HelixClient{
		BaseURL:     helixBase,
		ClientID:    clientID,
		AccessToken: accessToken,
	}
	req := SubscriptionRequest{
		Type:    "channel.chat.message",
		Version: "1",
		Condition: map[string]string{
			"broadcaster_user_id": broadcasterID,
			"user_id":             userID,
		},
		Transport: Transport{
			Method:    "websocket",
			SessionID: sessionID,
		},
	}

	err := client.Post(ctx, "/eventsub/subscriptions", req, nil)
	if err == nil {
		return nil
	}
	// Preserve the existing callers' error surface: EventSub code uses
	// errors.As against *AuthError to decide whether to surface an
	// `auth_error` notification to the host. Map 401 onto that shape.
	if errors.Is(err, ErrUnauthorized) {
		var he *HelixError
		if errors.As(err, &he) {
			return &AuthError{Status: he.Status, Body: he.Error()}
		}
	}
	return err
}

// AuthError is the legacy 401 error type for EventSub callers that predate
// [HelixError]. Kept for backward compatibility with eventsub.go; new code
// should check `errors.Is(err, ErrUnauthorized)` against a HelixError
// directly instead.
type AuthError struct {
	Status int
	Body   string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("twitch auth error (%d): %s", e.Status, e.Body)
}

// Helix limit on a single chat message body. Messages longer than this are
// rejected with HTTP 400 by Twitch, so we mirror it locally to fail fast
// without consuming a Helix call.
const MaxChatMessageBytes = 500

// SendChatMessageRequest is the body Twitch's POST /chat/messages expects.
// `BroadcasterID` is the channel; `SenderID` must match the authenticated
// user on the access token. `ReplyParentMessageID` is omitted for top-level
// sends — reply support lives behind a follow-up control field.
type SendChatMessageRequest struct {
	BroadcasterID string `json:"broadcaster_id"`
	SenderID      string `json:"sender_id"`
	Message       string `json:"message"`
}

type BanUserRequest struct {
	Data BanUserRequestData `json:"data"`
}

type BanUserRequestData struct {
	UserID   string `json:"user_id"`
	Duration int    `json:"duration,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// SendChatMessageResponse is the envelope Twitch returns on 200. We surface
// the per-send drop reason (e.g. AutoMod, channel followers-only) so the UI
// can tell the user why a message did not appear.
type SendChatMessageResponse struct {
	Data []struct {
		MessageID  string `json:"message_id"`
		IsSent     bool   `json:"is_sent"`
		DropReason struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"drop_reason"`
	} `json:"data"`
}

// SendChatMessage posts a chat message via Helix and returns the parsed
// response. Caller is responsible for passing a non-empty message that fits
// in [MaxChatMessageBytes]; this method validates length up front to avoid
// a wasted round-trip.
func (c *HelixClient) SendChatMessage(ctx context.Context, broadcasterID, senderID, message string) (*SendChatMessageResponse, error) {
	if message == "" {
		return nil, errors.New("twitch helix: empty chat message")
	}
	if len(message) > MaxChatMessageBytes {
		return nil, fmt.Errorf("twitch helix: chat message exceeds %d bytes", MaxChatMessageBytes)
	}
	req := SendChatMessageRequest{
		BroadcasterID: broadcasterID,
		SenderID:      senderID,
		Message:       message,
	}
	var resp SendChatMessageResponse
	if err := c.Post(ctx, "/chat/messages", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *HelixClient) BanUser(ctx context.Context, broadcasterID, moderatorID, targetUserID, reason string) error {
	if broadcasterID == "" || moderatorID == "" || targetUserID == "" {
		return errors.New("twitch helix: missing broadcaster, moderator, or target user")
	}
	path := fmt.Sprintf("/moderation/bans?broadcaster_id=%s&moderator_id=%s", broadcasterID, moderatorID)
	return c.Post(ctx, path, BanUserRequest{Data: BanUserRequestData{
		UserID: targetUserID,
		Reason: reason,
	}}, nil)
}

func (c *HelixClient) TimeoutUser(ctx context.Context, broadcasterID, moderatorID, targetUserID string, durationSeconds int, reason string) error {
	if broadcasterID == "" || moderatorID == "" || targetUserID == "" {
		return errors.New("twitch helix: missing broadcaster, moderator, or target user")
	}
	if durationSeconds < 1 || durationSeconds > 1209600 {
		return fmt.Errorf("twitch helix: timeout duration out of range [1, 1209600]: %d", durationSeconds)
	}
	path := fmt.Sprintf("/moderation/bans?broadcaster_id=%s&moderator_id=%s", broadcasterID, moderatorID)
	return c.Post(ctx, path, BanUserRequest{Data: BanUserRequestData{
		UserID:   targetUserID,
		Duration: durationSeconds,
		Reason:   reason,
	}}, nil)
}

func (c *HelixClient) DeleteChatMessage(ctx context.Context, broadcasterID, moderatorID, messageID string) error {
	if broadcasterID == "" || moderatorID == "" || messageID == "" {
		return errors.New("twitch helix: missing broadcaster, moderator, or message id")
	}
	path := fmt.Sprintf("/moderation/chat?broadcaster_id=%s&moderator_id=%s&message_id=%s", broadcasterID, moderatorID, messageID)
	return c.Delete(ctx, path, nil)
}

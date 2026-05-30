package twitch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestSubscribeSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/eventsub/subscriptions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("bad auth header: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Client-Id") != "test-client" {
			t.Fatalf("bad client-id: %s", r.Header.Get("Client-Id"))
		}

		var req SubscriptionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req.Type != "channel.chat.message" {
			t.Fatalf("unexpected type: %s", req.Type)
		}
		if req.Transport.SessionID != "sess-123" {
			t.Fatalf("unexpected session_id: %s", req.Transport.SessionID)
		}

		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	err := Subscribe(context.Background(), srv.URL, "sess-123", "broadcaster-1", "user-1", "test-token", "test-client")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestSubscribeAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))
	}))
	defer srv.Close()

	err := Subscribe(context.Background(), srv.URL, "sess-123", "b", "u", "bad-token", "client")
	if err == nil {
		t.Fatal("expected error")
	}

	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthError, got %T: %v", err, err)
	}
	if authErr.Status != 401 {
		t.Fatalf("expected status 401, got %d", authErr.Status)
	}
}

func TestSubscribeServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"Internal Server Error"}`))
	}))
	defer srv.Close()

	err := Subscribe(context.Background(), srv.URL, "sess-123", "b", "u", "token", "client")
	if err == nil {
		t.Fatal("expected error for 500")
	}

	var authErr *AuthError
	if errors.As(err, &authErr) {
		t.Fatal("500 should not be an AuthError")
	}
}

func TestSendChatMessageSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/chat/messages" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req SendChatMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req.BroadcasterID != "b1" || req.SenderID != "u1" || req.Message != "hello" {
			t.Fatalf("unexpected body: %+v", req)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"message_id":"abc","is_sent":true}]}`))
	}))
	defer srv.Close()

	c := &HelixClient{BaseURL: srv.URL, ClientID: "cid", AccessToken: "tok"}
	resp, err := c.SendChatMessage(context.Background(), "b1", "u1", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Data) != 1 || !resp.Data[0].IsSent || resp.Data[0].MessageID != "abc" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestSendChatMessageDropped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"message_id":"","is_sent":false,"drop_reason":{"code":"msg_duplicate","message":"duplicate"}}]}`))
	}))
	defer srv.Close()

	c := &HelixClient{BaseURL: srv.URL, ClientID: "cid", AccessToken: "tok"}
	resp, err := c.SendChatMessage(context.Background(), "b1", "u1", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Data[0].IsSent {
		t.Fatal("expected dropped")
	}
	if resp.Data[0].DropReason.Code != "msg_duplicate" {
		t.Fatalf("unexpected drop code: %q", resp.Data[0].DropReason.Code)
	}
}

func TestSendChatMessageEmpty(t *testing.T) {
	c := &HelixClient{ClientID: "cid", AccessToken: "tok"}
	if _, err := c.SendChatMessage(context.Background(), "b1", "u1", ""); err == nil {
		t.Fatal("expected error for empty message")
	}
}

func TestSendChatMessageOversize(t *testing.T) {
	c := &HelixClient{ClientID: "cid", AccessToken: "tok"}
	big := make([]byte, MaxChatMessageBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	if _, err := c.SendChatMessage(context.Background(), "b1", "u1", string(big)); err == nil {
		t.Fatal("expected error for oversized message")
	}
}

func TestSendChatMessageUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"Unauthorized","status":401,"message":"Missing scope: user:write:chat"}`))
	}))
	defer srv.Close()

	c := &HelixClient{BaseURL: srv.URL, ClientID: "cid", AccessToken: "tok"}
	_, err := c.SendChatMessage(context.Background(), "b1", "u1", "hello")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestBanUserPostsModerationBan(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/moderation/bans" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("broadcaster_id") != "b1" || r.URL.Query().Get("moderator_id") != "m1" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		var req BanUserRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req.Data.UserID != "u1" || req.Data.Reason != "spam" || req.Data.Duration != 0 {
			t.Fatalf("unexpected body: %+v", req)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &HelixClient{BaseURL: srv.URL, ClientID: "cid", AccessToken: "tok"}
	if err := c.BanUser(context.Background(), "b1", "m1", "u1", "spam"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTimeoutUserPostsDuration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req BanUserRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req.Data.Duration != 60 {
			t.Fatalf("expected duration 60, got %d", req.Data.Duration)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &HelixClient{BaseURL: srv.URL, ClientID: "cid", AccessToken: "tok"}
	if err := c.TimeoutUser(context.Background(), "b1", "m1", "u1", 60, "burst"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTimeoutUserRejectsOutOfRange(t *testing.T) {
	c := &HelixClient{ClientID: "cid", AccessToken: "tok"}
	if err := c.TimeoutUser(context.Background(), "b1", "m1", "u1", 0, ""); err == nil {
		t.Fatal("expected error")
	}
	if err := c.TimeoutUser(context.Background(), "b1", "m1", "u1", 1209601, ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestDeleteChatMessageUsesModerationChatEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/moderation/chat" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("broadcaster_id") != "b1" || q.Get("moderator_id") != "m1" || q.Get("message_id") != "msg1" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &HelixClient{BaseURL: srv.URL, ClientID: "cid", AccessToken: "tok"}
	if err := c.DeleteChatMessage(context.Background(), "b1", "m1", "msg1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBanUserRequiresFields(t *testing.T) {
	c := &HelixClient{ClientID: "cid", AccessToken: "tok"}
	for i, args := range [][3]string{{"", "m1", "u1"}, {"b1", "", "u1"}, {"b1", "m1", ""}} {
		if err := c.BanUser(context.Background(), args[0], args[1], args[2], ""); err == nil {
			t.Fatalf("case %s expected error", strconv.Itoa(i))
		}
	}
}

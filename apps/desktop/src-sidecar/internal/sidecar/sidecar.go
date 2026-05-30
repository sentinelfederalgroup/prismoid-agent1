// Package sidecar contains the entry-point logic for the Go sidecar process.
//
// The actual main package in cmd/sidecar is a thin shim that calls Run.
// Logic lives here so it can be unit-tested without spawning a real process.
package sidecar

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/ImpulseB23/Prismoid/sidecar/internal/control"
	"github.com/ImpulseB23/Prismoid/sidecar/internal/emotes"
	"github.com/ImpulseB23/Prismoid/sidecar/internal/kick"
	"github.com/ImpulseB23/Prismoid/sidecar/internal/ringbuf"
	"github.com/ImpulseB23/Prismoid/sidecar/internal/twitch"
	"github.com/ImpulseB23/Prismoid/sidecar/internal/youtube"
)

const (
	outChanCapacity = 1024
	cmdChanCapacity = 16
	// Bootstrap and command-plane lines fit comfortably under 1 MB. The default
	// 64KB scanner limit is too tight for large control messages so we lift it
	// here with headroom for future growth. EventSub envelopes never traverse
	// stdin; they arrive over the WebSocket and exit through `out`.
	maxScannerLine  = 1024 * 1024
	heartbeatPeriod = 1 * time.Second
)

// Run is the sidecar entry point. It wires real stdin/stdout into RunWithIO,
// which contains the testable lifecycle logic.
func Run() error {
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return RunWithIO(ctx, os.Stdin, os.Stdout, log.Logger, ringbuf.Attach, ringbuf.Notify)
}

// AttachFunc opens a shared memory section by handle. The production
// implementation is ringbuf.Attach; tests inject a fake.
type AttachFunc func(handle uintptr, size int) ([]byte, func(), error)

// RunWithIO is the testable lifecycle entry: read the bootstrap, attach to
// the shared memory section via the supplied AttachFunc, spawn the writer
// goroutine, and run the command loop until ctx is cancelled or stdin closes.
//
// The `notify` callback is invoked by the writer goroutine after each
// successful ring write. In production it wraps `ringbuf.Notify` (SetEvent on
// Windows). Tests pass a no-op or a recorder.
func RunWithIO(
	ctx context.Context,
	stdin io.Reader,
	stdout io.Writer,
	logger zerolog.Logger,
	attach AttachFunc,
	notify NotifyFunc,
) error {
	logger.Info().Msg("sidecar starting")

	scanner := readerScanner(stdin)

	boot, err := ReadBootstrap(scanner)
	if err != nil {
		logger.Error().Err(err).Msg("failed to read bootstrap")
		return err
	}
	logger.Info().
		Uint64("handle", uint64(boot.ShmHandle)).
		Uint64("event_handle", uint64(boot.ShmEventHandle)).
		Int("size", boot.ShmSize).
		Msg("bootstrap received")

	mem, cleanup, err := attach(boot.ShmHandle, boot.ShmSize)
	if err != nil {
		logger.Error().Err(err).Msg("failed to attach to shared memory")
		return err
	}
	defer cleanup()

	// The host also inherited the event HANDLE separately; close our copy on
	// shutdown so the handle count doesn't grow over the sidecar's lifetime.
	// The host keeps its own reference and continues to own the underlying
	// kernel object.
	defer func() {
		if err := ringbuf.CloseEventHandle(boot.ShmEventHandle); err != nil {
			logger.Warn().Err(err).Msg("failed to close inherited event handle on shutdown")
		}
	}()

	writer, err := ringbuf.Open(mem)
	if err != nil {
		logger.Error().Err(err).Msg("failed to open ring buffer writer")
		return err
	}

	out := make(chan []byte, outChanCapacity)
	signal := MakeSignalFunc(boot.ShmEventHandle, notify, logger)
	go RunWriter(ctx, out, writer, signal)

	return RunCommandLoop(ctx, scanner, json.NewEncoder(stdout), out, logger, heartbeatPeriod)
}

// NotifyFunc signals the host that new data has been written to the ring
// buffer. The production impl is ringbuf.Notify; tests inject a fake.
type NotifyFunc func(eventHandle uintptr) error

// MakeSignalFunc builds the no-argument callback that [`RunWriter`] invokes
// after each successful ring buffer write. The callback is a thin wrapper
// around the provided NotifyFunc that:
//
//   - No-ops when eventHandle is 0 (bootstrap did not include an event, e.g.
//     under a Rust host version that pre-dates PRI-12 or on non-Windows
//     platforms that haven't wired eventfd/Mach semaphores yet).
//   - Logs at Warn level if the NotifyFunc returns an error, so a transient
//     SetEvent failure shows up in the host's stderr log drain without
//     stalling or panicking the writer goroutine.
//
// Extracted from the inline closure in RunWithIO so the branching logic is
// unit-testable.
func MakeSignalFunc(eventHandle uintptr, notify NotifyFunc, logger zerolog.Logger) func() {
	return func() {
		if eventHandle == 0 {
			return
		}
		if err := notify(eventHandle); err != nil {
			logger.Warn().Err(err).Msg("failed to signal ring buffer event")
		}
	}
}

// ReadBootstrap consumes a single line from the scanner and decodes it as a
// control.Bootstrap message. Returns an error on EOF or invalid JSON.
func ReadBootstrap(scanner *bufio.Scanner) (control.Bootstrap, error) {
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return control.Bootstrap{}, fmt.Errorf("read bootstrap line: %w", err)
		}
		return control.Bootstrap{}, fmt.Errorf("stdin closed before bootstrap")
	}
	var boot control.Bootstrap
	if err := json.Unmarshal(scanner.Bytes(), &boot); err != nil {
		return control.Bootstrap{}, fmt.Errorf("invalid bootstrap message: %w", err)
	}
	return boot, nil
}

// RunWriter is the sole producer to the ring buffer. Multiple platform clients
// send raw envelope bytes via `in`; this goroutine drains them serially into
// the ring buffer, calling `signal` after each successful write so the host
// can wake from WaitForSingleObject immediately.
//
// Backpressure: the ring buffer evicts oldest unread frames in place when a
// new write would not fit (drop-oldest). writer.Write only returns false when
// the payload itself is malformed (empty or larger than the ring capacity);
// those should never happen with normalized envelopes but are logged so a
// regression surfaces.
//
// Memory ordering: `writer.Write` ends with an atomic.StoreUint64 on the
// write index (release store in Go's memory model). `signal` ultimately makes
// a SetEvent syscall which acts as a full memory barrier, so by the time the
// host's WaitForSingleObject returns and it loads the write index with
// Acquire ordering, the payload bytes are guaranteed visible.
func RunWriter(ctx context.Context, in <-chan []byte, writer *ringbuf.Writer, signal func()) {
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-in:
			if !ok {
				return
			}
			if !writer.Write(data) {
				log.Warn().Int("bytes", len(data)).Msg("ring buffer rejected malformed payload")
				continue
			}
			signal()
		}
	}
}

// RunCommandLoop drives the heartbeat ticker and command dispatch until ctx is
// cancelled. Reads commands from the scanner via a small fan-in goroutine and
// writes heartbeats + notifications via the encoder. All writes to the encoder
// are serialized through encoderMu because notify is invoked from Twitch
// client goroutines while heartbeats fire from this loop; json.Encoder and
// the underlying io.Writer are not safe for concurrent use.
//
// `period` is the heartbeat tick interval; production passes [`heartbeatPeriod`],
// tests pass a short duration to keep the suite fast.
func RunCommandLoop(ctx context.Context, scanner *bufio.Scanner, encoder *json.Encoder, out chan<- []byte, logger zerolog.Logger, period time.Duration) error {
	cmdCh := make(chan control.Command, cmdChanCapacity)
	go scanCommands(scanner, cmdCh, logger)

	heartbeat := time.NewTicker(period)
	defer heartbeat.Stop()

	clients := make(map[string]context.CancelFunc)
	twitchClients := make(map[string]*twitch.Client)
	var encoderMu sync.Mutex
	notify := makeNotify(encoder, &encoderMu, logger)

	// Heartbeat counter is scoped to this loop's lifetime. Monotonic gaps let
	// the host watchdog detect missed ticks even if the underlying clock is
	// skewed. Resets to 0 on respawn, which is the correct signal.
	var heartbeatCounter uint64

	for {
		select {
		case <-ctx.Done():
			logger.Info().Msg("sidecar shutting down")
			return nil
		case <-heartbeat.C:
			heartbeatCounter++
			payload := control.HeartbeatPayload{
				TSMs:    time.Now().UnixMilli(),
				Counter: heartbeatCounter,
			}
			encoderMu.Lock()
			err := encoder.Encode(control.Message{Type: "heartbeat", Payload: payload})
			encoderMu.Unlock()
			if err != nil {
				logger.Error().Err(err).Msg("failed to write heartbeat to host")
				return err
			}
		case cmd := <-cmdCh:
			DispatchCommand(ctx, cmd, clients, twitchClients, out, notify, logger)
		}
	}
}

func scanCommands(scanner *bufio.Scanner, cmdCh chan<- control.Command, logger zerolog.Logger) {
	for scanner.Scan() {
		var cmd control.Command
		if err := json.Unmarshal(scanner.Bytes(), &cmd); err != nil {
			logger.Error().Err(err).Msg("invalid command from host")
			continue
		}
		cmdCh <- cmd
	}
}

func makeNotify(encoder *json.Encoder, encoderMu *sync.Mutex, logger zerolog.Logger) twitch.Notify {
	return func(msgType string, payload any) {
		encoderMu.Lock()
		err := encoder.Encode(control.Message{Type: msgType, Payload: payload})
		encoderMu.Unlock()
		if err != nil {
			logger.Error().Err(err).Str("type", msgType).Msg("failed to notify host")
		}
	}
}

// DispatchCommand routes a control.Command to its handler.
func DispatchCommand(ctx context.Context, cmd control.Command, clients map[string]context.CancelFunc, twitchClients map[string]*twitch.Client, out chan<- []byte, notify twitch.Notify, logger zerolog.Logger) {
	switch cmd.Cmd {
	case "twitch_connect":
		HandleTwitchConnect(ctx, cmd, clients, twitchClients, out, notify, logger)
	case "twitch_disconnect":
		HandleTwitchDisconnect(cmd, clients, twitchClients, logger)
	case "token_refresh":
		HandleTokenRefresh(cmd, twitchClients, logger)
	case "youtube_connect":
		HandleYouTubeConnect(ctx, cmd, clients, out, notify, logger)
	case "youtube_disconnect":
		HandleYouTubeDisconnect(cmd, clients, logger)
	case "kick_connect":
		HandleKickConnect(ctx, cmd, clients, out, logger)
	case "kick_disconnect":
		HandleKickDisconnect(cmd, clients, logger)
	case "ban_user":
		HandleBanUser(ctx, cmd, notify, logger)
	case "unban_user":
		HandleUnbanUser(cmd, logger)
	case "timeout_user":
		HandleTimeoutUser(ctx, cmd, notify, logger)
	case "delete_message":
		HandleDeleteMessage(ctx, cmd, notify, logger)
	case "send_chat_message":
		HandleSendChatMessage(ctx, cmd, notify, logger)
	case "youtube_send_message":
		HandleYouTubeSendMessage(ctx, cmd, notify, logger)
	default:
		logger.Info().Str("cmd", cmd.Cmd).Str("channel", cmd.Channel).Msg("received command")
	}
}

// Twitch Helix enforces a 1209600-second (14-day) maximum on timeouts;
// anything longer must be a permanent ban. Mirrored here so the log-only
// scaffold rejects values that the real Helix call would reject anyway,
// keeping the protocol contract consistent between the scaffold and the
// real implementation that lands in a follow-up.
const maxTimeoutSeconds = 1209600

// HandleBanUser logs the intended ban. Helix integration (POST
// /moderation/bans with body `{data: {user_id, reason}}`) lands in a
// follow-up PR; this scaffold exists to lock the host→sidecar protocol
// shape before the API client is built.
func HandleBanUser(ctx context.Context, cmd control.Command, notify twitch.Notify, logger zerolog.Logger) {
	reply := func(p SendChatResultPayload) {
		p.RequestID = cmd.RequestID
		notify("send_chat_result", p)
	}
	if cmd.BroadcasterID == "" || cmd.TargetUserID == "" {
		logger.Warn().
			Str("cmd", cmd.Cmd).
			Str("broadcaster", cmd.BroadcasterID).
			Str("target", cmd.TargetUserID).
			Msg("ban_user missing required field; ignoring")
		reply(SendChatResultPayload{ErrorMessage: "missing broadcaster or target"})
		return
	}
	if cmd.UserID == "" || cmd.ClientID == "" || cmd.Token == "" {
		reply(SendChatResultPayload{ErrorMessage: "missing moderator, client_id, or token"})
		return
	}
	client := &twitch.HelixClient{ClientID: cmd.ClientID, AccessToken: cmd.Token, BaseURL: sendChatHelixBase}
	if err := client.BanUser(ctx, cmd.BroadcasterID, cmd.UserID, cmd.TargetUserID, cmd.Reason); err != nil {
		logger.Warn().Err(err).Str("broadcaster", cmd.BroadcasterID).Str("target", cmd.TargetUserID).Msg("ban_user failed")
		reply(SendChatResultPayload{ErrorMessage: err.Error()})
		return
	}
	reply(SendChatResultPayload{Ok: true})
}

// HandleUnbanUser logs the intended unban. Helix: DELETE
// /moderation/bans?user_id=<id>&broadcaster_id=<id>&moderator_id=<id>.
func HandleUnbanUser(cmd control.Command, logger zerolog.Logger) {
	if cmd.BroadcasterID == "" || cmd.TargetUserID == "" {
		logger.Warn().
			Str("cmd", cmd.Cmd).
			Str("broadcaster", cmd.BroadcasterID).
			Str("target", cmd.TargetUserID).
			Msg("unban_user missing required field; ignoring")
		return
	}
	logger.Info().
		Str("broadcaster", cmd.BroadcasterID).
		Str("target", cmd.TargetUserID).
		Msg("unban_user (scaffold: no Helix call yet)")
}

// HandleTimeoutUser logs the intended timeout. Helix: POST /moderation/bans
// with body `{data: {user_id, duration, reason}}`, where duration is
// 1..1209600 seconds. Values outside that range are rejected locally to
// match Helix semantics and surface misuse in logs.
func HandleTimeoutUser(ctx context.Context, cmd control.Command, notify twitch.Notify, logger zerolog.Logger) {
	reply := func(p SendChatResultPayload) {
		p.RequestID = cmd.RequestID
		notify("send_chat_result", p)
	}
	if cmd.BroadcasterID == "" || cmd.TargetUserID == "" {
		logger.Warn().
			Str("cmd", cmd.Cmd).
			Str("broadcaster", cmd.BroadcasterID).
			Str("target", cmd.TargetUserID).
			Msg("timeout_user missing required field; ignoring")
		reply(SendChatResultPayload{ErrorMessage: "missing broadcaster or target"})
		return
	}
	if cmd.UserID == "" || cmd.ClientID == "" || cmd.Token == "" {
		reply(SendChatResultPayload{ErrorMessage: "missing moderator, client_id, or token"})
		return
	}
	if cmd.DurationSeconds < 1 || cmd.DurationSeconds > maxTimeoutSeconds {
		logger.Warn().
			Str("cmd", cmd.Cmd).
			Int("duration_seconds", cmd.DurationSeconds).
			Int("max_duration_seconds", maxTimeoutSeconds).
			Msg("timeout_user duration out of range [1, 1209600]; ignoring")
		reply(SendChatResultPayload{ErrorMessage: "timeout duration out of range [1, 1209600]"})
		return
	}
	client := &twitch.HelixClient{ClientID: cmd.ClientID, AccessToken: cmd.Token, BaseURL: sendChatHelixBase}
	if err := client.TimeoutUser(ctx, cmd.BroadcasterID, cmd.UserID, cmd.TargetUserID, cmd.DurationSeconds, cmd.Reason); err != nil {
		logger.Warn().Err(err).Str("broadcaster", cmd.BroadcasterID).Str("target", cmd.TargetUserID).Msg("timeout_user failed")
		reply(SendChatResultPayload{ErrorMessage: err.Error()})
		return
	}
	reply(SendChatResultPayload{Ok: true})
}

// HandleDeleteMessage logs the intended deletion. Helix: DELETE
// /moderation/chat?broadcaster_id=<id>&moderator_id=<id>&message_id=<id>.
// Message-less deletion (clear all chat) is deliberately not supported by
// this command — a separate `clear_chat` command would handle that when
// needed.
func HandleDeleteMessage(ctx context.Context, cmd control.Command, notify twitch.Notify, logger zerolog.Logger) {
	reply := func(p SendChatResultPayload) {
		p.RequestID = cmd.RequestID
		notify("send_chat_result", p)
	}
	if cmd.BroadcasterID == "" || cmd.MessageID == "" {
		logger.Warn().
			Str("cmd", cmd.Cmd).
			Str("broadcaster", cmd.BroadcasterID).
			Str("message_id", cmd.MessageID).
			Msg("delete_message missing required field; ignoring")
		reply(SendChatResultPayload{ErrorMessage: "missing broadcaster or message_id"})
		return
	}
	if cmd.UserID == "" || cmd.ClientID == "" || cmd.Token == "" {
		reply(SendChatResultPayload{ErrorMessage: "missing moderator, client_id, or token"})
		return
	}
	client := &twitch.HelixClient{ClientID: cmd.ClientID, AccessToken: cmd.Token, BaseURL: sendChatHelixBase}
	if err := client.DeleteChatMessage(ctx, cmd.BroadcasterID, cmd.UserID, cmd.MessageID); err != nil {
		logger.Warn().Err(err).Str("broadcaster", cmd.BroadcasterID).Str("message_id", cmd.MessageID).Msg("delete_message failed")
		reply(SendChatResultPayload{ErrorMessage: err.Error()})
		return
	}
	reply(SendChatResultPayload{Ok: true})
}

// SendChatResultPayload is the body of a `send_chat_result` notification
// emitted to the host after a send_chat_message attempt. The frontend uses
// this to surface failures (drop reasons, auth errors) without having to
// poll any other state.
type SendChatResultPayload struct {
	// RequestID echoes back the command's request_id so the host can
	// correlate this result with the awaiting Tauri invocation.
	RequestID    uint64 `json:"request_id,omitempty"`
	Ok           bool   `json:"ok"`
	MessageID    string `json:"message_id,omitempty"`
	DropCode     string `json:"drop_code,omitempty"`
	DropMessage  string `json:"drop_message,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

// sendChatHelixBase overrides the Helix base URL used by HandleSendChatMessage
// in tests. Empty string falls through to the production Helix endpoint.
var sendChatHelixBase = ""

// HandleSendChatMessage posts the user's message to Twitch via Helix and
// emits a `send_chat_result` notification with either the assigned
// message_id or the drop reason / transport error. Validation mirrors the
// Helix endpoint so obvious misuse (empty fields, oversized body) fails
// without consuming a request.
func HandleSendChatMessage(ctx context.Context, cmd control.Command, notify twitch.Notify, logger zerolog.Logger) {
	reply := func(p SendChatResultPayload) {
		p.RequestID = cmd.RequestID
		notify("send_chat_result", p)
	}
	if cmd.BroadcasterID == "" || cmd.UserID == "" || cmd.ClientID == "" || cmd.Token == "" {
		logger.Warn().
			Str("broadcaster", cmd.BroadcasterID).
			Str("user", cmd.UserID).
			Msg("send_chat_message missing required field; ignoring")
		reply(SendChatResultPayload{
			ErrorMessage: "missing broadcaster, user, client_id, or token",
		})
		return
	}
	if cmd.Message == "" {
		reply(SendChatResultPayload{ErrorMessage: "empty message"})
		return
	}
	if len(cmd.Message) > twitch.MaxChatMessageBytes {
		reply(SendChatResultPayload{
			ErrorMessage: fmt.Sprintf("message exceeds %d bytes", twitch.MaxChatMessageBytes),
		})
		return
	}
	client := &twitch.HelixClient{
		ClientID:    cmd.ClientID,
		AccessToken: cmd.Token,
		BaseURL:     sendChatHelixBase,
	}
	resp, err := client.SendChatMessage(ctx, cmd.BroadcasterID, cmd.UserID, cmd.Message)
	if err != nil {
		logger.Warn().Err(err).Str("broadcaster", cmd.BroadcasterID).Msg("send_chat_message failed")
		reply(SendChatResultPayload{ErrorMessage: err.Error()})
		return
	}
	if len(resp.Data) == 0 {
		reply(SendChatResultPayload{ErrorMessage: "empty response from helix"})
		return
	}
	first := resp.Data[0]
	if !first.IsSent {
		reply(SendChatResultPayload{
			DropCode:    first.DropReason.Code,
			DropMessage: first.DropReason.Message,
		})
		return
	}
	reply(SendChatResultPayload{
		Ok:        true,
		MessageID: first.MessageID,
	})
}

// youtubeSendAPIBase overrides the YouTube Data API base URL used by
// HandleYouTubeSendMessage in tests. Empty string falls through to the
// production endpoint.
var youtubeSendAPIBase = ""

// HandleYouTubeSendMessage posts the user's message to the supplied
// liveChatId via the YouTube Data API and emits a `send_chat_result`
// notification. Reuses the Twitch result shape because the host-side
// completer routing is keyed on `request_id` and the success/failure
// payload (message_id vs error_message) is identical between platforms.
func HandleYouTubeSendMessage(ctx context.Context, cmd control.Command, notify twitch.Notify, logger zerolog.Logger) {
	reply := func(p SendChatResultPayload) {
		p.RequestID = cmd.RequestID
		notify("send_chat_result", p)
	}
	if cmd.LiveChatID == "" || cmd.Token == "" {
		logger.Warn().
			Str("chat_id", cmd.LiveChatID).
			Msg("youtube_send_message missing required field; ignoring")
		reply(SendChatResultPayload{ErrorMessage: "missing live_chat_id or token"})
		return
	}
	if cmd.Message == "" {
		reply(SendChatResultPayload{ErrorMessage: "empty message"})
		return
	}
	client := &youtube.APIClient{
		AccessToken: cmd.Token,
		BaseURL:     youtubeSendAPIBase,
	}
	resp, err := client.SendChatMessage(ctx, cmd.LiveChatID, cmd.Message)
	if err != nil {
		logger.Warn().Err(err).Str("chat_id", cmd.LiveChatID).Msg("youtube_send_message failed")
		reply(SendChatResultPayload{
			DropCode:    youtubeErrorCode(err),
			DropMessage: err.Error(),
		})
		return
	}
	reply(SendChatResultPayload{
		Ok:        true,
		MessageID: resp.ID,
	})
}

// youtubeErrorCode maps a [youtube.APIClient] error onto a stable
// machine-readable code the Rust host re-surfaces to the UI. Anything
// the API client recognized as a known failure mode (auth/quota) gets a
// dedicated code so the frontend can render a tailored message without
// string-matching the human-readable text.
func youtubeErrorCode(err error) string {
	switch {
	case errors.Is(err, youtube.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, youtube.ErrQuotaExceeded):
		return "quota_exceeded"
	}
	var apiErr *youtube.APIError
	if errors.As(err, &apiErr) {
		return "youtube_api"
	}
	return "youtube_send_failed"
}

// HandleTwitchConnect spawns a Twitch EventSub client for the broadcaster in
// cmd if there isn't already one running. The client writes envelope bytes to
// `out`, which the writer goroutine drains into the ring buffer.
//
// A parallel goroutine fetches the channel's emote and badge bundle (Helix +
// 7TV + BTTV + FFZ) and emits it as a single `emote_bundle` control message.
// Fetches share the client's cancel context, so twitch_disconnect also
// cancels an in-flight bundle fetch. Failures per provider are captured in
// [emotes.Bundle.Errors] rather than blocking the chat connection.
func HandleTwitchConnect(ctx context.Context, cmd control.Command, clients map[string]context.CancelFunc, twitchClients map[string]*twitch.Client, out chan<- []byte, notify twitch.Notify, logger zerolog.Logger) {
	if _, exists := clients[cmd.BroadcasterID]; exists {
		logger.Warn().Str("broadcaster", cmd.BroadcasterID).Msg("already connected, ignoring")
		return
	}

	clientCtx, clientCancel := context.WithCancel(ctx)

	client := twitch.NewClient(
		cmd.BroadcasterID,
		cmd.UserID,
		cmd.Token,
		cmd.ClientID,
		out,
		logger.With().Str("broadcaster", cmd.BroadcasterID).Logger(),
		notify,
	)

	clients[cmd.BroadcasterID] = clientCancel
	twitchClients[cmd.BroadcasterID] = client

	go func() {
		// errors.Is handles both parent shutdown and per-client disconnect:
		// in either case the inner Read returns context.Canceled, which we
		// want to treat as a normal exit, not an error.
		if err := client.Run(clientCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error().Err(err).Str("broadcaster", cmd.BroadcasterID).Msg("twitch client exited")
		}
	}()

	go emoteFetchFn(clientCtx, cmd, notify, logger)

	logger.Info().Str("broadcaster", cmd.BroadcasterID).Msg("twitch client started")
}

// emoteFetchFn is the goroutine entry point for fetching an emote bundle on
// twitch_connect. Package-level so tests can stub it to a no-op without
// spinning up httptest servers for all four providers.
var emoteFetchFn = FetchAndNotifyEmotes

// FetchAndNotifyEmotes builds a [emotes.Fetcher] from the connect command's
// credentials, fetches the channel's full emote/badge bundle, and emits it
// to the host as an `emote_bundle` control message. Extracted so tests can
// drive it directly without spinning up the full command loop.
//
// An empty BroadcasterID is treated as "nothing to fetch" and the function
// returns without emitting. A cancelled context (twitch_disconnect or parent
// shutdown mid-fetch) short-circuits the emit alike.
func FetchAndNotifyEmotes(ctx context.Context, cmd control.Command, notify twitch.Notify, logger zerolog.Logger) {
	if cmd.BroadcasterID == "" {
		return
	}
	fetchAndEmit(ctx, buildFetcher(cmd), cmd.BroadcasterID, notify, logger)
}

// fetchAndEmit is the test-friendly core of [FetchAndNotifyEmotes]: given an
// already-constructed fetcher, run the fetch and emit the bundle.
func fetchAndEmit(ctx context.Context, f *emotes.Fetcher, broadcasterID string, notify twitch.Notify, logger zerolog.Logger) {
	bundle := f.Fetch(ctx, broadcasterID)
	if ctx.Err() != nil {
		return
	}
	for _, pe := range bundle.Errors {
		logger.Warn().
			Str("broadcaster", broadcasterID).
			Str("provider", string(pe.Provider)).
			Str("scope", string(pe.Scope)).
			Err(pe.Err).
			Msg("emote provider fetch failed")
	}
	notify("emote_bundle", bundle)
	logger.Info().
		Str("broadcaster", broadcasterID).
		Int("twitch_global", len(bundle.TwitchGlobalEmotes.Emotes)).
		Int("twitch_channel", len(bundle.TwitchChannelEmotes.Emotes)).
		Int("seventv_global", len(bundle.SevenTVGlobal.Emotes)).
		Int("seventv_channel", len(bundle.SevenTVChannel.Emotes)).
		Int("bttv_global", len(bundle.BTTVGlobal.Emotes)).
		Int("bttv_channel", len(bundle.BTTVChannel.Emotes)).
		Int("ffz_global", len(bundle.FFZGlobal.Emotes)).
		Int("ffz_channel", len(bundle.FFZChannel.Emotes)).
		Int("errors", len(bundle.Errors)).
		Msg("emote bundle ready")
}

// buildFetcher constructs an [emotes.Fetcher] from a twitch_connect command.
// Twitch Helix lookups require a client ID and bearer token; without them
// the first-party sub-client is left nil and the fetcher skips those
// endpoints entirely (third-party providers still run).
func buildFetcher(cmd control.Command) *emotes.Fetcher {
	f := &emotes.Fetcher{
		SevenTV: &emotes.SevenTVClient{},
		BTTV:    &emotes.BTTVClient{},
		FFZ:     &emotes.FFZClient{},
	}
	if cmd.ClientID != "" && cmd.Token != "" {
		f.Twitch = &emotes.TwitchClient{
			ClientID:    cmd.ClientID,
			AccessToken: cmd.Token,
		}
	}
	return f
}

// HandleTwitchDisconnect cancels and removes a previously-connected client.
func HandleTwitchDisconnect(cmd control.Command, clients map[string]context.CancelFunc, twitchClients map[string]*twitch.Client, logger zerolog.Logger) {
	cancelFn, exists := clients[cmd.BroadcasterID]
	if !exists {
		logger.Warn().Str("broadcaster", cmd.BroadcasterID).Msg("no active connection to disconnect")
		return
	}
	cancelFn()
	delete(clients, cmd.BroadcasterID)
	delete(twitchClients, cmd.BroadcasterID)
	logger.Info().Str("broadcaster", cmd.BroadcasterID).Msg("twitch client disconnected")
}

// HandleTokenRefresh updates the access token on all running Twitch clients.
// The Rust supervisor sends this when it proactively refreshes the token
// before expiry, so EventSub reconnects pick up the new credential.
func HandleTokenRefresh(cmd control.Command, twitchClients map[string]*twitch.Client, logger zerolog.Logger) {
	if cmd.Token == "" {
		logger.Warn().Msg("token_refresh missing token; ignoring")
		return
	}
	n := 0
	for id, c := range twitchClients {
		c.SetAccessToken(cmd.Token)
		logger.Debug().Str("broadcaster", id).Msg("token rotated")
		n++
	}
	logger.Info().Int("clients", n).Msg("token refresh applied")
}

// HandleYouTubeConnect spawns a YouTube gRPC streamList client for the given
// live chat ID. The client writes tagged JSON payloads to `out`, which the
// writer goroutine drains into the ring buffer.
func HandleYouTubeConnect(ctx context.Context, cmd control.Command, clients map[string]context.CancelFunc, out chan<- []byte, notify twitch.Notify, logger zerolog.Logger) {
	chatID := cmd.LiveChatID
	if chatID == "" {
		logger.Warn().Msg("youtube_connect missing live_chat_id")
		return
	}

	key := "yt:" + chatID
	if _, exists := clients[key]; exists {
		logger.Warn().Str("chat_id", chatID).Msg("already connected to youtube chat, ignoring")
		return
	}

	clientCtx, clientCancel := context.WithCancel(ctx)

	client := &youtube.Client{
		LiveChatID:  chatID,
		APIKey:      cmd.APIKey,
		AccessToken: cmd.Token,
		Out:         out,
		Log:         logger.With().Str("youtube_chat", chatID).Logger(),
		Notify:      notify,
	}

	clients[key] = clientCancel

	go func() {
		if err := client.Run(clientCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error().Err(err).Str("chat_id", chatID).Msg("youtube client exited")
		}
	}()

	logger.Info().Str("chat_id", chatID).Msg("youtube client started")
}

// HandleYouTubeDisconnect cancels and removes a previously-connected YouTube client.
func HandleYouTubeDisconnect(cmd control.Command, clients map[string]context.CancelFunc, logger zerolog.Logger) {
	chatID := cmd.LiveChatID
	if chatID == "" {
		logger.Warn().Msg("youtube_disconnect missing live_chat_id")
		return
	}
	key := "yt:" + chatID
	cancelFn, exists := clients[key]
	if !exists {
		logger.Warn().Str("chat_id", chatID).Msg("no active youtube connection to disconnect")
		return
	}
	cancelFn()
	delete(clients, key)
	logger.Info().Str("chat_id", chatID).Msg("youtube client disconnected")
}

// HandleKickConnect spawns a Kick Pusher WebSocket client for the chatroom in
// cmd if there isn't already one running. Uses the chatroom ID as the client
// registry key (prefixed with "kick:" to avoid collisions with Twitch IDs).
func HandleKickConnect(ctx context.Context, cmd control.Command, clients map[string]context.CancelFunc, out chan<- []byte, logger zerolog.Logger) {
	if cmd.ChatroomID <= 0 {
		logger.Warn().Int("chatroom", cmd.ChatroomID).Msg("kick_connect missing chatroom_id; ignoring")
		return
	}
	key := kickClientKey(cmd.ChatroomID)
	if _, exists := clients[key]; exists {
		logger.Warn().Int("chatroom", cmd.ChatroomID).Msg("kick already connected, ignoring")
		return
	}

	clientCtx, clientCancel := context.WithCancel(ctx)

	client := &kick.Client{
		ChatroomID: cmd.ChatroomID,
		Out:        out,
		Log:        logger.With().Int("chatroom", cmd.ChatroomID).Logger(),
		Notify:     func(string, any) {},
	}

	clients[key] = clientCancel

	go func() {
		if err := client.Run(clientCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error().Err(err).Int("chatroom", cmd.ChatroomID).Msg("kick client exited")
		}
	}()

	logger.Info().Int("chatroom", cmd.ChatroomID).Msg("kick client started")
}

// HandleKickDisconnect cancels and removes a previously-connected Kick client.
func HandleKickDisconnect(cmd control.Command, clients map[string]context.CancelFunc, logger zerolog.Logger) {
	key := kickClientKey(cmd.ChatroomID)
	cancelFn, exists := clients[key]
	if !exists {
		logger.Warn().Int("chatroom", cmd.ChatroomID).Msg("no active kick connection to disconnect")
		return
	}
	cancelFn()
	delete(clients, key)
	logger.Info().Int("chatroom", cmd.ChatroomID).Msg("kick client disconnected")
}

func kickClientKey(chatroomID int) string {
	return "kick:" + strconv.Itoa(chatroomID)
}

// readerScanner is a small helper used by tests; production code constructs
// its scanner directly from os.Stdin in Run.
func readerScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, maxScannerLine), maxScannerLine)
	return s
}

package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/rs/zerolog"

	"github.com/ImpulseB23/Prismoid/sidecar/internal/control"
	"github.com/ImpulseB23/Prismoid/sidecar/internal/emotes"
	"github.com/ImpulseB23/Prismoid/sidecar/internal/ringbuf"
	"github.com/ImpulseB23/Prismoid/sidecar/internal/twitch"
)

const (
	testCacheLine  = 64
	testHeaderSize = testCacheLine * 5
)

// makeTestRingBuffer constructs a ring buffer writer backed by a plain []byte.
// The header layout matches the production ringbuf package; this is the same
// pattern PR #24's tests used before they were rewritten to use channels.
func makeTestRingBuffer(t *testing.T, capacity int) (*ringbuf.Writer, []byte) {
	t.Helper()
	mem := make([]byte, testHeaderSize+capacity)
	*(*uint64)(unsafe.Pointer(&mem[testCacheLine*2])) = uint64(capacity)
	w, err := ringbuf.Open(mem)
	if err != nil {
		t.Fatalf("ringbuf.Open: %v", err)
	}
	return w, mem
}

func TestReadBootstrap_Valid(t *testing.T) {
	r := strings.NewReader(`{"shm_handle": 12345, "shm_size": 4096}` + "\n")
	scanner := readerScanner(r)

	boot, err := ReadBootstrap(scanner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if boot.ShmHandle != 12345 {
		t.Errorf("expected handle 12345, got %d", boot.ShmHandle)
	}
	if boot.ShmSize != 4096 {
		t.Errorf("expected size 4096, got %d", boot.ShmSize)
	}
}

func TestReadBootstrap_InvalidJSON(t *testing.T) {
	r := strings.NewReader("not json\n")
	scanner := readerScanner(r)

	_, err := ReadBootstrap(scanner)
	if err == nil {
		t.Fatal("expected error for invalid json")
	}
	if !strings.Contains(err.Error(), "invalid bootstrap message") {
		t.Errorf("expected wrapped error message, got: %v", err)
	}
}

func TestReadBootstrap_EOF(t *testing.T) {
	r := strings.NewReader("")
	scanner := readerScanner(r)

	_, err := ReadBootstrap(scanner)
	if err == nil {
		t.Fatal("expected error for EOF before bootstrap")
	}
	if !strings.Contains(err.Error(), "stdin closed") {
		t.Errorf("expected stdin closed message, got: %v", err)
	}
}

func TestRunWriter_DrainsChannelToRing(t *testing.T) {
	writer, mem := makeTestRingBuffer(t, 4096)

	in := make(chan []byte, 4)
	in <- []byte("hello")
	in <- []byte("world")

	var signalCount atomic.Int32
	signal := func() { signalCount.Add(1) }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunWriter(ctx, in, writer, signal)
		close(done)
	}()

	// give the goroutine a moment to drain
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	// inspect the ring buffer header to verify writes happened
	writePos := *(*uint64)(unsafe.Pointer(&mem[0]))
	// "hello" = 5 + 4 length prefix; "world" = 5 + 4 length prefix; total 18 bytes
	if writePos != 18 {
		t.Errorf("expected write index 18, got %d", writePos)
	}

	// each successful write should have signaled exactly once
	if got := signalCount.Load(); got != 2 {
		t.Errorf("expected 2 signals, got %d", got)
	}
}

func TestRunWriter_StopsOnContextCancel(t *testing.T) {
	writer, _ := makeTestRingBuffer(t, 4096)

	in := make(chan []byte, 1)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		RunWriter(ctx, in, writer, func() {})
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunWriter did not exit on context cancel")
	}
}

func TestRunWriter_StopsOnChannelClose(t *testing.T) {
	writer, _ := makeTestRingBuffer(t, 4096)

	in := make(chan []byte, 1)
	var signalCount atomic.Int32
	signal := func() { signalCount.Add(1) }

	done := make(chan struct{})
	go func() {
		RunWriter(context.Background(), in, writer, signal)
		close(done)
	}()

	close(in)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunWriter did not exit on channel close")
	}
	if got := signalCount.Load(); got != 0 {
		t.Errorf("expected no signals from a closed empty channel, got %d", got)
	}
}

func TestRunWriter_SkipsSignalOnRejectedPayload(t *testing.T) {
	// 32-byte ring; a 32-byte payload would frame to 36 bytes, exceeding
	// capacity, so writer.Write returns false and signal must not fire.
	writer, _ := makeTestRingBuffer(t, 32)

	in := make(chan []byte, 2)
	in <- make([]byte, 32)
	in <- make([]byte, 8)
	close(in)

	var signalCount atomic.Int32
	signal := func() { signalCount.Add(1) }

	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		RunWriter(ctx, in, writer, signal)
		close(done)
	}()

	<-done

	// Only the second (valid) write should have signaled.
	if got := signalCount.Load(); got != 1 {
		t.Errorf("expected 1 signal (rejected writes must not signal), got %d", got)
	}
}

func TestHandleTwitchConnect_AddsClient(t *testing.T) {
	restore := stubEmoteFetcher(t)
	defer restore()

	clients := make(map[string]context.CancelFunc)
	twitchClients := make(map[string]*twitch.Client)
	out := make(chan []byte, 1)
	cmd := control.Command{
		Cmd:           "twitch_connect",
		BroadcasterID: "broadcaster-1",
		UserID:        "user-1",
		Token:         "tok",
		ClientID:      "cid",
	}

	HandleTwitchConnect(context.Background(), cmd, clients, twitchClients, out, func(string, any) {}, zerolog.Nop())

	if _, ok := clients["broadcaster-1"]; !ok {
		t.Fatal("expected client to be registered")
	}
	if _, ok := twitchClients["broadcaster-1"]; !ok {
		t.Fatal("expected twitch client ref to be registered")
	}

	// clean up the goroutine the handler spawned
	clients["broadcaster-1"]()
}

func TestHandleTwitchConnect_RejectsDuplicate(t *testing.T) {
	restore := stubEmoteFetcher(t)
	defer restore()

	clients := make(map[string]context.CancelFunc)
	twitchClients := make(map[string]*twitch.Client)
	out := make(chan []byte, 1)

	var cancelled atomic.Bool
	clients["broadcaster-1"] = func() { cancelled.Store(true) }

	cmd := control.Command{Cmd: "twitch_connect", BroadcasterID: "broadcaster-1"}
	HandleTwitchConnect(context.Background(), cmd, clients, twitchClients, out, func(string, any) {}, zerolog.Nop())

	if cancelled.Load() {
		t.Fatal("existing client cancel was overwritten")
	}
	if len(clients) != 1 {
		t.Fatalf("expected 1 client, got %d", len(clients))
	}
}

func TestHandleTwitchDisconnect_CancelsAndRemoves(t *testing.T) {
	clients := make(map[string]context.CancelFunc)
	twitchClients := make(map[string]*twitch.Client)
	var cancelled atomic.Bool
	clients["broadcaster-1"] = func() { cancelled.Store(true) }
	twitchClients["broadcaster-1"] = twitch.NewClient("broadcaster-1", "", "", "", nil, zerolog.Nop(), nil)

	cmd := control.Command{Cmd: "twitch_disconnect", BroadcasterID: "broadcaster-1"}
	HandleTwitchDisconnect(cmd, clients, twitchClients, zerolog.Nop())

	if !cancelled.Load() {
		t.Fatal("expected client to be cancelled")
	}
	if _, ok := clients["broadcaster-1"]; ok {
		t.Fatal("expected client to be removed from registry")
	}
	if _, ok := twitchClients["broadcaster-1"]; ok {
		t.Fatal("expected twitch client ref to be removed")
	}
}

func TestHandleTwitchDisconnect_NoOpForUnknown(t *testing.T) {
	clients := make(map[string]context.CancelFunc)
	twitchClients := make(map[string]*twitch.Client)
	cmd := control.Command{Cmd: "twitch_disconnect", BroadcasterID: "broadcaster-unknown"}

	// should not panic
	HandleTwitchDisconnect(cmd, clients, twitchClients, zerolog.Nop())
}

func TestHandleTokenRefresh_UpdatesAllClients(t *testing.T) {
	a := twitch.NewClient("b1", "", "old-tok", "", nil, zerolog.Nop(), nil)
	b := twitch.NewClient("b2", "", "old-tok", "", nil, zerolog.Nop(), nil)
	twitchClients := map[string]*twitch.Client{"b1": a, "b2": b}

	cmd := control.Command{Cmd: "token_refresh", Token: "fresh-tok"}
	HandleTokenRefresh(cmd, twitchClients, zerolog.Nop())

	if got := a.AccessToken(); got != "fresh-tok" {
		t.Errorf("client b1 token = %q, want fresh-tok", got)
	}
	if got := b.AccessToken(); got != "fresh-tok" {
		t.Errorf("client b2 token = %q, want fresh-tok", got)
	}
}

func TestHandleTokenRefresh_EmptyTokenIgnored(t *testing.T) {
	c := twitch.NewClient("b1", "", "orig", "", nil, zerolog.Nop(), nil)
	twitchClients := map[string]*twitch.Client{"b1": c}

	HandleTokenRefresh(control.Command{Cmd: "token_refresh"}, twitchClients, zerolog.Nop())

	if got := c.AccessToken(); got != "orig" {
		t.Errorf("token should not change on empty refresh, got %q", got)
	}
}

func TestHandleTokenRefresh_NoClientsNoOp(t *testing.T) {
	twitchClients := make(map[string]*twitch.Client)
	HandleTokenRefresh(control.Command{Cmd: "token_refresh", Token: "tok"}, twitchClients, zerolog.Nop())
}

func TestDispatchCommand_RoutesTokenRefresh(t *testing.T) {
	c := twitch.NewClient("b1", "", "old", "", nil, zerolog.Nop(), nil)
	clients := make(map[string]context.CancelFunc)
	twitchClients := map[string]*twitch.Client{"b1": c}
	out := make(chan []byte, 1)
	cmd := control.Command{Cmd: "token_refresh", Token: "new"}

	DispatchCommand(context.Background(), cmd, clients, twitchClients, out, func(string, any) {}, zerolog.Nop())

	if got := c.AccessToken(); got != "new" {
		t.Errorf("dispatch did not route token_refresh: token = %q", got)
	}
}

func TestDispatchCommand_RoutesConnect(t *testing.T) {
	restore := stubEmoteFetcher(t)
	defer restore()

	clients := make(map[string]context.CancelFunc)
	twitchClients := make(map[string]*twitch.Client)
	out := make(chan []byte, 1)
	cmd := control.Command{Cmd: "twitch_connect", BroadcasterID: "b1"}

	DispatchCommand(context.Background(), cmd, clients, twitchClients, out, func(string, any) {}, zerolog.Nop())

	if _, ok := clients["b1"]; !ok {
		t.Fatal("expected connect to register client")
	}
	clients["b1"]()
}

func TestDispatchCommand_RoutesDisconnect(t *testing.T) {
	var cancelled atomic.Bool
	clients := map[string]context.CancelFunc{
		"b1": func() { cancelled.Store(true) },
	}
	twitchClients := make(map[string]*twitch.Client)
	out := make(chan []byte, 1)
	cmd := control.Command{Cmd: "twitch_disconnect", BroadcasterID: "b1"}

	DispatchCommand(context.Background(), cmd, clients, twitchClients, out, func(string, any) {}, zerolog.Nop())

	if !cancelled.Load() {
		t.Fatal("expected disconnect to cancel client")
	}
}

func TestDispatchCommand_IgnoresUnknown(t *testing.T) {
	clients := make(map[string]context.CancelFunc)
	twitchClients := make(map[string]*twitch.Client)
	out := make(chan []byte, 1)
	cmd := control.Command{Cmd: "frobnicate"}

	// should not panic, should not modify clients
	DispatchCommand(context.Background(), cmd, clients, twitchClients, out, func(string, any) {}, zerolog.Nop())

	if len(clients) != 0 {
		t.Fatalf("expected no clients, got %d", len(clients))
	}
}

// bufLogger returns a zerolog logger that writes JSON lines to the returned
// buffer, for assertions on log content in mod-action scaffolding tests.
func bufLogger() (zerolog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return zerolog.New(&buf), &buf
}

func TestDispatchCommand_RoutesModActions(t *testing.T) {
	clients := make(map[string]context.CancelFunc)
	twitchClients := make(map[string]*twitch.Client)
	out := make(chan []byte, 1)

	notifyCount := 0
	notify := func(typ string, _ any) {
		if typ == "send_chat_result" {
			notifyCount++
		}
	}
	DispatchCommand(context.Background(), control.Command{Cmd: "ban_user", BroadcasterID: "b1", TargetUserID: "t1"}, clients, twitchClients, out, notify, zerolog.Nop())
	DispatchCommand(context.Background(), control.Command{Cmd: "timeout_user", BroadcasterID: "b1", TargetUserID: "t1", DurationSeconds: 60}, clients, twitchClients, out, notify, zerolog.Nop())
	DispatchCommand(context.Background(), control.Command{Cmd: "delete_message", BroadcasterID: "b1", MessageID: "m1"}, clients, twitchClients, out, notify, zerolog.Nop())
	if notifyCount != 3 {
		t.Fatalf("expected 3 moderation result notifications, got %d", notifyCount)
	}

	logger, buf := bufLogger()
	DispatchCommand(context.Background(), control.Command{Cmd: "unban_user", BroadcasterID: "b1", TargetUserID: "t1"}, clients, twitchClients, out, notify, logger)
	if !strings.Contains(buf.String(), "unban_user (scaffold") {
		t.Errorf("expected unban scaffold log, got %s", buf.String())
	}
}

func TestHandleBanUser_MissingTargetIgnored(t *testing.T) {
	logger, buf := bufLogger()
	HandleBanUser(context.Background(), control.Command{Cmd: "ban_user", BroadcasterID: "b1"}, func(string, any) {}, logger)
	if !strings.Contains(buf.String(), "missing required field") {
		t.Errorf("expected warn about missing field, got %s", buf.String())
	}
}

func TestHandleUnbanUser_MissingTargetIgnored(t *testing.T) {
	logger, buf := bufLogger()
	HandleUnbanUser(control.Command{Cmd: "unban_user", BroadcasterID: "b1"}, logger)
	if !strings.Contains(buf.String(), "missing required field") {
		t.Errorf("expected warn about missing field, got %s", buf.String())
	}
}

func TestHandleTimeoutUser_RejectsOutOfRangeDuration(t *testing.T) {
	// Helix enforces 1..1209600 seconds. Values outside this range must be
	// rejected by the scaffold to stay consistent with the real endpoint.
	cases := []struct {
		name     string
		duration int
	}{
		{"zero", 0},
		{"negative", -1},
		{"over_14_days", 1209601},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger, buf := bufLogger()
			HandleTimeoutUser(context.Background(), control.Command{
				Cmd:             "timeout_user",
				BroadcasterID:   "b1",
				UserID:          "u1",
				ClientID:        "cid",
				Token:           "tok",
				TargetUserID:    "t1",
				DurationSeconds: tc.duration,
			}, func(string, any) {}, logger)
			if !strings.Contains(buf.String(), "out of range") {
				t.Errorf("expected out-of-range warn for duration=%d, got %s", tc.duration, buf.String())
			}
		})
	}
}

func TestHandleTimeoutUser_MissingTargetIgnored(t *testing.T) {
	logger, buf := bufLogger()
	HandleTimeoutUser(context.Background(), control.Command{Cmd: "timeout_user", BroadcasterID: "b1", DurationSeconds: 60}, func(string, any) {}, logger)
	if !strings.Contains(buf.String(), "missing required field") {
		t.Errorf("expected warn about missing field, got %s", buf.String())
	}
}

func TestHandleDeleteMessage_MissingMessageIDIgnored(t *testing.T) {
	logger, buf := bufLogger()
	HandleDeleteMessage(context.Background(), control.Command{Cmd: "delete_message", BroadcasterID: "b1"}, func(string, any) {}, logger)
	if !strings.Contains(buf.String(), "missing required field") {
		t.Errorf("expected warn about missing field, got %s", buf.String())
	}
}

func TestRunCommandLoop_DispatchesCommands(t *testing.T) {
	restore := stubEmoteFetcher(t)
	defer restore()

	// Pre-load the scanner with one twitch_connect, then close stdin so
	// scanCommands exits cleanly. The loop itself stops when ctx is cancelled.
	stdin := strings.NewReader(`{"cmd":"twitch_connect","broadcaster_id":"b1"}` + "\n")
	scanner := readerScanner(stdin)

	var stdout bytes.Buffer
	encoder := json.NewEncoder(&stdout)

	out := make(chan []byte, 1)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	// Long period so the heartbeat ticker doesn't pollute stdout during this
	// command-dispatch-focused test.
	go func() { done <- RunCommandLoop(ctx, scanner, encoder, out, zerolog.Nop(), time.Hour) }()

	// give the loop a moment to dispatch the queued command
	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunCommandLoop returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunCommandLoop did not exit")
	}
}

func TestRunCommandLoop_EmitsHeartbeat(t *testing.T) {
	// No commands; let the heartbeat fire at least once. Short period keeps
	// the test fast; the timeout gives the ticker room to fire exactly once.
	scanner := readerScanner(strings.NewReader(""))

	var stdout bytes.Buffer
	encoder := json.NewEncoder(&stdout)

	out := make(chan []byte, 1)
	period := 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), period+100*time.Millisecond)
	defer cancel()

	tsBefore := time.Now().UnixMilli()
	if err := RunCommandLoop(ctx, scanner, encoder, out, zerolog.Nop(), period); err != nil {
		t.Fatalf("RunCommandLoop returned error: %v", err)
	}
	tsAfter := time.Now().UnixMilli()

	// First emitted line is a heartbeat with the enriched payload.
	firstLine := strings.SplitN(stdout.String(), "\n", 2)[0]
	var msg control.Message
	if err := json.Unmarshal([]byte(firstLine), &msg); err != nil {
		t.Fatalf("heartbeat not valid JSON: %v (got: %s)", err, firstLine)
	}
	if msg.Type != "heartbeat" {
		t.Fatalf("expected type=heartbeat, got %q", msg.Type)
	}
	payloadBytes, _ := json.Marshal(msg.Payload)
	var hb control.HeartbeatPayload
	if err := json.Unmarshal(payloadBytes, &hb); err != nil {
		t.Fatalf("heartbeat payload not decodable: %v", err)
	}
	if hb.Counter != 1 {
		t.Errorf("expected counter=1 on first heartbeat, got %d", hb.Counter)
	}
	if hb.TSMs < tsBefore || hb.TSMs > tsAfter {
		t.Errorf("heartbeat ts_ms %d outside expected window [%d, %d]", hb.TSMs, tsBefore, tsAfter)
	}
}

func TestRunCommandLoop_HeartbeatCounterIncreases(t *testing.T) {
	// Run long enough for 3 heartbeats to fire; verify counter is monotonic
	// 1, 2, 3 in the order they appear in stdout.
	scanner := readerScanner(strings.NewReader(""))

	var stdout bytes.Buffer
	encoder := json.NewEncoder(&stdout)

	out := make(chan []byte, 1)
	period := 20 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 3*period+30*time.Millisecond)
	defer cancel()

	if err := RunCommandLoop(ctx, scanner, encoder, out, zerolog.Nop(), period); err != nil {
		t.Fatalf("RunCommandLoop returned error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 heartbeat lines, got %d: %s", len(lines), stdout.String())
	}
	for i, line := range lines[:3] {
		var msg control.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("line %d not valid JSON: %v", i, err)
		}
		payloadBytes, _ := json.Marshal(msg.Payload)
		var hb control.HeartbeatPayload
		if err := json.Unmarshal(payloadBytes, &hb); err != nil {
			t.Fatalf("line %d payload not decodable: %v", i, err)
		}
		wantCounter := uint64(i + 1)
		if hb.Counter != wantCounter {
			t.Errorf("line %d: expected counter=%d, got %d", i, wantCounter, hb.Counter)
		}
	}
}

// errWriter forces encoder.Encode to fail so we can exercise the heartbeat
// error return path.
type errWriter struct{ err error }

func (w *errWriter) Write(_ []byte) (int, error) { return 0, w.err }

func TestRunCommandLoop_PropagatesHeartbeatWriteError(t *testing.T) {
	scanner := readerScanner(strings.NewReader(""))
	encoder := json.NewEncoder(&errWriter{err: errors.New("pipe broken")})

	out := make(chan []byte, 1)
	period := 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), period+100*time.Millisecond)
	defer cancel()

	err := RunCommandLoop(ctx, scanner, encoder, out, zerolog.Nop(), period)
	if err == nil {
		t.Fatal("expected error when encoder.Encode fails")
	}
}

func TestMakeNotify_EncodesMessage(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	notify := makeNotify(json.NewEncoder(&buf), &mu, zerolog.Nop())
	notify("auth_error", "expired token")

	var msg control.Message
	if err := json.Unmarshal(buf.Bytes(), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "auth_error" {
		t.Errorf("expected auth_error, got %s", msg.Type)
	}
	if msg.Payload != "expired token" {
		t.Errorf("expected payload, got %v", msg.Payload)
	}
}

func TestMakeNotify_SerializesConcurrentWrites(t *testing.T) {
	// Hammers notify from many goroutines and verifies the resulting stream
	// is a sequence of well-formed JSON messages, not interleaved garbage.
	// Without the mutex, json.Encoder.Encode calls would race on the shared
	// io.Writer and produce torn output.
	var buf bytes.Buffer
	var mu sync.Mutex
	notify := makeNotify(json.NewEncoder(&buf), &mu, zerolog.Nop())

	const goroutines = 16
	const perGoroutine = 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				notify("auth_error", id)
			}
		}(g)
	}
	wg.Wait()

	dec := json.NewDecoder(&buf)
	count := 0
	for {
		var msg control.Message
		if err := dec.Decode(&msg); err != nil {
			break
		}
		count++
	}
	if count != goroutines*perGoroutine {
		t.Errorf("expected %d well-formed messages, got %d", goroutines*perGoroutine, count)
	}
}

// nopNotify is a stub NotifyFunc used by RunWithIO tests that don't care
// about signaling. Returns nil without side effects.
func nopNotify(_ uintptr) error { return nil }

func TestMakeSignalFunc_NoopsOnZeroEventHandle(t *testing.T) {
	var called atomic.Bool
	notify := func(_ uintptr) error {
		called.Store(true)
		return nil
	}
	signal := MakeSignalFunc(0, notify, zerolog.Nop())
	signal()
	if called.Load() {
		t.Fatal("notify should not be called when eventHandle is 0")
	}
}

func TestMakeSignalFunc_CallsNotifyWithEventHandle(t *testing.T) {
	var got atomic.Uintptr
	notify := func(h uintptr) error {
		got.Store(h)
		return nil
	}
	signal := MakeSignalFunc(0x1234, notify, zerolog.Nop())
	signal()
	if got.Load() != 0x1234 {
		t.Fatalf("expected notify called with 0x1234, got %#x", got.Load())
	}
}

func TestMakeSignalFunc_SwallowsNotifyError(t *testing.T) {
	// A notify that returns an error must not panic or block. The error is
	// logged at Warn level and the writer goroutine continues.
	notify := func(_ uintptr) error {
		return errors.New("set event failed")
	}
	signal := MakeSignalFunc(0x42, notify, zerolog.Nop())
	// Must not panic.
	signal()
}

func TestRunWithIO_AttachError(t *testing.T) {
	// Bootstrap is valid; the AttachFunc fails. RunWithIO must propagate.
	stdin := strings.NewReader(`{"shm_handle": 1, "shm_event_handle": 2, "shm_size": 4096}` + "\n")
	var stdout bytes.Buffer

	fakeAttach := func(uintptr, int) ([]byte, func(), error) {
		return nil, nil, errors.New("attach failed")
	}

	err := RunWithIO(context.Background(), stdin, &stdout, zerolog.Nop(), fakeAttach, nopNotify)
	if err == nil || err.Error() != "attach failed" {
		t.Fatalf("expected attach failed error, got: %v", err)
	}
}

func TestRunWithIO_BootstrapError(t *testing.T) {
	stdin := strings.NewReader("not json\n")
	var stdout bytes.Buffer

	err := RunWithIO(context.Background(), stdin, &stdout, zerolog.Nop(), nil, nopNotify)
	if err == nil {
		t.Fatal("expected bootstrap error")
	}
	if !strings.Contains(err.Error(), "invalid bootstrap message") {
		t.Errorf("expected invalid bootstrap error, got: %v", err)
	}
}

func TestRunWithIO_RingbufOpenError(t *testing.T) {
	// Bootstrap valid, attach returns an undersized buffer that ringbuf.Open
	// rejects. RunWithIO must propagate that error.
	stdin := strings.NewReader(`{"shm_handle": 1, "shm_event_handle": 2, "shm_size": 8}` + "\n")
	var stdout bytes.Buffer

	fakeAttach := func(uintptr, int) ([]byte, func(), error) {
		return make([]byte, 8), func() {}, nil
	}

	err := RunWithIO(context.Background(), stdin, &stdout, zerolog.Nop(), fakeAttach, nopNotify)
	if err == nil {
		t.Fatal("expected ringbuf.Open error for undersized buffer")
	}
}

func TestScanCommands_SkipsInvalidJSON(t *testing.T) {
	stdin := strings.NewReader("not json\n" + `{"cmd":"twitch_disconnect","broadcaster_id":"b1"}` + "\n")
	scanner := readerScanner(stdin)
	cmdCh := make(chan control.Command, 4)

	scanCommands(scanner, cmdCh, zerolog.Nop())

	// only the valid command should make it through
	select {
	case cmd := <-cmdCh:
		if cmd.Cmd != "twitch_disconnect" {
			t.Errorf("expected twitch_disconnect, got %s", cmd.Cmd)
		}
	default:
		t.Fatal("expected one valid command on the channel")
	}

	select {
	case extra := <-cmdCh:
		t.Fatalf("unexpected extra command: %+v", extra)
	default:
	}
}

func TestMakeNotify_LogsOnEncoderError(t *testing.T) {
	encoder := json.NewEncoder(&errWriter{err: errors.New("pipe broken")})
	var mu sync.Mutex
	notify := makeNotify(encoder, &mu, zerolog.Nop())
	// must not panic; error path is exercised internally
	notify("auth_error", "anything")
}

func TestRunWithIO_HappyPath(t *testing.T) {
	// Bootstrap valid, attach returns a real buffer, command loop runs until
	// the context is cancelled. The writer goroutine is spawned and must drain
	// gracefully on shutdown.
	stdin := strings.NewReader(`{"shm_handle": 1, "shm_event_handle": 2, "shm_size": 4096}` + "\n")
	var stdout bytes.Buffer

	mem := make([]byte, testHeaderSize+4096)
	*(*uint64)(unsafe.Pointer(&mem[testCacheLine*2])) = uint64(4096)

	fakeAttach := func(uintptr, int) ([]byte, func(), error) {
		return mem, func() {}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), heartbeatPeriod+200*time.Millisecond)
	defer cancel()

	if err := RunWithIO(ctx, stdin, &stdout, zerolog.Nop(), fakeAttach, nopNotify); err != nil {
		t.Fatalf("RunWithIO returned error: %v", err)
	}

	if !strings.Contains(stdout.String(), `"type":"heartbeat"`) {
		t.Errorf("expected at least one heartbeat in stdout, got: %s", stdout.String())
	}
}

// stubEmoteFetcher replaces emoteFetchFn with a no-op for the duration of a
// test so HandleTwitchConnect doesn't kick off real HTTP fetches against
// 7tv.io / betterttv.net / frankerfacez.com during unit tests.
func stubEmoteFetcher(t *testing.T) func() {
	t.Helper()
	orig := emoteFetchFn
	emoteFetchFn = func(context.Context, control.Command, twitch.Notify, zerolog.Logger) {}
	return func() { emoteFetchFn = orig }
}

func TestBuildFetcher_WithTwitchCredsPopulatesHelixClient(t *testing.T) {
	f := buildFetcher(control.Command{
		Cmd:           "twitch_connect",
		BroadcasterID: "b1",
		ClientID:      "cid",
		Token:         "tok",
	})
	if f.Twitch == nil {
		t.Fatal("expected Twitch client to be set when cid+token are present")
	}
	if f.Twitch.ClientID != "cid" || f.Twitch.AccessToken != "tok" {
		t.Errorf("twitch client credentials not wired: %+v", f.Twitch)
	}
	if f.SevenTV == nil || f.BTTV == nil || f.FFZ == nil {
		t.Error("third-party clients must always be set")
	}
}

func TestBuildFetcher_WithoutTwitchCredsSkipsHelix(t *testing.T) {
	// Missing either ClientID or Token must leave the Twitch client nil so
	// Fetcher.Fetch skips Helix entirely instead of 401-ing every request.
	cases := []control.Command{
		{BroadcasterID: "b1"},
		{BroadcasterID: "b1", ClientID: "cid"},
		{BroadcasterID: "b1", Token: "tok"},
	}
	for i, cmd := range cases {
		f := buildFetcher(cmd)
		if f.Twitch != nil {
			t.Errorf("case %d: expected nil Twitch client, got %+v", i, f.Twitch)
		}
	}
}

func TestFetchAndNotifyEmotes_EmptyBroadcasterDoesNotEmit(t *testing.T) {
	var called atomic.Bool
	notify := func(string, any) { called.Store(true) }

	FetchAndNotifyEmotes(context.Background(), control.Command{}, notify, zerolog.Nop())

	if called.Load() {
		t.Fatal("expected no emit when BroadcasterID is empty")
	}
}

func TestFetchAndNotifyEmotes_EmitsBundleOverControlPlane(t *testing.T) {
	// Spin up an httptest server that plays all four provider endpoints: the
	// integration checks that a real fetcher reaches our channel handler and
	// the resulting Bundle is emitted as a single `emote_bundle` message.
	sevenTVGlobal := `{"id":"g","emotes":[{"id":"7tv1","name":"PepegaAim","data":{"id":"7tv1","name":"PepegaAim","host":{"url":"//cdn.7tv.app/emote/7tv1","files":[{"name":"1x.webp","width":32,"height":32,"format":"WEBP"}]}}}]}`
	sevenTVUser := `{"emote_set":{"id":"u","emotes":[]}}`
	bttvGlobal := `[{"id":"bttv1","code":"monkaS","imageType":"png"}]`
	bttvChannel := `{"channelEmotes":[],"sharedEmotes":[]}`
	ffzGlobal := `{"default_sets":[3],"sets":{"3":{"emoticons":[{"id":1,"name":"ZrehplaR","urls":{"1":"//cdn.frankerfacez.com/1.png"}}]}}}`
	ffzRoom := `{"room":{"set":500},"sets":{"500":{"emoticons":[]}}}`
	twitchGlobalEmotes := `{"data":[],"template":""}`
	twitchChannelEmotes := `{"data":[],"template":""}`
	twitchGlobalBadges := `{"data":[]}`
	twitchChannelBadges := `{"data":[]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/emote-sets/global"):
			_, _ = io.WriteString(w, sevenTVGlobal)
		case strings.Contains(r.URL.Path, "/users/twitch/"):
			_, _ = io.WriteString(w, sevenTVUser)
		case strings.HasSuffix(r.URL.Path, "/cached/emotes/global"):
			_, _ = io.WriteString(w, bttvGlobal)
		case strings.Contains(r.URL.Path, "/cached/users/twitch/"):
			_, _ = io.WriteString(w, bttvChannel)
		case strings.HasSuffix(r.URL.Path, "/set/global"):
			_, _ = io.WriteString(w, ffzGlobal)
		case strings.Contains(r.URL.Path, "/room/id/"):
			_, _ = io.WriteString(w, ffzRoom)
		case strings.HasSuffix(r.URL.Path, "/chat/emotes/global"):
			_, _ = io.WriteString(w, twitchGlobalEmotes)
		case strings.HasPrefix(r.URL.Path, "/chat/emotes"):
			_, _ = io.WriteString(w, twitchChannelEmotes)
		case strings.HasSuffix(r.URL.Path, "/chat/badges/global"):
			_, _ = io.WriteString(w, twitchGlobalBadges)
		case strings.HasPrefix(r.URL.Path, "/chat/badges"):
			_, _ = io.WriteString(w, twitchChannelBadges)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	f := &emotes.Fetcher{
		Twitch:  &emotes.TwitchClient{BaseURL: srv.URL, ClientID: "cid", AccessToken: "tok"},
		SevenTV: &emotes.SevenTVClient{BaseURL: srv.URL},
		BTTV:    &emotes.BTTVClient{BaseURL: srv.URL},
		FFZ:     &emotes.FFZClient{BaseURL: srv.URL},
	}

	var gotType string
	var gotPayload any
	notify := func(mt string, p any) { gotType = mt; gotPayload = p }

	fetchAndEmit(context.Background(), f, "b1", notify, zerolog.Nop())

	if gotType != "emote_bundle" {
		t.Fatalf("expected type=emote_bundle, got %q", gotType)
	}
	bundle, ok := gotPayload.(emotes.Bundle)
	if !ok {
		t.Fatalf("expected emotes.Bundle payload, got %T", gotPayload)
	}
	if len(bundle.SevenTVGlobal.Emotes) == 0 {
		t.Error("expected 7TV global emotes")
	}
	if len(bundle.BTTVGlobal.Emotes) == 0 {
		t.Error("expected BTTV global emotes")
	}
	if len(bundle.FFZGlobal.Emotes) == 0 {
		t.Error("expected FFZ global emotes")
	}

	// JSON round-trip has to succeed: this is the on-the-wire contract.
	raw, err := json.Marshal(control.Message{Type: gotType, Payload: bundle})
	if err != nil {
		t.Fatalf("bundle message failed to marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"type":"emote_bundle"`) {
		t.Errorf("marshalled message missing type: %s", raw)
	}
	if !strings.Contains(string(raw), `"seventv_global"`) {
		t.Errorf("marshalled bundle missing seventv_global field: %s", raw)
	}
}

func TestFetchAndNotifyEmotes_CancelledContextSuppressesEmit(t *testing.T) {
	// Use a server that blocks until the context is cancelled; confirm no
	// emit fires when the context is already done by the time Fetch returns.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	f := &emotes.Fetcher{
		SevenTV: &emotes.SevenTVClient{BaseURL: srv.URL},
		BTTV:    &emotes.BTTVClient{BaseURL: srv.URL},
		FFZ:     &emotes.FFZClient{BaseURL: srv.URL},
	}

	var called atomic.Bool
	notify := func(string, any) { called.Store(true) }

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fetchAndEmit(ctx, f, "b1", notify, zerolog.Nop())

	if called.Load() {
		t.Fatal("expected no emit when context is cancelled before fetch returns")
	}
}

func TestProviderError_JSONIncludesErrorString(t *testing.T) {
	pe := emotes.ProviderError{
		Provider: emotes.ProviderBTTV,
		Scope:    emotes.ScopeGlobal,
		Err:      errors.New("network down"),
	}
	raw, err := json.Marshal(pe)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, `"provider":"bttv"`) ||
		!strings.Contains(got, `"scope":"global"`) ||
		!strings.Contains(got, `"error":"network down"`) {
		t.Errorf("unexpected JSON: %s", got)
	}
}

func TestHandleKickConnect_AddsClient(t *testing.T) {
	clients := make(map[string]context.CancelFunc)
	out := make(chan []byte, 1)
	cmd := control.Command{
		Cmd:        "kick_connect",
		ChatroomID: 12345,
	}

	HandleKickConnect(context.Background(), cmd, clients, out, zerolog.Nop())

	if _, ok := clients["kick:12345"]; !ok {
		t.Fatal("expected kick client to be registered")
	}
	clients["kick:12345"]()
}

func TestHandleKickConnect_RejectsDuplicate(t *testing.T) {
	clients := make(map[string]context.CancelFunc)
	out := make(chan []byte, 1)

	var cancelled atomic.Bool
	clients["kick:12345"] = func() { cancelled.Store(true) }

	cmd := control.Command{Cmd: "kick_connect", ChatroomID: 12345}
	HandleKickConnect(context.Background(), cmd, clients, out, zerolog.Nop())

	if cancelled.Load() {
		t.Fatal("existing kick client cancel was overwritten")
	}
	if len(clients) != 1 {
		t.Fatalf("expected 1 client, got %d", len(clients))
	}
}

func TestHandleKickConnect_RejectsZeroChatroomID(t *testing.T) {
	clients := make(map[string]context.CancelFunc)
	out := make(chan []byte, 1)
	cmd := control.Command{Cmd: "kick_connect", ChatroomID: 0}

	HandleKickConnect(context.Background(), cmd, clients, out, zerolog.Nop())

	if len(clients) != 0 {
		t.Fatalf("expected no clients for zero chatroom ID, got %d", len(clients))
	}
}

func TestHandleKickConnect_RejectsNegativeChatroomID(t *testing.T) {
	clients := make(map[string]context.CancelFunc)
	out := make(chan []byte, 1)
	cmd := control.Command{Cmd: "kick_connect", ChatroomID: -1}

	HandleKickConnect(context.Background(), cmd, clients, out, zerolog.Nop())

	if len(clients) != 0 {
		t.Fatalf("expected no clients for negative chatroom ID, got %d", len(clients))
	}
}

func TestHandleKickDisconnect_CancelsAndRemoves(t *testing.T) {
	clients := make(map[string]context.CancelFunc)
	var cancelled atomic.Bool
	clients["kick:12345"] = func() { cancelled.Store(true) }

	cmd := control.Command{Cmd: "kick_disconnect", ChatroomID: 12345}
	HandleKickDisconnect(cmd, clients, zerolog.Nop())

	if !cancelled.Load() {
		t.Fatal("expected kick client to be cancelled")
	}
	if _, ok := clients["kick:12345"]; ok {
		t.Fatal("expected kick client to be removed from registry")
	}
}

func TestHandleKickDisconnect_NoOpForUnknown(t *testing.T) {
	clients := make(map[string]context.CancelFunc)
	cmd := control.Command{Cmd: "kick_disconnect", ChatroomID: 99999}

	HandleKickDisconnect(cmd, clients, zerolog.Nop())
}

func TestDispatchCommand_RoutesKickConnect(t *testing.T) {
	clients := make(map[string]context.CancelFunc)
	twitchClients := make(map[string]*twitch.Client)
	out := make(chan []byte, 1)
	cmd := control.Command{Cmd: "kick_connect", ChatroomID: 777}

	DispatchCommand(context.Background(), cmd, clients, twitchClients, out, func(string, any) {}, zerolog.Nop())

	if _, ok := clients["kick:777"]; !ok {
		t.Fatal("expected kick_connect to register client")
	}
	clients["kick:777"]()
}

func TestDispatchCommand_RoutesKickDisconnect(t *testing.T) {
	var cancelled atomic.Bool
	clients := map[string]context.CancelFunc{
		"kick:777": func() { cancelled.Store(true) },
	}
	twitchClients := make(map[string]*twitch.Client)
	out := make(chan []byte, 1)
	cmd := control.Command{Cmd: "kick_disconnect", ChatroomID: 777}

	DispatchCommand(context.Background(), cmd, clients, twitchClients, out, func(string, any) {}, zerolog.Nop())

	if !cancelled.Load() {
		t.Fatal("expected kick_disconnect to cancel client")
	}
}

func TestHandleYouTubeConnect_AddsClient(t *testing.T) {
	clients := make(map[string]context.CancelFunc)
	out := make(chan []byte, 1)
	cmd := control.Command{
		Cmd:        "youtube_connect",
		LiveChatID: "abc",
		APIKey:     "key",
	}

	HandleYouTubeConnect(context.Background(), cmd, clients, out, func(string, any) {}, zerolog.Nop())

	if _, ok := clients["yt:abc"]; !ok {
		t.Fatal("expected youtube client to be registered")
	}
	clients["yt:abc"]()
}

func TestHandleYouTubeConnect_RejectsMissingChatID(t *testing.T) {
	clients := make(map[string]context.CancelFunc)
	out := make(chan []byte, 1)
	cmd := control.Command{Cmd: "youtube_connect"}

	HandleYouTubeConnect(context.Background(), cmd, clients, out, func(string, any) {}, zerolog.Nop())

	if len(clients) != 0 {
		t.Fatalf("expected no clients for missing chat_id, got %d", len(clients))
	}
}

func TestHandleYouTubeConnect_RejectsDuplicate(t *testing.T) {
	clients := make(map[string]context.CancelFunc)
	out := make(chan []byte, 1)

	var cancelled atomic.Bool
	clients["yt:abc"] = func() { cancelled.Store(true) }

	cmd := control.Command{Cmd: "youtube_connect", LiveChatID: "abc", APIKey: "key"}
	HandleYouTubeConnect(context.Background(), cmd, clients, out, func(string, any) {}, zerolog.Nop())

	if cancelled.Load() {
		t.Fatal("existing youtube client cancel was overwritten")
	}
	if len(clients) != 1 {
		t.Fatalf("expected 1 client, got %d", len(clients))
	}
}

func TestHandleYouTubeDisconnect_CancelsAndRemoves(t *testing.T) {
	clients := make(map[string]context.CancelFunc)
	var cancelled atomic.Bool
	clients["yt:abc"] = func() { cancelled.Store(true) }

	cmd := control.Command{Cmd: "youtube_disconnect", LiveChatID: "abc"}
	HandleYouTubeDisconnect(cmd, clients, zerolog.Nop())

	if !cancelled.Load() {
		t.Fatal("expected youtube client to be cancelled")
	}
	if _, ok := clients["yt:abc"]; ok {
		t.Fatal("expected youtube client to be removed from registry")
	}
}

func TestHandleYouTubeDisconnect_NoOpForUnknown(t *testing.T) {
	clients := make(map[string]context.CancelFunc)
	cmd := control.Command{Cmd: "youtube_disconnect", LiveChatID: "missing"}

	HandleYouTubeDisconnect(cmd, clients, zerolog.Nop())
}

func TestHandleYouTubeDisconnect_RejectsMissingChatID(t *testing.T) {
	clients := make(map[string]context.CancelFunc)
	cmd := control.Command{Cmd: "youtube_disconnect"}

	HandleYouTubeDisconnect(cmd, clients, zerolog.Nop())

	if len(clients) != 0 {
		t.Fatalf("expected no client mutations, got %d", len(clients))
	}
}

func TestDispatchCommand_RoutesYouTubeConnect(t *testing.T) {
	clients := make(map[string]context.CancelFunc)
	twitchClients := make(map[string]*twitch.Client)
	out := make(chan []byte, 1)
	cmd := control.Command{Cmd: "youtube_connect", LiveChatID: "xyz", APIKey: "k"}

	DispatchCommand(context.Background(), cmd, clients, twitchClients, out, func(string, any) {}, zerolog.Nop())

	if _, ok := clients["yt:xyz"]; !ok {
		t.Fatal("expected youtube_connect to register client")
	}
	clients["yt:xyz"]()
}

func TestDispatchCommand_RoutesYouTubeDisconnect(t *testing.T) {
	var cancelled atomic.Bool
	clients := map[string]context.CancelFunc{
		"yt:xyz": func() { cancelled.Store(true) },
	}
	twitchClients := make(map[string]*twitch.Client)
	out := make(chan []byte, 1)
	cmd := control.Command{Cmd: "youtube_disconnect", LiveChatID: "xyz"}

	DispatchCommand(context.Background(), cmd, clients, twitchClients, out, func(string, any) {}, zerolog.Nop())

	if !cancelled.Load() {
		t.Fatal("expected youtube_disconnect to cancel client")
	}
}

func TestHandleSendChatMessage_MissingFieldsRepliesWithRequestID(t *testing.T) {
	cases := []struct {
		name string
		cmd  control.Command
		want string
	}{
		{
			"no broadcaster",
			control.Command{Cmd: "send_chat_message", UserID: "u", ClientID: "c", Token: "t", Message: "hi", RequestID: 7},
			"missing broadcaster",
		},
		{
			"empty message",
			control.Command{Cmd: "send_chat_message", BroadcasterID: "b", UserID: "u", ClientID: "c", Token: "t", RequestID: 8},
			"empty message",
		},
		{
			"oversize",
			control.Command{Cmd: "send_chat_message", BroadcasterID: "b", UserID: "u", ClientID: "c", Token: "t", Message: strings.Repeat("a", twitch.MaxChatMessageBytes+1), RequestID: 9},
			"exceeds",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got SendChatResultPayload
			var typ string
			notify := func(t string, p any) {
				typ = t
				got = p.(SendChatResultPayload)
			}
			HandleSendChatMessage(context.Background(), tc.cmd, notify, zerolog.Nop())
			if typ != "send_chat_result" {
				t.Fatalf("expected send_chat_result, got %q", typ)
			}
			if got.RequestID != tc.cmd.RequestID {
				t.Errorf("request_id not echoed: want %d got %d", tc.cmd.RequestID, got.RequestID)
			}
			if got.Ok {
				t.Error("expected Ok=false")
			}
			if !strings.Contains(got.ErrorMessage, tc.want) {
				t.Errorf("expected error containing %q, got %q", tc.want, got.ErrorMessage)
			}
		})
	}
}

func TestDispatchCommand_RoutesSendChatMessage(t *testing.T) {
	var typ string
	notify := func(t string, p any) {
		typ = t
		_ = p
	}
	cmd := control.Command{Cmd: "send_chat_message", RequestID: 11}
	DispatchCommand(context.Background(), cmd, map[string]context.CancelFunc{}, make(map[string]*twitch.Client), make(chan []byte, 1), notify, zerolog.Nop())
	if typ != "send_chat_result" {
		t.Fatalf("expected dispatch to invoke HandleSendChatMessage, got notify type %q", typ)
	}
}

func TestHandleSendChatMessage_SuccessEchoesMessageID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/messages" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"message_id":"abc-123","is_sent":true}]}`)
	}))
	defer srv.Close()
	prev := sendChatHelixBase
	sendChatHelixBase = srv.URL
	defer func() { sendChatHelixBase = prev }()

	var got SendChatResultPayload
	notify := func(_ string, p any) { got = p.(SendChatResultPayload) }
	HandleSendChatMessage(context.Background(), control.Command{
		Cmd:           "send_chat_message",
		BroadcasterID: "b",
		UserID:        "u",
		ClientID:      "c",
		Token:         "t",
		Message:       "hello",
		RequestID:     42,
	}, notify, zerolog.Nop())

	if !got.Ok || got.MessageID != "abc-123" || got.RequestID != 42 {
		t.Fatalf("unexpected payload: %+v", got)
	}
}

func TestHandleSendChatMessage_DropReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"is_sent":false,"drop_reason":{"code":"msg_duplicate","message":"duplicate"}}]}`)
	}))
	defer srv.Close()
	prev := sendChatHelixBase
	sendChatHelixBase = srv.URL
	defer func() { sendChatHelixBase = prev }()

	var got SendChatResultPayload
	notify := func(_ string, p any) { got = p.(SendChatResultPayload) }
	HandleSendChatMessage(context.Background(), control.Command{
		Cmd:           "send_chat_message",
		BroadcasterID: "b", UserID: "u", ClientID: "c", Token: "t",
		Message: "x", RequestID: 5,
	}, notify, zerolog.Nop())

	if got.Ok || got.DropCode != "msg_duplicate" || got.RequestID != 5 {
		t.Fatalf("unexpected payload: %+v", got)
	}
}

func TestHandleSendChatMessage_HelixError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"Unauthorized","status":401,"message":"oops"}`)
	}))
	defer srv.Close()
	prev := sendChatHelixBase
	sendChatHelixBase = srv.URL
	defer func() { sendChatHelixBase = prev }()

	var got SendChatResultPayload
	notify := func(_ string, p any) { got = p.(SendChatResultPayload) }
	HandleSendChatMessage(context.Background(), control.Command{
		Cmd:           "send_chat_message",
		BroadcasterID: "b", UserID: "u", ClientID: "c", Token: "t",
		Message: "x", RequestID: 99,
	}, notify, zerolog.Nop())

	if got.Ok || got.ErrorMessage == "" || got.RequestID != 99 {
		t.Fatalf("unexpected payload: %+v", got)
	}
}

func TestHandleYouTubeSendMessage_SuccessEchoesMessageID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/liveChat/messages" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer yt-tok" {
			t.Fatalf("missing/incorrect bearer auth: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"yt-msg-1"}`)
	}))
	defer srv.Close()
	prev := youtubeSendAPIBase
	youtubeSendAPIBase = srv.URL
	defer func() { youtubeSendAPIBase = prev }()

	var got SendChatResultPayload
	notify := func(_ string, p any) { got = p.(SendChatResultPayload) }
	HandleYouTubeSendMessage(context.Background(), control.Command{
		Cmd:        "youtube_send_message",
		LiveChatID: "abc",
		Token:      "yt-tok",
		Message:    "hi",
		RequestID:  77,
	}, notify, zerolog.Nop())

	if !got.Ok || got.MessageID != "yt-msg-1" || got.RequestID != 77 {
		t.Fatalf("unexpected payload: %+v", got)
	}
}

func TestHandleYouTubeSendMessage_MissingFieldsReplies(t *testing.T) {
	cases := []struct {
		name string
		cmd  control.Command
	}{
		{"missing chat id", control.Command{Cmd: "youtube_send_message", Token: "t", Message: "hi", RequestID: 1}},
		{"missing token", control.Command{Cmd: "youtube_send_message", LiveChatID: "abc", Message: "hi", RequestID: 2}},
		{"empty message", control.Command{Cmd: "youtube_send_message", LiveChatID: "abc", Token: "t", RequestID: 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got SendChatResultPayload
			notify := func(_ string, p any) { got = p.(SendChatResultPayload) }
			HandleYouTubeSendMessage(context.Background(), tc.cmd, notify, zerolog.Nop())
			if got.Ok {
				t.Fatalf("expected ok=false, got %+v", got)
			}
			if got.ErrorMessage == "" {
				t.Fatalf("expected error message, got %+v", got)
			}
			if got.RequestID != tc.cmd.RequestID {
				t.Fatalf("request_id = %d, want %d", got.RequestID, tc.cmd.RequestID)
			}
		})
	}
}

func TestHandleYouTubeSendMessage_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":403,"message":"chat ended","errors":[{"reason":"liveChatEnded"}]}}`)
	}))
	defer srv.Close()
	prev := youtubeSendAPIBase
	youtubeSendAPIBase = srv.URL
	defer func() { youtubeSendAPIBase = prev }()

	var got SendChatResultPayload
	notify := func(_ string, p any) { got = p.(SendChatResultPayload) }
	HandleYouTubeSendMessage(context.Background(), control.Command{
		Cmd: "youtube_send_message", LiveChatID: "abc", Token: "t", Message: "hi", RequestID: 13,
	}, notify, zerolog.Nop())

	if got.Ok || got.DropMessage == "" || got.RequestID != 13 {
		t.Fatalf("unexpected payload: %+v", got)
	}
	if got.DropCode != "youtube_api" {
		t.Fatalf("drop_code = %q, want youtube_api", got.DropCode)
	}
}

func TestHandleYouTubeSendMessage_UnauthorizedSurfacesStableCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":401,"message":"bad token","errors":[{"reason":"authError"}]}}`)
	}))
	defer srv.Close()
	prev := youtubeSendAPIBase
	youtubeSendAPIBase = srv.URL
	defer func() { youtubeSendAPIBase = prev }()

	var got SendChatResultPayload
	notify := func(_ string, p any) { got = p.(SendChatResultPayload) }
	HandleYouTubeSendMessage(context.Background(), control.Command{
		Cmd: "youtube_send_message", LiveChatID: "abc", Token: "t", Message: "hi", RequestID: 21,
	}, notify, zerolog.Nop())

	if got.Ok || got.DropCode != "unauthorized" {
		t.Fatalf("expected drop_code=unauthorized, got %+v", got)
	}
}

func TestHandleYouTubeSendMessage_QuotaSurfacesStableCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":403,"message":"out of quota","errors":[{"reason":"quotaExceeded"}]}}`)
	}))
	defer srv.Close()
	prev := youtubeSendAPIBase
	youtubeSendAPIBase = srv.URL
	defer func() { youtubeSendAPIBase = prev }()

	var got SendChatResultPayload
	notify := func(_ string, p any) { got = p.(SendChatResultPayload) }
	HandleYouTubeSendMessage(context.Background(), control.Command{
		Cmd: "youtube_send_message", LiveChatID: "abc", Token: "t", Message: "hi", RequestID: 22,
	}, notify, zerolog.Nop())

	if got.Ok || got.DropCode != "quota_exceeded" {
		t.Fatalf("expected drop_code=quota_exceeded, got %+v", got)
	}
}

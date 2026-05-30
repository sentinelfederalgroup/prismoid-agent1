// Virtualized chat renderer. Uses Pretext for exact pixel-perfect message
// heights (no DOM reflow), binary-searches the visible range, and keeps
// the mounted DOM bounded to the viewport window + overscan. ADR 21 +
// docs/frontend.md: one viewport signal per frame, message buffer lives
// outside Solid reactivity.

import {
  Component,
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  onCleanup,
  onMount,
} from "solid-js";
import { listen } from "@tauri-apps/api/event";
import {
  addMessages,
  confirmPendingId,
  failPending,
  getMessage,
  messageRevision,
  retryPending,
  viewport,
  type ChatMessage,
} from "../stores/chatStore";
import { observeAgentMessages } from "../stores/agentStore";
import { sendMessage, type SendMessageError } from "../lib/twitchAuth";
import { formatSendError, toSendError } from "../lib/messageInput";
import {
  loadBadgeBundle,
  resolveBadge,
  badgeRevision,
  type EmoteBundle,
} from "../stores/badgeStore";
import {
  BADGE_GAP_PX,
  BADGE_SIZE_PX,
  MESSAGE_FONT_FAMILY,
  MESSAGE_FONT_SIZE_PX,
  MESSAGE_LINE_HEIGHT,
  MESSAGE_PADDING_X,
  MESSAGE_PADDING_Y,
  measureMessageHeight,
  prepareMessage,
  type PreparedMessage,
} from "../lib/messageLayout";
import { normalizeUserColor } from "../lib/messageStyle";
import type { MessagePiece } from "../lib/emoteSpans";

const OVERSCAN = 6;
const STICK_THRESHOLD = 40;

function renderPiece(piece: MessagePiece) {
  if (piece.kind === "text") {
    return <span>{piece.text}</span>;
  }
  const { primary, overlays } = piece;
  return (
    <span
      style={{
        display: "inline-block",
        position: "relative",
        width: `${primary.width}px`,
        height: `${primary.height}px`,
        "vertical-align": "middle",
      }}
    >
      <img
        src={primary.emote.url_1x}
        alt={primary.emote.code}
        title={primary.emote.code}
        width={primary.width}
        height={primary.height}
        draggable={false}
        style={{ display: "block" }}
      />
      <For each={overlays}>
        {(overlay) => (
          <img
            src={overlay.emote.url_1x}
            alt={overlay.emote.code}
            title={overlay.emote.code}
            width={overlay.width}
            height={overlay.height}
            draggable={false}
            style={{
              position: "absolute",
              left: `${(primary.width - overlay.width) / 2}px`,
              top: `${(primary.height - overlay.height) / 2}px`,
              "pointer-events": "none",
            }}
          />
        )}
      </For>
    </span>
  );
}

interface PositionedMessage {
  monoIndex: number;
  msg: ChatMessage;
  prepared: PreparedMessage;
  top: number;
  height: number;
}

interface CachedEntry {
  prepared: PreparedMessage;
  height: number;
  measuredWidth: number;
}

export interface ChatFeedProps {
  login: string;
}

const ChatFeed: Component<ChatFeedProps> = (props) => {
  let containerRef: HTMLDivElement | undefined;
  const entryCache = new Map<number, CachedEntry>();
  let lastBadgeRev = 0;
  let lastMessageRev = 0;

  const [width, setWidth] = createSignal(0);
  const [viewportHeight, setViewportHeight] = createSignal(0);
  const [scrollTop, setScrollTop] = createSignal(0);
  const [stickToBottom, setStickToBottom] = createSignal(true);
  const [fontsLoaded, setFontsLoaded] = createSignal(false);

  let scrollRafPending = false;

  const handleScroll = () => {
    if (!containerRef) return;
    if (scrollRafPending) return;
    scrollRafPending = true;
    requestAnimationFrame(() => {
      scrollRafPending = false;
      if (!containerRef) return;
      const top = containerRef.scrollTop;
      const clientH = containerRef.clientHeight;
      const totalH = containerRef.scrollHeight;
      setScrollTop(top);
      setStickToBottom(totalH - top - clientH <= STICK_THRESHOLD);
    });
  };

  const layout = createMemo<{
    messages: PositionedMessage[];
    totalHeight: number;
  }>(() => {
    const v = viewport();
    const w = width();
    // Read the badge revision so bundle reloads invalidate the prepared
    // cache and trigger a full re-measure.
    const rev = badgeRevision();
    // Read the message-mutation revision so reconciled or failed
    // pending entries re-render with their new content.
    const msgRev = messageRevision();
    if (!fontsLoaded() || w <= 0 || v.count === 0) {
      return { messages: [], totalHeight: 0 };
    }

    const liveStart = v.start;
    const liveEnd = v.start + v.count;

    for (const key of entryCache.keys()) {
      if (key < liveStart) entryCache.delete(key);
    }
    if (rev !== lastBadgeRev) {
      entryCache.clear();
      lastBadgeRev = rev;
    }
    if (msgRev !== lastMessageRev) {
      entryCache.clear();
      lastMessageRev = msgRev;
    }

    const messages: PositionedMessage[] = new Array(v.count);
    let y = 0;
    let writeIdx = 0;
    for (let mono = liveStart; mono < liveEnd; mono++) {
      const msg = getMessage(mono);
      if (!msg) continue;
      let entry = entryCache.get(mono);
      if (entry === undefined || entry.measuredWidth !== w) {
        const prepared =
          entry !== undefined
            ? entry.prepared
            : prepareMessage(msg, resolveBadge);
        const height = measureMessageHeight(prepared.prepared, w);
        entry = { prepared, height, measuredWidth: w };
        entryCache.set(mono, entry);
      }
      messages[writeIdx++] = {
        monoIndex: mono,
        msg,
        prepared: entry.prepared,
        top: y,
        height: entry.height,
      };
      y += entry.height;
    }
    messages.length = writeIdx;
    return { messages, totalHeight: y };
  });

  const visibleRange = createMemo<{ start: number; end: number }>(() => {
    const { messages } = layout();
    const top = scrollTop();
    const vh = viewportHeight();
    if (messages.length === 0 || vh === 0) return { start: 0, end: 0 };

    const minY = Math.max(0, top);
    const maxY = top + vh;

    let low = 0;
    let high = messages.length;
    while (low < high) {
      const mid = (low + high) >> 1;
      if (messages[mid]!.top + messages[mid]!.height > minY) high = mid;
      else low = mid + 1;
    }
    const start = Math.max(0, low - OVERSCAN);

    low = start;
    high = messages.length;
    while (low < high) {
      const mid = (low + high) >> 1;
      if (messages[mid]!.top >= maxY) high = mid;
      else low = mid + 1;
    }
    const end = Math.min(messages.length, low + OVERSCAN);
    return { start, end };
  });

  const visibleMessages = createMemo<PositionedMessage[]>(() => {
    const { messages } = layout();
    const { start, end } = visibleRange();
    return messages.slice(start, end);
  });

  createEffect(() => {
    const { totalHeight } = layout();
    if (!containerRef) return;
    if (!stickToBottom()) return;
    const vh = viewportHeight();
    if (vh === 0) return;
    const target = Math.max(0, totalHeight - vh);
    if (Math.abs(containerRef.scrollTop - target) > 0.5) {
      containerRef.scrollTop = target;
    }
  });

  onMount(() => {
    if (!containerRef) return;

    const ro = new ResizeObserver(() => {
      if (!containerRef) return;
      setWidth(containerRef.clientWidth);
      setViewportHeight(containerRef.clientHeight);
    });
    ro.observe(containerRef);
    setWidth(containerRef.clientWidth);
    setViewportHeight(containerRef.clientHeight);

    // Pretext uses canvas measureText; heights are only trustworthy once
    // webfonts are decoded. Fall back to immediate readiness in headless
    // environments that lack document.fonts, and on rejection/throw so the
    // UI never gets stuck waiting on a font promise that never settles.
    const fonts = (document as Document & { fonts?: FontFaceSet }).fonts;
    if (fonts && typeof fonts.ready?.then === "function") {
      fonts.ready
        .then(() => setFontsLoaded(true))
        .catch(() => setFontsLoaded(true));
    } else {
      setFontsLoaded(true);
    }

    let unlisten: (() => void) | undefined;
    let unlistenBundle: (() => void) | undefined;
    listen<ChatMessage[]>("chat_messages", (event) => {
      addMessages(event.payload);
      observeAgentMessages(event.payload, props.login);
    })
      .then((fn) => {
        unlisten = fn;
      })
      .catch((err) =>
        console.error("failed to listen for chat messages:", err),
      );

    listen<EmoteBundle>("emote_bundle", (event) => {
      loadBadgeBundle(event.payload);
    })
      .then((fn) => {
        unlistenBundle = fn;
      })
      .catch((err) =>
        console.error("failed to listen for emote bundles:", err),
      );

    onCleanup(() => {
      ro.disconnect();
      unlisten?.();
      unlistenBundle?.();
    });
  });

  return (
    <div
      ref={(el) => (containerRef = el)}
      onScroll={handleScroll}
      style={{
        flex: 1,
        "overflow-y": "auto",
        position: "relative",
        "will-change": "transform",
      }}
    >
      <div
        style={{
          position: "relative",
          height: `${layout().totalHeight}px`,
          width: "100%",
        }}
      >
        <For each={visibleMessages()}>
          {(item) => {
            const status = () => item.msg.status;
            const onRetry = () => {
              const localId = item.msg.local_id;
              if (!localId) return;
              const text = retryPending(localId);
              if (!text) return;
              sendMessage(text).then(
                (ok) => confirmPendingId(localId, ok.message_id),
                (raw) => {
                  const err = toSendError(raw);
                  failPending(
                    localId,
                    typeof err === "string"
                      ? err
                      : formatSendError(err as SendMessageError),
                  );
                },
              );
            };
            return (
              <div
                style={{
                  position: "absolute",
                  top: 0,
                  left: 0,
                  right: 0,
                  transform: `translateY(${item.top}px)`,
                  height: `${item.height}px`,
                  padding: `${MESSAGE_PADDING_Y / 2}px ${MESSAGE_PADDING_X}px`,
                  "line-height": `${MESSAGE_LINE_HEIGHT}px`,
                  "box-sizing": "border-box",
                  "font-family": MESSAGE_FONT_FAMILY,
                  "font-size": `${MESSAGE_FONT_SIZE_PX}px`,
                  "white-space": "normal",
                  "overflow-wrap": "break-word",
                  opacity: status() === "pending" ? 0.55 : 1,
                  "border-left":
                    status() === "failed"
                      ? "2px solid #f5a3a3"
                      : "2px solid transparent",
                  cursor: status() === "failed" ? "pointer" : "default",
                }}
                title={
                  status() === "failed"
                    ? `${item.msg.error_message ?? "send failed"} (click or press Enter to retry)`
                    : undefined
                }
                role={status() === "failed" ? "button" : undefined}
                tabIndex={status() === "failed" ? 0 : undefined}
                aria-label={
                  status() === "failed"
                    ? `Send failed: ${item.msg.error_message ?? "unknown error"}. Activate to retry.`
                    : undefined
                }
                onClick={status() === "failed" ? onRetry : undefined}
                onKeyDown={
                  status() === "failed"
                    ? (e: KeyboardEvent) => {
                        if (e.key === "Enter" || e.key === " ") {
                          e.preventDefault();
                          onRetry();
                        }
                      }
                    : undefined
                }
              >
                <Show when={item.prepared.timestamp}>
                  <span
                    style={{
                      color: "#6e6e72",
                      "white-space": "pre",
                    }}
                  >
                    {item.prepared.timestamp}
                  </span>
                </Show>
                <For each={item.prepared.badges}>
                  {(b) => (
                    <img
                      src={b.badge.url_1x}
                      srcset={
                        b.badge.url_2x
                          ? `${b.badge.url_1x} 1x, ${b.badge.url_2x} 2x${
                              b.badge.url_4x ? `, ${b.badge.url_4x} 4x` : ""
                            }`
                          : undefined
                      }
                      alt={b.badge.title}
                      title={b.badge.title}
                      width={BADGE_SIZE_PX}
                      height={BADGE_SIZE_PX}
                      draggable={false}
                      style={{
                        display: "inline-block",
                        "vertical-align": "middle",
                        "margin-right": `${BADGE_GAP_PX}px`,
                      }}
                    />
                  )}
                </For>
                <span
                  style={{
                    color: normalizeUserColor(item.msg.color),
                    "font-weight": 700,
                    // Keep DOM in lockstep with Pretext's `break: "never"`
                    // on the username segment so heights stay accurate even
                    // for very long display names.
                    "white-space": "nowrap",
                  }}
                >
                  {item.msg.display_name}
                </span>
                <span style={{ color: "#adadb8" }}>: </span>
                <For each={item.prepared.pieces}>
                  {(piece) => renderPiece(piece)}
                </For>
              </div>
            );
          }}
        </For>
      </div>
    </div>
  );
};

export default ChatFeed;

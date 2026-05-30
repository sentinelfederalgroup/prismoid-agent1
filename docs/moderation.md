# Moderation

## Actions

| Action          | Twitch                                         | YouTube                                         |
| --------------- | ---------------------------------------------- | ----------------------------------------------- |
| Delete message  | Helix `DELETE /moderation/chat`                | `liveChatMessages.delete`                       |
| Timeout         | Helix `POST /moderation/bans` with `duration`  | `liveChatBans.insert` with `banDurationSeconds` |
| Ban (permanent) | Helix `POST /moderation/bans` without duration | `liveChatBans.insert` without duration          |
| Unban           | Helix `DELETE /moderation/bans`                | `liveChatBans.delete`                           |

## Flow

```
User clicks mod button
    |
    v
Frontend updates UI optimistically (gray out, strikethrough)
    |
    v
Tauri IPC -> Rust (identifies platform from UnifiedMessage)
    |
    v
Rust routes to Go via command channel
    |
    v
Go dispatches to correct platform API
    |
    v
Success: no UI change needed (already updated)
Failure: Rust notifies frontend -> UI reverts, shows error indicator
```

## Permissions

On account link, the Go sidecar queries each platform's API to check the user's mod status for the connected channel.

- Twitch: Helix `GET /moderation/moderators` to check if user is mod
- YouTube: `liveChatModerators.list` to check if user is mod

Mod action buttons are only enabled for platforms where the user has permission. If the user is a mod on Twitch but not YouTube, Twitch messages show mod buttons and YouTube messages don't.

Permissions are re-checked on channel connect and cached for the session.

## User Cards

Click a username to open a user card:

- Display name and platform badge
- Account age (platform-specific)
- Follower/subscriber status
- Recent messages from this session (pulled from the message ring buffer by user ID)
- Quick-action buttons: timeout (with duration presets), ban, delete all messages from this user

User card data is fetched on demand from the platform API via Go, with a short cache (5 min) to avoid redundant calls when clicking the same user repeatedly.

## Streamer Agent

The desktop UI includes a local co-mod panel that watches the same unified message stream as the chat feed. The first implementation is deterministic and runs entirely in the frontend so it works without an external AI key:

- risky-message triage with severity, reasons, and suggested action
- local policy checks for repeated-message spam, rapid same-user bursts, risky links, and direct harm language
- question queue for streamer follow-up
- suggested replies for common live moments
- one-minute pulse with platform counts and repeated terms
- four panel looks: Professional, Gamify, Dark Fantasy, and Girls
- streamer-only `!agent` commands for clearing queues, resetting state, and switching looks
- approval-gated moderation workflow: Twitch actions can be applied from the panel after streamer approval; YouTube/Kick actions fall back to copy/manual handling until their active chat moderation state is wired into the desktop host

The agent store is intentionally isolated from automatic platform mutation. It recommends actions, and the UI requires a streamer click before any moderation command runs. Twitch moderation uses Helix `moderator:manage:banned_users` and `moderator:manage:chat_messages` scopes. YouTube/Kick recommendations stay manual because the current unified `ChatMessage` model does not yet carry enough live-chat/moderator context to safely execute those platform mutations. A future provider can replace the deterministic analyzer with OpenAI, Ollama, or another model while keeping the same `AgentSnapshot` interface.

Agent commands are only accepted when the message is from the signed-in streamer login or the platform marks it as broadcaster-authored. Viewer attempts are recorded in the command lock and ignored.

// Twitch sign-in flow client. Mirrors the Tauri command surface in
// apps/desktop/src-tauri/src/twitch_auth/commands.rs.

import { invoke } from "@tauri-apps/api/core";
import { openUrl } from "@tauri-apps/plugin-opener";

export type AuthStatusState = "logged_out" | "logged_in";

// Discriminated union: when state is logged_in, login is guaranteed by
// the backend (twitch_auth::commands::twitch_auth_status). Modeling it
// this way prevents the UI from silently rendering an empty username if
// a malformed payload ever slips through.
export type AuthStatus =
  | { state: "logged_out" }
  | { state: "logged_in"; login: string };

export interface DeviceCodeView {
  verification_uri: string;
  user_code: string;
  expires_in_secs: number;
}

export interface AuthCommandError {
  kind:
    | "no_tokens"
    | "refresh_invalid"
    | "device_code_expired"
    | "user_denied"
    | "keychain"
    | "oauth"
    | "json"
    | "config"
    | "no_pending_flow";
  message: string;
}

export function getAuthStatus(): Promise<AuthStatus> {
  return invoke("twitch_auth_status");
}

export function startLogin(): Promise<DeviceCodeView> {
  return invoke("twitch_start_login");
}

export function completeLogin(): Promise<AuthStatus> {
  return invoke("twitch_complete_login");
}

export function cancelLogin(): Promise<void> {
  return invoke("twitch_cancel_login");
}

export function logout(): Promise<void> {
  return invoke("twitch_logout");
}

const ALLOWED_HOSTS = ["www.twitch.tv", "id.twitch.tv"];

export function openVerificationUri(uri: string): Promise<void> {
  let parsed: URL;
  try {
    parsed = new URL(uri);
  } catch {
    return Promise.reject(new Error("invalid verification URL"));
  }
  if (
    parsed.protocol !== "https:" ||
    !ALLOWED_HOSTS.includes(parsed.hostname)
  ) {
    return Promise.reject(new Error("verification URL not on a Twitch domain"));
  }
  return openUrl(uri);
}

// Frontend-facing error envelope from sidecar_commands::twitch_send_message.
// Mirrors the discriminated union the Rust side serializes via serde's
// internally-tagged representation. `kind` is stable and safe to switch on.
export type SendMessageError =
  | { kind: "not_logged_in"; message: string }
  | { kind: "empty_message" }
  | { kind: "message_too_long"; max_bytes: number }
  | { kind: "message_too_long_chars"; max_chars: number }
  | { kind: "sidecar_not_running" }
  | { kind: "io"; message: string }
  | { kind: "auth"; message: string }
  | { kind: "json"; message: string }
  | { kind: "helix"; code: string; message: string }
  | { kind: "youtube"; code: string; message: string };

export const MAX_CHAT_MESSAGE_BYTES = 500;

/** Success payload from `twitch_send_message`. Mirrors `SendChatOk`
 * on the Rust side. The `message_id` is the Helix-assigned id, used
 * by the optimistic renderer to correlate a pending entry with its
 * authoritative EventSub echo. */
export interface SendMessageOk {
  message_id: string;
}

export function sendMessage(text: string): Promise<SendMessageOk> {
  return invoke("twitch_send_message", { text });
}

export type TwitchModerationAction = "ban" | "timeout" | "delete";

export interface TwitchModerationInput {
  action: TwitchModerationAction;
  targetUserId?: string;
  messageId?: string;
  durationSeconds?: number;
  reason?: string;
}

export interface ModerationOk {
  ok: boolean;
}

export function runTwitchModerationAction(
  input: TwitchModerationInput,
): Promise<ModerationOk> {
  return invoke("twitch_moderation_action", {
    action: input.action,
    targetUserId: input.targetUserId,
    messageId: input.messageId,
    durationSeconds: input.durationSeconds,
    reason: input.reason,
  });
}

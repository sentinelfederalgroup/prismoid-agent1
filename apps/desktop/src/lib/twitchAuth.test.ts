import { afterEach, describe, expect, it, vi } from "vitest";

const invokeMock = vi.fn();

vi.mock("@tauri-apps/api/core", () => ({
  invoke: (...args: unknown[]) => invokeMock(...args),
}));

vi.mock("@tauri-apps/plugin-opener", () => ({
  openUrl: vi.fn().mockResolvedValue(undefined),
}));

import {
  sendMessage,
  runTwitchModerationAction,
  openVerificationUri,
  MAX_CHAT_MESSAGE_BYTES,
} from "./twitchAuth";

afterEach(() => {
  invokeMock.mockReset();
});

describe("runTwitchModerationAction", () => {
  it("invokes twitch_moderation_action with camelCase payload", async () => {
    invokeMock.mockResolvedValueOnce({ ok: true });
    await runTwitchModerationAction({
      action: "timeout",
      targetUserId: "u1",
      durationSeconds: 60,
      reason: "spam",
    });
    expect(invokeMock).toHaveBeenCalledWith("twitch_moderation_action", {
      action: "timeout",
      targetUserId: "u1",
      messageId: undefined,
      durationSeconds: 60,
      reason: "spam",
    });
  });
});

describe("sendMessage", () => {
  it("invokes twitch_send_message with the text payload", async () => {
    invokeMock.mockResolvedValueOnce(undefined);
    await sendMessage("hello world");
    expect(invokeMock).toHaveBeenCalledWith("twitch_send_message", {
      text: "hello world",
    });
  });

  it("propagates the structured error from the backend", async () => {
    const err = { kind: "helix", code: "msg_duplicate", message: "dup" };
    invokeMock.mockRejectedValueOnce(err);
    await expect(sendMessage("x")).rejects.toEqual(err);
  });

  it("exposes the byte cap matching the Rust constant", () => {
    expect(MAX_CHAT_MESSAGE_BYTES).toBe(500);
  });
});

describe("openVerificationUri", () => {
  it("allows a valid Twitch verification URL", async () => {
    await expect(
      openVerificationUri("https://www.twitch.tv/activate"),
    ).resolves.toBeUndefined();
  });

  it("rejects non-Twitch hosts", async () => {
    await expect(openVerificationUri("https://evil.com/phish")).rejects.toThrow(
      "verification URL not on a Twitch domain",
    );
  });

  it("rejects http URLs", async () => {
    await expect(
      openVerificationUri("http://www.twitch.tv/activate"),
    ).rejects.toThrow("verification URL not on a Twitch domain");
  });

  it("rejects invalid URLs", async () => {
    await expect(openVerificationUri("not-a-url")).rejects.toThrow(
      "invalid verification URL",
    );
  });
});

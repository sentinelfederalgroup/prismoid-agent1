import { describe, expect, it } from "vitest";
import { createAgentStore } from "./agentStore";
import type { ChatMessage } from "./chatStore";

function makeMsg(
  id: string,
  text: string,
  overrides: Partial<ChatMessage> = {},
): ChatMessage {
  return {
    id,
    platform: "Twitch",
    timestamp: 1_000,
    arrival_time: 1_000,
    effective_ts: 1_000,
    arrival_seq: 0,
    username: "viewer",
    display_name: "Viewer",
    platform_user_id: "viewer-1",
    message_text: text,
    badges: [],
    is_mod: false,
    is_subscriber: false,
    is_broadcaster: false,
    color: null,
    reply_to: null,
    emote_spans: [],
    ...overrides,
  };
}

describe("agentStore", () => {
  it("flags harmful language with a high-severity ban recommendation", () => {
    const store = createAgentStore(() => 1_000);

    store.observeMessages([makeMsg("1", "kys")]);

    const [flag] = store.snapshot().flags;
    expect(flag?.severity).toBe("high");
    expect(flag?.action).toBe("ban");
    expect(flag?.status).toBe("open");
    expect(flag?.reasons).toContain("direct harm, doxxing, or raid language");
  });

  it("flags promotional links for deletion", () => {
    const store = createAgentStore(() => 1_000);

    store.observeMessages([
      makeMsg("1", "free followers at https://example.com"),
    ]);

    const [flag] = store.snapshot().flags;
    expect(flag?.severity).toBe("medium");
    expect(flag?.action).toBe("delete");
    expect(flag?.platform).toBe("Twitch");
  });

  it("collects viewer questions and suggests a streamer reply", () => {
    const store = createAgentStore(() => 1_000);

    store.observeMessages([makeMsg("1", "what build are you using?")]);

    expect(store.snapshot().questions).toHaveLength(1);
    expect(store.snapshot().replies[0]?.text).toContain("@Viewer");
    expect(store.snapshot().replies[0]?.reason).toBe(
      "Answer the newest open question",
    );
  });

  it("tracks platform pulse and top terms across recent messages", () => {
    const store = createAgentStore(() => 2_000);

    store.observeMessages([
      makeMsg("1", "boss fight looks clean", { platform: "Twitch" }),
      makeMsg("2", "boss fight timing?", { platform: "Kick" }),
      makeMsg("3", "youtube chat checking in", { platform: "YouTube" }),
    ]);

    expect(store.snapshot().pulse.messagesPerMinute).toBe(3);
    expect(store.snapshot().pulse.platforms).toEqual({
      Twitch: 1,
      YouTube: 1,
      Kick: 1,
    });
    expect(store.snapshot().pulse.topTerms).toContain("boss");
    expect(store.snapshot().replies.some((r) => r.id === "multistream")).toBe(
      true,
    );
  });

  it("dismisses flags and questions without clearing the pulse", () => {
    const store = createAgentStore(() => 1_000);
    store.observeMessages([
      makeMsg("1", "free followers at https://example.com"),
      makeMsg("2", "how long is stream today?"),
    ]);
    const flagId = store.snapshot().flags[0]!.id;
    const questionId = store.snapshot().questions[0]!.id;

    store.dismissFlag(flagId);
    store.dismissQuestion(questionId);

    expect(store.snapshot().flags).toHaveLength(0);
    expect(store.snapshot().questions).toHaveLength(0);
    expect(store.snapshot().pulse.totalMessages).toBe(2);
  });

  it("tracks streamer approval and completion for moderation flags", () => {
    const store = createAgentStore(() => 1_000);
    store.observeMessages([makeMsg("1", "free followers at https://example.com")]);
    const flagId = store.snapshot().flags[0]!.id;

    store.approveFlag(flagId);
    expect(store.snapshot().flags[0]?.status).toBe("approved");
    expect(store.snapshot().flags[0]?.approvedAt).toBe(1_000);

    store.completeFlag(flagId);
    expect(store.snapshot().flags[0]?.status).toBe("done");
    expect(store.snapshot().flags[0]?.completedAt).toBe(1_000);
  });

  it("flags repeated-message spam without an external AI service", () => {
    const store = createAgentStore(() => 1_000);
    store.observeMessages([
      makeMsg("1", "visit my channel now"),
      makeMsg("2", "visit my channel now"),
      makeMsg("3", "visit my channel now"),
    ]);

    const [flag] = store.snapshot().flags;
    expect(flag?.severity).toBe("medium");
    expect(flag?.action).toBe("timeout");
    expect(flag?.reasons).toContain("repeated-message spam");
  });

  it("flags rapid same-user bursts", () => {
    const store = createAgentStore(() => 1_000);
    store.observeMessages(
      Array.from({ length: 6 }, (_, i) =>
        makeMsg(`${i}`, `normal sentence ${i}`, {
          username: "same_user",
          platform_user_id: "same-user-id",
        }),
      ),
    );

    expect(store.snapshot().flags[0]?.reasons).toContain(
      "rapid same-user message burst",
    );
  });

  it("flags account-safety link impersonation", () => {
    const store = createAgentStore(() => 1_000);
    store.observeMessages([makeMsg("1", "mod verify at https://example.com")]);

    expect(store.snapshot().flags[0]?.reasons).toContain(
      "impersonation or account-safety link pattern",
    );
    expect(store.snapshot().flags[0]?.action).toBe("delete");
  });

  it("ignores agent commands from regular viewers", () => {
    const store = createAgentStore(() => 1_000);

    store.observeMessages(
      [makeMsg("1", "!agent look dark-fantasy")],
      "streamer",
    );

    expect(store.snapshot().look).toBe("professional");
    expect(store.snapshot().commands[0]?.status).toBe("ignored");
    expect(store.snapshot().commands[0]?.reason).toContain(
      "Only the signed-in streamer",
    );
  });

  it("accepts agent commands from the signed-in streamer", () => {
    const store = createAgentStore(() => 1_000);

    store.observeMessages(
      [
        makeMsg("1", "!agent look girls", {
          username: "streamer",
          display_name: "Streamer",
        }),
      ],
      "streamer",
    );

    expect(store.snapshot().look).toBe("girls");
    expect(store.snapshot().commands[0]?.status).toBe("accepted");
  });

  it("accepts agent commands from broadcaster messages", () => {
    const store = createAgentStore(() => 1_000);

    store.observeMessages([
      makeMsg("1", "!agent look gamify", { is_broadcaster: true }),
    ]);

    expect(store.snapshot().look).toBe("gamify");
    expect(store.snapshot().commands[0]?.status).toBe("accepted");
  });

  it("clears queues with a streamer-only clear command", () => {
    const store = createAgentStore(() => 1_000);
    store.observeMessages([
      makeMsg("1", "free followers at https://example.com"),
      makeMsg("2", "how long is stream today?"),
    ]);

    store.observeMessages(
      [
        makeMsg("3", "!agent clear", {
          username: "streamer",
          display_name: "Streamer",
        }),
      ],
      "streamer",
    );

    expect(store.snapshot().flags).toHaveLength(0);
    expect(store.snapshot().questions).toHaveLength(0);
  });
});

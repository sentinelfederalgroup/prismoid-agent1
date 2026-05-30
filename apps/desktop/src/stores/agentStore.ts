import { createSignal } from "solid-js";
import type { ChatMessage } from "./chatStore";

export type AgentSeverity = "low" | "medium" | "high";
export type AgentAction = "watch" | "delete" | "timeout" | "ban";
export type AgentLook = "professional" | "gamify" | "dark-fantasy" | "girls";

export interface AgentFlag {
  id: string;
  messageId: string;
  platform: ChatMessage["platform"];
  displayName: string;
  username: string;
  platformUserId: string;
  text: string;
  severity: AgentSeverity;
  action: AgentAction;
  reasons: string[];
  status: "open" | "approved" | "done";
  createdAt: number;
  approvedAt?: number;
  completedAt?: number;
}

export interface AgentQuestion {
  id: string;
  platform: ChatMessage["platform"];
  displayName: string;
  text: string;
  createdAt: number;
}

export interface AgentReply {
  id: string;
  text: string;
  reason: string;
}

export interface AgentCommandLog {
  id: string;
  displayName: string;
  text: string;
  status: "accepted" | "ignored";
  reason: string;
  createdAt: number;
}

export interface AgentPulse {
  totalMessages: number;
  messagesPerMinute: number;
  platforms: Record<ChatMessage["platform"], number>;
  topTerms: string[];
}

export interface AgentSnapshot {
  look: AgentLook;
  flags: AgentFlag[];
  questions: AgentQuestion[];
  replies: AgentReply[];
  commands: AgentCommandLog[];
  pulse: AgentPulse;
}

export interface AgentStore {
  snapshot: () => AgentSnapshot;
  observeMessages: (messages: ChatMessage[], streamerLogin?: string) => void;
  setLook: (look: AgentLook) => void;
  approveFlag: (id: string) => void;
  completeFlag: (id: string) => void;
  dismissFlag: (id: string) => void;
  dismissQuestion: (id: string) => void;
  reset: () => void;
}

const MAX_FLAGS = 12;
const MAX_QUESTIONS = 8;
const MAX_COMMANDS = 6;
const WINDOW_MS = 60_000;
const AGENT_COMMAND_PATTERN = /^!(?:agent|modbot)\b\s*(.*)$/i;
const QUESTION_STARTERS = /\b(how|what|when|where|why|can|could|will|would|is|are|do|does|did)\b/i;
const URL_PATTERN = /\bhttps?:\/\/|(?:\b[\w-]+\.)+(?:com|net|org|gg|tv|io)\b/i;
const MONEY_SPAM = /\b(prime|gift\s*card|crypto|airdrop|telegram|whatsapp|followers?|views?|subs?)\b/i;
const THREAT_PATTERN = /\b(kill yourself|kys|doxx|swat|hate raid)\b/i;
const DEFAULT_PULSE: AgentPulse = {
  totalMessages: 0,
  messagesPerMinute: 0,
  platforms: { Twitch: 0, YouTube: 0, Kick: 0 },
  topTerms: [],
};

function emptySnapshot(): AgentSnapshot {
  return {
    look: "professional",
    flags: [],
    questions: [],
    commands: [],
    replies: [
      {
        id: "welcome",
        text: "Welcome in. Drop questions in chat and I will pull them into the queue.",
        reason: "Low-traffic opener",
      },
    ],
    pulse: { ...DEFAULT_PULSE, platforms: { ...DEFAULT_PULSE.platforms } },
  };
}

export function createAgentStore(now = () => Date.now()): AgentStore {
  const recent: ChatMessage[] = [];
  const [snapshot, setSnapshot] = createSignal<AgentSnapshot>(emptySnapshot());

  function observeMessages(messages: ChatMessage[], streamerLogin = ""): void {
    if (messages.length === 0) return;
    const timestamp = now();
    for (const message of messages) {
      recent.push(message);
    }
    pruneRecent(timestamp);

    setSnapshot((current) => {
      const nextFlags = [...current.flags];
      const nextQuestions = [...current.questions];
      const nextCommands = [...current.commands];
      let nextLook = current.look;
      let resetRequested = false;
      for (const message of messages) {
        const command = parseAgentCommand(message, streamerLogin, timestamp);
        if (command) {
          prependUnique(nextCommands, command.log, MAX_COMMANDS);
          if (command.log.status === "accepted") {
            if (command.kind === "reset") resetRequested = true;
            if (command.kind === "clear") {
              nextFlags.length = 0;
              nextQuestions.length = 0;
            }
            if (command.kind === "clear-flags") nextFlags.length = 0;
            if (command.kind === "clear-questions") nextQuestions.length = 0;
            if (command.kind === "look") nextLook = command.look;
          }
          continue;
        }

        const flag = classifyMessage(message, timestamp, recent);
        if (flag) prependUnique(nextFlags, flag, MAX_FLAGS);

        const question = classifyQuestion(message, timestamp);
        if (question) prependUnique(nextQuestions, question, MAX_QUESTIONS);
      }

      if (resetRequested) {
        recent.length = 0;
        return {
          ...emptySnapshot(),
          look: nextLook,
          commands: nextCommands,
        };
      }

      return {
        look: nextLook,
        flags: nextFlags,
        questions: nextQuestions,
        commands: nextCommands,
        replies: buildReplies(nextQuestions, nextFlags, recent),
        pulse: buildPulse(recent, timestamp),
      };
    });
  }

  function pruneRecent(timestamp: number): void {
    const cutoff = timestamp - WINDOW_MS;
    while (recent.length > 0 && recent[0]!.arrival_time < cutoff) {
      recent.shift();
    }
  }

  function dismissFlag(id: string): void {
    setSnapshot((current) => ({
      ...current,
      flags: current.flags.filter((flag) => flag.id !== id),
    }));
  }

  function approveFlag(id: string): void {
    const timestamp = now();
    setSnapshot((current) => ({
      ...current,
      flags: current.flags.map((flag) =>
        flag.id === id
          ? { ...flag, status: "approved", approvedAt: timestamp }
          : flag,
      ),
    }));
  }

  function completeFlag(id: string): void {
    const timestamp = now();
    setSnapshot((current) => ({
      ...current,
      flags: current.flags.map((flag) =>
        flag.id === id ? { ...flag, status: "done", completedAt: timestamp } : flag,
      ),
    }));
  }

  function dismissQuestion(id: string): void {
    setSnapshot((current) => {
      const questions = current.questions.filter((q) => q.id !== id);
      return {
        ...current,
        questions,
        replies: buildReplies(questions, current.flags, recent),
      };
    });
  }

  function reset(): void {
    recent.length = 0;
    setSnapshot((current) => ({ ...emptySnapshot(), look: current.look }));
  }

  function setLook(look: AgentLook): void {
    setSnapshot((current) => ({ ...current, look }));
  }

  return {
    snapshot,
    observeMessages,
    setLook,
    approveFlag,
    completeFlag,
    dismissFlag,
    dismissQuestion,
    reset,
  };
}

type ParsedAgentCommand =
  | { kind: "reset"; log: AgentCommandLog }
  | { kind: "clear"; log: AgentCommandLog }
  | { kind: "clear-flags"; log: AgentCommandLog }
  | { kind: "clear-questions"; log: AgentCommandLog }
  | { kind: "look"; look: AgentLook; log: AgentCommandLog };

function parseAgentCommand(
  message: ChatMessage,
  streamerLogin: string,
  createdAt: number,
): ParsedAgentCommand | undefined {
  const match = message.message_text.trim().match(AGENT_COMMAND_PATTERN);
  if (!match) return undefined;

  const text = message.message_text.trim();
  const baseLog = {
    id: `${message.platform}:${message.id}:command`,
    displayName: message.display_name,
    text,
    createdAt,
  };

  if (!isStreamerCommand(message, streamerLogin)) {
    return {
      kind: "clear",
      log: {
        ...baseLog,
        status: "ignored",
        reason: "Only the signed-in streamer can run agent commands.",
      },
    };
  }

  const args = (match[1] ?? "").trim().toLowerCase();
  if (args === "reset") {
    return {
      kind: "reset",
      log: { ...baseLog, status: "accepted", reason: "Agent state reset." },
    };
  }
  if (args === "clear") {
    return {
      kind: "clear",
      log: {
        ...baseLog,
        status: "accepted",
        reason: "Mod and question queues cleared.",
      },
    };
  }
  if (args === "clear flags") {
    return {
      kind: "clear-flags",
      log: { ...baseLog, status: "accepted", reason: "Mod queue cleared." },
    };
  }
  if (args === "clear questions") {
    return {
      kind: "clear-questions",
      log: {
        ...baseLog,
        status: "accepted",
        reason: "Question queue cleared.",
      },
    };
  }

  const look = parseLook(args);
  if (look) {
    return {
      kind: "look",
      look,
      log: {
        ...baseLog,
        status: "accepted",
        reason: `Look changed to ${lookLabel(look)}.`,
      },
    };
  }

  return {
    kind: "clear",
    log: {
      ...baseLog,
      status: "ignored",
      reason:
        "Unknown command. Use !agent reset, !agent clear, or !agent look <style>.",
    },
  };
}

function parseLook(args: string): AgentLook | undefined {
  const lookArg = args.replace(/^look\s+/, "").replace(/\s+/g, "-");
  if (
    lookArg === "professional" ||
    lookArg === "gamify" ||
    lookArg === "dark-fantasy" ||
    lookArg === "girls"
  ) {
    return lookArg;
  }
  return undefined;
}

function lookLabel(look: AgentLook): string {
  if (look === "dark-fantasy") return "Dark Fantasy";
  return look[0]!.toUpperCase() + look.slice(1);
}

function isStreamerCommand(message: ChatMessage, streamerLogin: string): boolean {
  if (message.is_broadcaster) return true;
  const expected = normalizeLogin(streamerLogin);
  if (!expected) return false;
  return (
    normalizeLogin(message.username) === expected ||
    normalizeLogin(message.display_name) === expected
  );
}

function classifyMessage(
  message: ChatMessage,
  createdAt: number,
  recent: ChatMessage[],
): AgentFlag | undefined {
  const text = message.message_text.trim();
  const reasons: string[] = [];
  let severity: AgentSeverity = "low";
  let action: AgentAction = "watch";

  if (THREAT_PATTERN.test(text)) {
    reasons.push("direct harm, doxxing, or raid language");
    severity = "high";
    action = "ban";
  }
  if (URL_PATTERN.test(text) && MONEY_SPAM.test(text)) {
    reasons.push("promotional link pattern");
    severity = severity === "high" ? "high" : "medium";
    action = action === "ban" ? "ban" : "delete";
  }
  if (URL_PATTERN.test(text) && /\b(mod|admin|support|staff|verify|login|claim)\b/i.test(text)) {
    reasons.push("impersonation or account-safety link pattern");
    severity = severity === "high" ? "high" : "medium";
    action = action === "ban" ? "ban" : "delete";
  }
  if (duplicateTextCount(message, recent) >= 3) {
    reasons.push("repeated-message spam");
    severity = severity === "high" ? "high" : "medium";
    action = action === "ban" ? "ban" : "timeout";
  }
  if (recentUserCount(message, recent) >= 6) {
    reasons.push("rapid same-user message burst");
    severity = severity === "high" ? "high" : "medium";
    action = action === "ban" ? "ban" : "timeout";
  }
  if (isShouty(text)) {
    reasons.push("all-caps spam pattern");
    severity = severity === "high" ? "high" : "low";
  }
  if (/(.)\1{7,}/.test(text)) {
    reasons.push("repeated-character spam pattern");
    severity = severity === "high" ? "high" : "low";
  }

  if (reasons.length === 0) return undefined;
  if (severity === "low" && action === "watch") action = "watch";
  if (severity === "medium" && action === "watch") action = "timeout";

  return {
    id: `${message.platform}:${message.id}:${reasons.join("|")}`,
    messageId: message.id,
    platform: message.platform,
    displayName: message.display_name,
    username: message.username,
    platformUserId: message.platform_user_id,
    text,
    severity,
    action,
    reasons,
    status: "open",
    createdAt,
  };
}

function duplicateTextCount(message: ChatMessage, recent: ChatMessage[]): number {
  const text = normalizeMessageText(message.message_text);
  if (text.length < 6) return 0;
  return recent.filter(
    (candidate) =>
      candidate.platform === message.platform &&
      normalizeMessageText(candidate.message_text) === text,
  ).length;
}

function recentUserCount(message: ChatMessage, recent: ChatMessage[]): number {
  const userKey = normalizeLogin(message.platform_user_id || message.username);
  if (!userKey) return 0;
  return recent.filter(
    (candidate) =>
      candidate.platform === message.platform &&
      normalizeLogin(candidate.platform_user_id || candidate.username) === userKey,
  ).length;
}

function normalizeMessageText(text: string): string {
  return text.trim().toLowerCase().replace(/\s+/g, " ");
}

function classifyQuestion(
  message: ChatMessage,
  createdAt: number,
): AgentQuestion | undefined {
  const text = message.message_text.trim();
  if (!text.includes("?") && !QUESTION_STARTERS.test(text)) return undefined;
  if (text.length < 8) return undefined;
  return {
    id: `${message.platform}:${message.id}:question`,
    platform: message.platform,
    displayName: message.display_name,
    text,
    createdAt,
  };
}

function buildReplies(
  questions: AgentQuestion[],
  flags: AgentFlag[],
  recent: ChatMessage[],
): AgentReply[] {
  const replies: AgentReply[] = [];
  const topQuestion = questions[0];
  if (topQuestion) {
    replies.push({
      id: `answer:${topQuestion.id}`,
      text: `@${topQuestion.displayName} good question. I am checking that now.`,
      reason: "Answer the newest open question",
    });
  }

  const highFlag = flags.find((flag) => flag.severity === "high");
  if (highFlag) {
    replies.push({
      id: `mod:${highFlag.id}`,
      text: `Mods, please review ${highFlag.displayName} on ${highFlag.platform}.`,
      reason: "Escalate a high-risk message",
    });
  }

  const platforms = new Set(recent.map((message) => message.platform));
  if (platforms.size >= 2) {
    replies.push({
      id: "multistream",
      text: "Chat is live across platforms, so say where you are watching from.",
      reason: "Cross-platform engagement",
    });
  }

  if (replies.length === 0) {
    replies.push({
      id: "heartbeat",
      text: "I see chat. Keep the questions coming.",
      reason: "Maintain momentum",
    });
  }
  return replies.slice(0, 3);
}

function buildPulse(recent: ChatMessage[], now: number): AgentPulse {
  const cutoff = now - WINDOW_MS;
  const live = recent.filter((message) => message.arrival_time >= cutoff);
  const platforms: AgentPulse["platforms"] = { Twitch: 0, YouTube: 0, Kick: 0 };
  for (const message of live) platforms[message.platform]++;

  return {
    totalMessages: live.length,
    messagesPerMinute: live.length,
    platforms,
    topTerms: topTerms(live),
  };
}

function topTerms(messages: ChatMessage[]): string[] {
  const counts = new Map<string, number>();
  for (const message of messages) {
    for (const raw of message.message_text.toLowerCase().split(/[^a-z0-9]+/)) {
      if (raw.length < 4) continue;
      if (STOP_WORDS.has(raw)) continue;
      counts.set(raw, (counts.get(raw) ?? 0) + 1);
    }
  }
  return [...counts.entries()]
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .slice(0, 5)
    .map(([term]) => term);
}

function isShouty(text: string): boolean {
  const letters = text.replace(/[^a-z]/gi, "");
  if (letters.length < 12) return false;
  const uppercase = letters.replace(/[^A-Z]/g, "").length;
  return uppercase / letters.length > 0.8;
}

function prependUnique<T extends { id: string }>(
  items: T[],
  item: T,
  limit: number,
): void {
  const existing = items.findIndex((candidate) => candidate.id === item.id);
  if (existing >= 0) items.splice(existing, 1);
  items.unshift(item);
  items.length = Math.min(items.length, limit);
}

const STOP_WORDS = new Set([
  "about",
  "after",
  "again",
  "also",
  "because",
  "chat",
  "from",
  "have",
  "just",
  "like",
  "that",
  "this",
  "what",
  "when",
  "where",
  "with",
  "your",
]);

const defaultAgentStore = createAgentStore();

export const agentSnapshot = defaultAgentStore.snapshot;
export const observeAgentMessages = defaultAgentStore.observeMessages;
export const setAgentLook = defaultAgentStore.setLook;
export const approveAgentFlag = defaultAgentStore.approveFlag;
export const completeAgentFlag = defaultAgentStore.completeFlag;
export const dismissAgentFlag = defaultAgentStore.dismissFlag;
export const dismissAgentQuestion = defaultAgentStore.dismissQuestion;
export const resetAgent = defaultAgentStore.reset;

function normalizeLogin(login: string): string {
  return login.trim().toLowerCase();
}

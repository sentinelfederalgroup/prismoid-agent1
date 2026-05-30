import { Component, For, Show, type JSX } from "solid-js";
import {
  agentSnapshot,
  approveAgentFlag,
  completeAgentFlag,
  dismissAgentFlag,
  dismissAgentQuestion,
  setAgentLook,
  type AgentFlag,
  type AgentLook,
} from "../stores/agentStore";
import { runTwitchModerationAction } from "../lib/twitchAuth";

const severityColor: Record<AgentFlag["severity"], string> = {
  low: "#facc15",
  medium: "#fb923c",
  high: "#f87171",
};

const looks: { id: AgentLook; label: string }[] = [
  { id: "professional", label: "Professional" },
  { id: "gamify", label: "Gamify" },
  { id: "dark-fantasy", label: "Dark Fantasy" },
  { id: "girls", label: "Girls" },
];

const lookThemes: Record<
  AgentLook,
  {
    panel: string;
    header: string;
    card: string;
    metric: string;
    border: string;
    text: string;
    muted: string;
    accent: string;
    pill: string;
    button: string;
  }
> = {
  professional: {
    panel: "#121214",
    header: "#18181b",
    card: "#1a1a1e",
    metric: "#1c1c20",
    border: "#2f2f35",
    text: "#efeff1",
    muted: "#a1a1aa",
    accent: "#60a5fa",
    pill: "#27272a",
    button: "#18181b",
  },
  gamify: {
    panel: "#10150f",
    header: "#182117",
    card: "#1d2b19",
    metric: "#21351c",
    border: "#39522f",
    text: "#f4ffe8",
    muted: "#b9d5aa",
    accent: "#a3e635",
    pill: "#2f441f",
    button: "#192715",
  },
  "dark-fantasy": {
    panel: "#120d16",
    header: "#1d1325",
    card: "#211629",
    metric: "#271933",
    border: "#4a2d5f",
    text: "#f5e9ff",
    muted: "#c4accf",
    accent: "#c084fc",
    pill: "#321d3f",
    button: "#1c1323",
  },
  girls: {
    panel: "#1a1016",
    header: "#25121d",
    card: "#2b1823",
    metric: "#331b29",
    border: "#6b344f",
    text: "#fff0f7",
    muted: "#e8b7cc",
    accent: "#f9a8d4",
    pill: "#452033",
    button: "#2a1722",
  },
};

type LookTheme = (typeof lookThemes)[AgentLook];

const AgentPanel: Component = () => {
  const snapshot = agentSnapshot;
  const theme = () => lookThemes[snapshot().look];
  const copyAction = (flag: AgentFlag) => {
    const text = modActionText(flag);
    navigator.clipboard?.writeText(text).catch(() => undefined);
    approveAgentFlag(flag.id);
  };
  const applyAction = async (flag: AgentFlag) => {
    if (flag.platform !== "Twitch") {
      copyAction(flag);
      return;
    }
    const input = twitchModerationInput(flag);
    if (!input) {
      copyAction(flag);
      return;
    }
    approveAgentFlag(flag.id);
    try {
      await runTwitchModerationAction(input);
      completeAgentFlag(flag.id);
    } catch {
      copyAction(flag);
    }
  };
  return (
    <aside
      aria-label="Streamer agent"
      style={panelStyle(theme())}
    >
      <div
        style={{
          padding: "12px",
          "border-bottom": `1px solid ${theme().border}`,
          background: theme().header,
        }}
      >
        <div
          style={{
            display: "flex",
            "align-items": "center",
            "justify-content": "space-between",
            gap: "8px",
          }}
        >
          <h2 style={{ margin: 0, "font-size": "14px", "font-weight": 700 }}>
            Streamer agent
          </h2>
          <span
            title="Local co-mod analysis"
            style={{
              "font-size": "11px",
              color: theme().muted,
              border: `1px solid ${theme().border}`,
              padding: "2px 6px",
              "border-radius": "4px",
            }}
          >
            local
          </span>
        </div>
        <div
          style={{
            display: "grid",
            "grid-template-columns": "1fr 1fr",
            gap: "8px",
            "margin-top": "12px",
          }}
        >
          <Metric label="msg/min" value={snapshot().pulse.messagesPerMinute} />
          <Metric label="flags" value={snapshot().flags.length} />
        </div>
        <div
          role="group"
          aria-label="Agent look"
          style={{
            display: "grid",
            "grid-template-columns": "1fr 1fr",
            gap: "6px",
            "margin-top": "10px",
          }}
        >
          <For each={looks}>
            {(look) => {
              const active = () => snapshot().look === look.id;
              return (
                <button
                  type="button"
                  aria-pressed={active()}
                  onClick={() => setAgentLook(look.id)}
                  style={{
                    border: `1px solid ${
                      active() ? theme().accent : theme().border
                    }`,
                    background: active() ? theme().pill : theme().button,
                    color: active() ? theme().text : theme().muted,
                    "border-radius": "5px",
                    padding: "5px 6px",
                    "font-size": "11px",
                    cursor: "pointer",
                    "min-width": 0,
                  }}
                >
                  {look.label}
                </button>
              );
            }}
          </For>
        </div>
      </div>

      <div style={{ padding: "12px", "overflow-y": "auto" }}>
        <Section title="Mod queue">
          <Show
            when={snapshot().flags.length > 0}
            fallback={<Empty text="No risky messages in the current window." />}
          >
            <For each={snapshot().flags}>
              {(flag) => (
                <div style={cardStyle()}>
                  <div style={{ display: "flex", gap: "8px" }}>
                    <span
                      title={flag.severity}
                      style={{
                        width: "8px",
                        height: "8px",
                        "border-radius": "50%",
                        background: severityColor[flag.severity],
                        "margin-top": "5px",
                        "flex-shrink": 0,
                      }}
                    />
                    <div style={{ "min-width": 0, flex: 1 }}>
                      <div style={rowStyle()}>
                        <strong style={{ "font-size": "12px" }}>
                          {flag.displayName}
                        </strong>
                        <span style={pillStyle()}>
                          {flag.status === "open"
                            ? flag.action
                            : `${flag.status}: ${flag.action}`}
                        </span>
                      </div>
                      <p style={messageStyle()}>{flag.text}</p>
                      <p style={reasonStyle()}>{flag.reasons.join(", ")}</p>
                      <div style={buttonRowStyle()}>
                        <button
                          type="button"
                          title="Apply on Twitch or copy for manual moderation"
                          onClick={() => applyAction(flag)}
                          style={smallButtonStyle()}
                        >
                          Apply
                        </button>
                        <button
                          type="button"
                          title="Copy suggested mod action"
                          onClick={() => copyAction(flag)}
                          style={smallButtonStyle()}
                        >
                          Copy action
                        </button>
                        <button
                          type="button"
                          title="Mark this action completed"
                          onClick={() => completeAgentFlag(flag.id)}
                          style={smallButtonStyle()}
                        >
                          Done
                        </button>
                      </div>
                    </div>
                    <button
                      title="Dismiss"
                      aria-label={`Dismiss flag for ${flag.displayName}`}
                      onClick={() => dismissAgentFlag(flag.id)}
                      style={iconButtonStyle()}
                    >
                      x
                    </button>
                  </div>
                </div>
              )}
            </For>
          </Show>
        </Section>

        <Section title="Question queue">
          <Show
            when={snapshot().questions.length > 0}
            fallback={<Empty text="No open viewer questions yet." />}
          >
            <For each={snapshot().questions}>
              {(question) => (
                <div style={cardStyle()}>
                  <div style={rowStyle()}>
                    <strong style={{ "font-size": "12px" }}>
                      {question.displayName}
                    </strong>
                    <button
                      title="Mark answered"
                      aria-label={`Mark ${question.displayName}'s question answered`}
                      onClick={() => dismissAgentQuestion(question.id)}
                      style={iconButtonStyle()}
                    >
                      x
                    </button>
                  </div>
                  <p style={messageStyle()}>{question.text}</p>
                </div>
              )}
            </For>
          </Show>
        </Section>

        <Section title="Suggested replies">
          <For each={snapshot().replies}>
            {(reply) => (
              <div style={cardStyle()}>
                <p style={messageStyle()}>{reply.text}</p>
                <p style={reasonStyle()}>{reply.reason}</p>
              </div>
            )}
          </For>
        </Section>

        <Section title="Pulse">
          <div style={cardStyle()}>
            <div style={rowStyle()}>
              <span style={reasonStyle()}>Twitch</span>
              <strong>{snapshot().pulse.platforms.Twitch}</strong>
            </div>
            <div style={rowStyle()}>
              <span style={reasonStyle()}>YouTube</span>
              <strong>{snapshot().pulse.platforms.YouTube}</strong>
            </div>
            <div style={rowStyle()}>
              <span style={reasonStyle()}>Kick</span>
              <strong>{snapshot().pulse.platforms.Kick}</strong>
            </div>
            <Show when={snapshot().pulse.topTerms.length > 0}>
              <div
                style={{
                  display: "flex",
                  "flex-wrap": "wrap",
                  gap: "6px",
                  "margin-top": "10px",
                }}
              >
                <For each={snapshot().pulse.topTerms}>
                  {(term) => <span style={pillStyle()}>{term}</span>}
                </For>
              </div>
            </Show>
          </div>
        </Section>

        <Section title="Command lock">
          <Show
            when={snapshot().commands.length > 0}
            fallback={
              <Empty text="Only streamer-authored !agent commands will run." />
            }
          >
            <For each={snapshot().commands}>
              {(command) => (
                <div style={cardStyle()}>
                  <div style={rowStyle()}>
                    <strong style={{ "font-size": "12px" }}>
                      {command.displayName}
                    </strong>
                    <span
                      style={{
                        ...pillStyle(),
                        color:
                          command.status === "accepted"
                            ? theme().accent
                            : "#fca5a5",
                      }}
                    >
                      {command.status}
                    </span>
                  </div>
                  <p style={messageStyle()}>{command.text}</p>
                  <p style={reasonStyle()}>{command.reason}</p>
                </div>
              )}
            </For>
          </Show>
        </Section>
      </div>
    </aside>
  );
};

const Metric: Component<{ label: string; value: number }> = (props) => (
  <div
    style={{
      background: "var(--agent-metric)",
      border: "1px solid var(--agent-border)",
      "border-radius": "6px",
      padding: "8px",
    }}
  >
    <div style={{ "font-size": "11px", color: "var(--agent-muted)" }}>
      {props.label}
    </div>
    <div style={{ "font-size": "18px", "font-weight": 700 }}>
      {props.value}
    </div>
  </div>
);

function panelStyle(theme: LookTheme): JSX.CSSProperties {
  return {
    width: "320px",
    "border-left": `1px solid ${theme.border}`,
    background: theme.panel,
    color: theme.text,
    "--agent-card": theme.card,
    "--agent-metric": theme.metric,
    "--agent-border": theme.border,
    "--agent-text": theme.text,
    "--agent-muted": theme.muted,
    "--agent-pill": theme.pill,
    "--agent-button": theme.button,
    display: "flex",
    "flex-direction": "column",
    "font-family":
      '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
    "min-width": 0,
  } as JSX.CSSProperties;
}

const Section: Component<{ title: string; children: JSX.Element }> = (props) => (
  <section style={{ "margin-bottom": "16px" }}>
    <h3
      style={{
        margin: "0 0 8px",
        "font-size": "12px",
        "font-weight": 700,
        color: "var(--agent-text)",
      }}
    >
      {props.title}
    </h3>
    {props.children}
  </section>
);

const Empty: Component<{ text: string }> = (props) => (
  <p style={{ margin: 0, color: "var(--agent-muted)", "font-size": "12px" }}>
    {props.text}
  </p>
);

function cardStyle() {
  return {
    background: "var(--agent-card)",
    border: "1px solid var(--agent-border)",
    "border-radius": "6px",
    padding: "9px",
    "margin-bottom": "8px",
  };
}

function rowStyle() {
  return {
    display: "flex",
    "align-items": "center",
    "justify-content": "space-between",
    gap: "8px",
  };
}

function messageStyle() {
  return {
    margin: "4px 0 0",
    color: "var(--agent-text)",
    "font-size": "12px",
    "line-height": 1.35,
    "overflow-wrap": "anywhere",
  };
}

function reasonStyle() {
  return {
    margin: "5px 0 0",
    color: "var(--agent-muted)",
    "font-size": "11px",
    "line-height": 1.3,
  };
}

function pillStyle() {
  return {
    color: "var(--agent-text)",
    background: "var(--agent-pill)",
    border: "1px solid var(--agent-border)",
    "border-radius": "4px",
    padding: "1px 5px",
    "font-size": "11px",
    "white-space": "nowrap",
  };
}

function iconButtonStyle() {
  return {
    width: "22px",
    height: "22px",
    "border-radius": "4px",
    border: "1px solid var(--agent-border)",
    background: "var(--agent-button)",
    color: "var(--agent-muted)",
    cursor: "pointer",
    "line-height": "18px",
    "flex-shrink": 0,
  };
}

function buttonRowStyle() {
  return {
    display: "flex",
    gap: "6px",
    "margin-top": "8px",
  };
}

function smallButtonStyle() {
  return {
    border: "1px solid var(--agent-border)",
    background: "var(--agent-button)",
    color: "var(--agent-text)",
    "border-radius": "4px",
    padding: "3px 6px",
    "font-size": "11px",
    cursor: "pointer",
  };
}

function modActionText(flag: AgentFlag): string {
  const target = flag.username || flag.displayName;
  if (flag.action === "ban") {
    return `/ban ${target} ${flag.reasons.join(", ")}`;
  }
  if (flag.action === "timeout") {
    return `/timeout ${target} 60 ${flag.reasons.join(", ")}`;
  }
  if (flag.action === "delete") {
    return `Delete message ${flag.messageId} from ${flag.displayName}: ${flag.reasons.join(", ")}`;
  }
  return `Watch ${flag.displayName}: ${flag.reasons.join(", ")}`;
}

function twitchModerationInput(flag: AgentFlag) {
  const reason = flag.reasons.join(", ");
  if (flag.action === "ban") {
    if (!flag.platformUserId) return undefined;
    return {
      action: "ban" as const,
      targetUserId: flag.platformUserId,
      reason,
    };
  }
  if (flag.action === "timeout") {
    if (!flag.platformUserId) return undefined;
    return {
      action: "timeout" as const,
      targetUserId: flag.platformUserId,
      durationSeconds: 60,
      reason,
    };
  }
  if (flag.action === "delete") {
    if (!flag.messageId) return undefined;
    return {
      action: "delete" as const,
      messageId: flag.messageId,
      reason,
    };
  }
  return undefined;
}

export default AgentPanel;

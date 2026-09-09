import { useSyncExternalStore } from "react";
import {
  api,
  requestKey,
  agentPath,
  APIError,
  type Agent,
  type Project,
  type Model,
  type Event,
  type Frame,
  type Output,
  type QueueMessage,
  type Schema,
} from "./api";

export type TranscriptItem = {
  id: string;
  kind: "user" | "agent" | "tool" | "tool_result" | "status";
  text: string;
  toolName?: string;
  toolCallId?: string;
  output?: string;
  time: string;
};
export type State = {
  agents: Record<string, Agent>;
  projects: Project[];
  models: Model[];
  transcripts: Record<string, TranscriptItem[]>;
  cursors: Record<string, number>;
  ready: Record<string, boolean>;
  status: "connecting" | "live" | "offline";
  error: string | null;
  loaded: boolean;
};
const initial = (): State => ({
  agents: {},
  projects: [],
  models: [],
  transcripts: {},
  cursors: {},
  ready: {},
  status: "connecting",
  error: null,
  loaded: false,
});

// Output events are completed display messages (not token deltas). Conversation
// events are model checkpoints and must not be rendered again as duplicate output.
export function reduceEvent(state: State, event: Event): State {
  const { agent_id: id, cursor, type, data } = event;
  if (cursor <= (state.cursors[id] || 0)) return state;
  if (cursor !== (state.cursors[id] || 0) + 1)
    throw new Error(
      `Event gap for ${id}; reconnecting from the last processed cursor.`,
    );
  let item: TranscriptItem | undefined;
  let agent = state.agents[id];
  if (!agent) throw new Error(`Missing inventory for ${id}`);
  if (type === "agent.created" || type === "agent.updated") {
    // Inventory is newer than replayed events. Do not roll current metadata back.
    if (cursor >= agent.cursor)
      agent = {
        ...agent,
        ...(data as Schema["AgentUpdate"]),
        cursor,
        updated_at: event.created_at,
      } as Agent;
  }
  if (type.startsWith("message.") || type.startsWith("turn.")) {
    const q = data as QueueMessage;
    const queue = [...(agent.queue || [])];
    const index = queue.findIndex((m) => m.id === q.id);
    if (index < 0) queue.push(q);
    else queue[index] = q;
    agent = { ...agent, queue };
    if (type === "turn.started")
      item = {
        id: String(cursor),
        kind: "user",
        text: q.text,
        time: event.created_at,
      };
    if (["turn.failed", "turn.interrupted", "turn.cancelled"].includes(type)) {
      item = {
        id: String(cursor),
        kind: "status",
        text: q.error || type,
        time: event.created_at,
      };
    }
  }
  if (type === "output") {
    const output = data as Output;
    if (output.ResponseType !== "usage")
      item = {
        id: String(cursor),
        kind: output.ResponseType,
        text: output.FullToolOutput || output.Content,
        toolName: output.ToolName,
        toolCallId: output.ToolCallID,
        time: event.created_at,
      };
  }
  let transcript = state.transcripts[id] || [];
  if (item) {
    // Match by call ID, never by name: parallel calls may use the same tool.
    let callIndex =
      item.kind === "tool_result" && item.toolCallId
        ? transcript.findLastIndex(
            (entry) =>
              entry.kind === "tool" && entry.toolCallId === item!.toolCallId,
          )
        : -1;
    // Older servers emitted sequential bash calls without correlation metadata.
    // Only fall back to an unresolved call if at least one side lacks an ID;
    // never attach a result to a different explicit call ID.
    if (item.kind === "tool_result" && callIndex < 0) {
      callIndex = transcript.findIndex(
        (entry) =>
          entry.kind === "tool" &&
          entry.output === undefined &&
          (!entry.toolCallId || !item!.toolCallId) &&
          (!entry.toolName ||
            !item!.toolName ||
            entry.toolName === item!.toolName),
      );
    }
    if (callIndex >= 0) {
      transcript = transcript.map((entry, index) =>
        index === callIndex ? { ...entry, output: item!.text } : entry,
      );
    } else {
      // Keep genuinely unmatched results visible rather than losing output.
      transcript = [...transcript, item];
    }
  }
  return {
    ...state,
    ready: { ...state.ready, [id]: state.ready[id] || cursor >= agent.cursor },
    agents: { ...state.agents, [id]: agent },
    cursors: { ...state.cursors, [id]: cursor },
    transcripts: item
      ? { ...state.transcripts, [id]: transcript }
      : state.transcripts,
  };
}

export class ControlPlane {
  state = initial();
  listeners = new Set<() => void>();
  private socket?: WebSocket;
  private stopped = true;
  private selected?: string;
  private retry?: ReturnType<typeof setTimeout>;
  private poll?: ReturnType<typeof setInterval>;
  private branchPoll?: ReturnType<typeof setInterval>;
  private branchRequests = new Map<string, number>();
  private failures = 0;
  private notification?: ReturnType<typeof setTimeout>;
  private generation = 0;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  snapshot = () => this.state;
  private set(patch: Partial<State>) {
    this.state = { ...this.state, ...patch };
    if (!this.notification)
      this.notification = setTimeout(() => {
        this.notification = undefined;
        this.listeners.forEach((fn) => fn());
      }, 16);
  }
  start = async () => {
    this.stop();
    this.stopped = false;
    const generation = ++this.generation;
    this.set({ status: "connecting", error: null });
    try {
      const [agents, projects, models] = await Promise.all([
        api<Agent[]>("/v1/agents?include_settled=true"),
        api<Project[]>("/v1/projects"),
        api<Model[]>("/v1/models"),
      ]);
      if (this.stopped || generation !== this.generation) return;
      // Fresh replay on an explicit restart, with no persisted cursor/state mismatch.
      this.set({
        ...initial(),
        agents: Object.fromEntries(
          agents.map((a) => [a.id, { ...a, queue: [] }]),
        ),
        projects,
        models,
        loaded: true,
      });
      this.connect();
      void this.refreshAgentProjects().catch(() => {});
      this.branchPoll = setInterval(() => {
        void this.refreshAgentProjects().catch(() => {});
      }, 3000);
      this.poll = setInterval(() => {
        void this.refreshProjects().catch(() => {});
      }, 15000);
    } catch (error) {
      if (!this.stopped && generation === this.generation)
        this.set({ status: "offline", error: String(error) });
    }
  };
  stop = () => {
    this.stopped = true;
    this.generation++;
    clearTimeout(this.retry);
    clearInterval(this.poll);
    clearInterval(this.branchPoll);
    this.socket?.close();
    this.socket = undefined;
  };
  select = (id?: string) => {
    this.selected = id;
    this.sendSubscription();
    void this.refreshAgentProjects().catch(() => {});
  };
  private sendSubscription() {
    if (this.socket?.readyState !== WebSocket.OPEN) return;
    this.socket.send(
      JSON.stringify({
        type: "subscribe",
        request_id: requestKey(),
        subscribe_all: true,
        // Explicitly include settled agents for initial history and external restore
        // updates. All mode discovers newly created agents without polling.
        agent_ids: [
          ...new Set([
            ...Object.keys(this.state.agents),
            ...(this.selected && this.state.agents[this.selected]
              ? [this.selected]
              : []),
          ]),
        ],
        cursors: this.state.cursors,
      }),
    );
  }
  private connect() {
    if (this.stopped) return;
    const url = new URL("/v1/ws", location.href);
    url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
    const ws = new WebSocket(url);
    this.socket = ws;
    let chain = Promise.resolve();
    let failed = false;
    let fatal = false;
    ws.onopen = () => {
      if (ws !== this.socket) return;
      this.set({ status: "connecting" });
      this.sendSubscription();
    };
    ws.onmessage = ({ data }) => {
      chain = chain
        .then(async () => {
          if (failed || ws !== this.socket || this.stopped) return;
          const frame = JSON.parse(data) as Frame;
          if (frame.type === "subscribed") {
            this.failures = 0;
            this.set({ status: "live", error: null });
          }
          if (frame.type === "inventory") {
            const previous = this.state.agents[frame.agent.id];
            this.set({
              agents: {
                ...this.state.agents,
                [frame.agent.id]: { ...previous, ...frame.agent },
              },
              ready: {
                ...this.state.ready,
                [frame.agent.id]:
                  this.state.ready[frame.agent.id] ||
                  (this.state.cursors[frame.agent.id] || 0) >=
                    frame.agent.cursor,
              },
            });
            if (
              frame.agent.project_id &&
              !this.state.projects.some((p) => p.id === frame.agent.project_id)
            )
              void this.refreshProjects().catch(() => {});
          }
          if (frame.type === "event" || frame.type === "event_ref") {
            // Never trust an arbitrary URL from the wire; reconstruct the API path.
            const event =
              frame.type === "event"
                ? frame.event
                : await api<Event>(
                    `${agentPath(frame.agent_id)}/events/${frame.cursor}`,
                  );
            if (failed || ws !== this.socket || this.stopped) return;
            this.set(reduceEvent(this.state, event));
          }
          if (frame.type === "error") {
            fatal = true;
            throw new APIError(frame.code, frame.message);
          }
        })
        .catch((error) => {
          failed = true;
          if (ws !== this.socket || this.stopped) return;
          this.set({ error: String(error), status: "offline" });
          ws.close();
        });
    };
    ws.onclose = () => {
      if (this.stopped || ws !== this.socket) return;
      this.set({ status: "offline" });
      if (!fatal)
        this.retry = setTimeout(
          () => this.connect(),
          Math.min(1000 * 2 ** this.failures++, 15000),
        );
    };
    ws.onerror = () => ws.close();
  }
  refreshProjects = async () => {
    const generation = this.generation;
    const projects = await api<Project[]>("/v1/projects");
    if (!this.stopped && this.generation === generation)
      this.set({
        projects: projects.map((project) => ({
          ...this.state.projects.find((p) => p.id === project.id),
          ...project,
        })),
      });
    if (!this.stopped && this.generation === generation)
      void this.refreshAgentProjects();
  };
  private refreshAgentProjects = async () => {
    if (this.stopped) return;
    const generation = this.generation;
    // Branches describe the shared project directory, not a historical turn.
    // Refresh every represented project so unselected sidebar chats stay current.
    const ids = new Set(
      Object.values(this.state.agents).map((agent) => agent.project_id),
    );
    await Promise.allSettled(
      this.state.projects
        .filter((project) => ids.has(project.id))
        .map(async ({ id }) => {
          if (this.branchRequests.get(id) === generation) return;
          this.branchRequests.set(id, generation);
          try {
            const project = await api<Project>(
              `/v1/projects/${encodeURIComponent(id)}`,
            );
            if (this.stopped || this.generation !== generation) return;
            this.set({
              projects: this.state.projects.map((p) =>
                p.id === id ? { ...p, git_branch: project.git_branch } : p,
              ),
            });
          } finally {
            if (this.branchRequests.get(id) === generation)
              this.branchRequests.delete(id);
          }
        }),
    );
  };
  // HTTP is used for mutations: it supports large messages and durable retry keys.
  // WebSocket events remain the single source of transcript/queue updates.
  createAgent = async (project: string, key: string) => {
    const agent = await api<Agent>(
      "/v1/agents",
      "POST",
      { project_id: project },
      key,
    );
    this.set({
      agents: {
        ...this.state.agents,
        [agent.id]: { ...this.state.agents[agent.id], ...agent },
      },
    });
    return agent;
  };
  settle = (id: string, settled: boolean) =>
    api<Agent>(agentPath(id), "PATCH", { settled });
  settings = (id: string, settings: Schema["SettingsPatch"]) =>
    api<Agent>(`${agentPath(id)}/settings`, "PATCH", settings);
  continue = (id: string) => api<Agent>(`${agentPath(id)}/continue`, "POST");
  stopTurn = (id: string, turnId: string) =>
    api<Agent>(`${agentPath(id)}/stop`, "POST", { turn_id: turnId });
  cancel = (id: string, messageId: string) =>
    api<void>(
      `${agentPath(id)}/messages/${encodeURIComponent(messageId)}`,
      "DELETE",
    );
  send = async (id: string, text: string, key: string) => {
    // Check current server state, not a potentially stale sidebar summary.
    const agent = await api<Agent>(agentPath(id));
    if (agent.settled) await this.settle(id, false);
    return api<QueueMessage>(
      `${agentPath(id)}/messages`,
      "POST",
      { text },
      key,
    );
  };
}
export const control = new ControlPlane();
export function useControl() {
  return useSyncExternalStore(control.subscribe, control.snapshot);
}

// A store replacement must not leave an old singleton/socket attached to the UI.
if (import.meta.hot) {
  import.meta.hot.dispose(() => control.stop());
  import.meta.hot.accept(() => location.reload());
}

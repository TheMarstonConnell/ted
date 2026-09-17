import type { Schema } from "./api";

// Dedicated protocol, separate from transcript replay.
export type LiveTab = Schema["BrowserLiveTab"];
export type LiveCommand = Schema["BrowserLiveCommand"];
type LiveEvent = Schema["BrowserLiveEvent"];
export type LiveFrame = Omit<Extract<LiveEvent, { type: "frame" }>, "type">;
export type LiveActivity = Omit<
  Extract<LiveEvent, { type: "activity" }>,
  "type"
> & {
  x: number;
  y: number;
  sequence?: number;
};
export type LiveSnapshot = {
  status: "connecting" | "live" | "reconnecting";
  tabs: LiveTab[];
  selected: string;
  viewed: string;
  pinned: string;
  frame: LiveFrame | null;
  activity: LiveActivity | null;
  error: string | null;
};

export function browserSocketURL(agentId: string, origin: string) {
  const url = new URL(
    `/v1/agents/${encodeURIComponent(agentId)}/browser`,
    origin,
  );
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  return url.href;
}

export function browserPoint(
  clientX: number,
  clientY: number,
  rect: Pick<DOMRect, "left" | "top" | "width" | "height">,
  width: number,
  height: number,
) {
  if (rect.width <= 0 || rect.height <= 0 || width <= 0 || height <= 0)
    return null;
  return {
    x: Math.max(
      0,
      Math.min(width - 0.01, ((clientX - rect.left) * width) / rect.width),
    ),
    y: Math.max(
      0,
      Math.min(height - 0.01, ((clientY - rect.top) * height) / rect.height),
    ),
  };
}
export function browserModifiers(event: {
  altKey: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
  shiftKey: boolean;
}) {
  return (
    (event.altKey ? 1 : 0) |
    (event.ctrlKey ? 2 : 0) |
    (event.metaKey ? 4 : 0) |
    (event.shiftKey ? 8 : 0)
  );
}
export function browserWheel(
  deltaX: number,
  deltaY: number,
  mode: number,
  width: number,
  height: number,
  scale: number,
) {
  const bound = (delta: number) => Math.max(-100000, Math.min(100000, delta));
  return {
    delta_x: bound(deltaX * (mode === 1 ? 16 : mode === 2 ? width : scale)),
    delta_y: bound(deltaY * (mode === 1 ? 16 : mode === 2 ? height : scale)),
  };
}

// AltGraph and macOS Option can produce characters rather than shortcuts.
// CDP keyDown with Ctrl+Alt flags does not insert these even with `text` set.
export function browserCommittedText(
  event: {
    key: string;
    altKey: boolean;
    ctrlKey: boolean;
    metaKey: boolean;
    getModifierState: (key: "AltGraph") => boolean;
  },
  platform: string,
) {
  const character = Array.from(event.key).length === 1;
  const option =
    /Mac|iPhone|iPad/.test(platform) &&
    event.altKey &&
    !event.ctrlKey &&
    !event.metaKey;
  return character && (event.getModifierState("AltGraph") || option)
    ? event.key
    : null;
}

// One socket per open panel. Reconnect only re-subscribes; never opens Chrome.
export class LiveBrowserConnection {
  private state: LiveSnapshot = {
    status: "connecting",
    tabs: [],
    selected: "",
    viewed: "",
    pinned: "",
    frame: null,
    activity: null,
    error: null,
  };
  private listeners = new Set<() => void>();
  private socket: WebSocket | null = null;
  private retry: ReturnType<typeof setTimeout> | undefined;
  private fade: ReturnType<typeof setTimeout> | undefined;
  private attempts = 0;
  private activitySequence = 0;
  private pendingFrame: LiveFrame | null = null;
  private stopped = false;
  private url: string;
  private createSocket: (url: string) => WebSocket;
  constructor(
    url: string,
    createSocket: (url: string) => WebSocket = (url) => new WebSocket(url),
  ) {
    this.url = url;
    this.createSocket = createSocket;
  }
  snapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private update(patch: Partial<LiveSnapshot>) {
    this.state = { ...this.state, ...patch };
    this.listeners.forEach((listener) => listener());
  }
  start = () => {
    this.stopped = false;
    this.connect();
  };
  private connect() {
    if (this.stopped) return;
    let socket: WebSocket;
    try {
      socket = this.createSocket(this.url);
    } catch {
      this.scheduleRetry();
      return;
    }
    this.socket = socket;
    socket.onopen = () => {
      if (socket !== this.socket || this.stopped) return;
      this.attempts = 0;
      this.update({ error: null });
      this.send({ type: "watch", tab_id: this.state.pinned });
    };
    socket.onmessage = (event) => {
      if (socket !== this.socket || this.stopped) return;
      try {
        this.receive(JSON.parse(String(event.data)) as LiveEvent);
      } catch {
        this.update({
          error: "Invalid browser update. Reconnect to try again.",
        });
      }
    };
    socket.onerror = () => {
      if (socket === this.socket) socket.close();
    };
    socket.onclose = () => {
      if (socket !== this.socket || this.stopped) return;
      this.socket = null;
      this.scheduleRetry();
    };
  }
  private scheduleRetry() {
    this.pendingFrame = null;
    this.update({
      status: "reconnecting",
      frame: null,
      activity: null,
    });
    clearTimeout(this.retry);
    this.retry = setTimeout(
      () => this.connect(),
      Math.min(1000 * 2 ** this.attempts++, 10000),
    );
  }
  reconnect = () => {
    this.disconnect();
    this.pendingFrame = null;
    this.update({
      status: "connecting",
      error: null,
      frame: null,
      activity: null,
    });
    this.start();
  };
  disconnect = () => {
    this.stopped = true;
    clearTimeout(this.retry);
    clearTimeout(this.fade);
    const socket = this.socket;
    this.socket = null;
    socket?.close();
  };
  send = (command: LiveCommand) => {
    if (
      command.type === "release" &&
      (!command.tab_id ||
        !this.state.tabs.some((tab) => tab.id === command.tab_id))
    )
      return false;
    if (
      (command.type === "text" || command.type === "key") &&
      Array.from(command.text ?? "").length > 16384
    ) {
      this.update({
        error: "Text is too long. Paste at most 16,384 characters at a time.",
      });
      return false;
    }
    if (this.socket?.readyState !== 1) return false;
    try {
      const payload = JSON.stringify(command);
      if (new TextEncoder().encode(payload).length > 64 * 1024) {
        this.update({
          error: "Browser input exceeds the 64 KiB message limit.",
        });
        return false;
      }
      this.socket.send(payload);
      return true;
    } catch {
      this.update({
        error: "Browser input could not be sent. Reconnect to try again.",
      });
      return false;
    }
  };
  watch = (tabId = "") => {
    this.pendingFrame = null;
    this.send({ type: "release", tab_id: this.state.viewed });
    const desired = tabId || this.state.selected;
    const sameView = !!desired && desired === this.state.viewed;
    this.update({
      pinned: tabId,
      activity: null,
      ...(sameView ? {} : { viewed: "", frame: null }),
    });
    this.send({ type: "watch", tab_id: tabId });
  };
  command = (command: LiveCommand) => {
    if (
      ["navigate", "back", "forward", "reload", "close"].includes(command.type)
    ) {
      this.send({ type: "release", tab_id: this.state.viewed });
      this.pendingFrame = null;
      // Keep the last image until Chrome replaces it. Back at the start of
      // history or closing a background tab may not produce another frame.
      this.update({ activity: null, error: null });
    }
    return this.send(command);
  };
  private receive(event: LiveEvent) {
    switch (event.type) {
      case "state": {
        const tabs = event.tabs ?? [];
        const viewed = event.tab_id ?? "";
        const viewChanged = viewed !== this.state.viewed;
        const changed =
          viewChanged ||
          tabs.find((t) => t.id === viewed)?.url !==
            this.state.tabs.find((t) => t.id === viewed)?.url;
        const pinned = event.pinned ?? "";
        const pinClosed = !!pinned && !tabs.some((tab) => tab.id === pinned);
        if (changed && tabs.some((tab) => tab.id === this.state.viewed))
          this.send({ type: "release", tab_id: this.state.viewed });
        const pendingFrame =
          this.pendingFrame?.tab_id === viewed ? this.pendingFrame : null;
        const pendingClosed =
          !!this.pendingFrame &&
          this.state.tabs.some((tab) => tab.id === this.pendingFrame?.tab_id) &&
          !tabs.some((tab) => tab.id === this.pendingFrame?.tab_id);
        if (pendingFrame || pendingClosed) this.pendingFrame = null;
        this.update({
          tabs,
          viewed,
          selected: event.selected ?? "",
          status: "live",
          ...(changed ? { activity: null } : {}),
          ...(viewChanged ? { frame: null } : {}),
          pinned: pinClosed ? "" : pinned,
          ...(pendingFrame ? { frame: pendingFrame } : {}),
        });
        if (pinClosed) this.send({ type: "watch", tab_id: "" });
        break;
      }
      case "frame":
        if (event.tab_id !== this.state.viewed || !this.state.viewed) {
          this.pendingFrame = event;
          return;
        }
        this.update({ frame: event });
        break;
      case "activity":
        if (event.tab_id !== this.state.viewed) return;
        clearTimeout(this.fade);
        if (event.kind === "clear") {
          this.update({ activity: null });
          return;
        }
        this.update({
          activity: {
            x: 0,
            y: 0,
            ...event,
            sequence: ++this.activitySequence,
          },
        });
        this.fade = setTimeout(() => this.update({ activity: null }), 1500);
        break;
      case "error":
        this.update({
          error: event.message,
        });
        break;
      default:
        throw new Error("Unknown browser event");
    }
  }
}

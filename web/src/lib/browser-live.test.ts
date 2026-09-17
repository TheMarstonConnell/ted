import { afterEach, describe, expect, it, vi } from "vitest";
import {
  LiveBrowserConnection,
  browserModifiers,
  browserCommittedText,
  browserPoint,
  browserSocketURL,
  browserWheel,
  type LiveCommand,
} from "./browser-live";

class Socket {
  readyState = 0;
  sent: LiveCommand[] = [];
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  send(raw: string) {
    this.sent.push(JSON.parse(raw));
  }
  open() {
    this.readyState = 1;
    this.onopen?.();
  }
  close() {
    this.readyState = 3;
    this.onclose?.();
  }
  event(data: unknown) {
    this.onmessage?.({ data: JSON.stringify(data) });
  }
}
function setup() {
  const sockets: Socket[] = [];
  const connection = new LiveBrowserConnection(
    "ws://localhost/v1/agents/a/browser",
    () => {
      const socket = new Socket();
      sockets.push(socket);
      return socket as unknown as WebSocket;
    },
  );
  connection.start();
  sockets[0].open();
  return { connection, sockets, socket: sockets[0] };
}
const tabs = [
  { id: "one", title: "One", url: "https://one.test" },
  { id: "two", title: "Two", url: "https://two.test" },
];
const state = (tab_id = "one", selected = "one", pinned = "") => ({
  type: "state",
  tabs,
  selected,
  pinned,
  tab_id,
});
const frame = (tab_id = "one") => ({
  type: "frame",
  tab_id,
  width: 1280,
  height: 720,
  data: "jpeg",
});
afterEach(() => vi.useRealTimers());

describe("dedicated live-browser transport", () => {
  it("uses a same-origin encoded endpoint, and subscription never starts a browser", () => {
    expect(browserSocketURL("a/b", "https://ted.test:8080")).toBe(
      "wss://ted.test:8080/v1/agents/a%2Fb/browser",
    );
    expect(browserSocketURL("a", "http://localhost")).toBe(
      "ws://localhost/v1/agents/a/browser",
    );
    const { connection, socket } = setup();
    expect(socket.sent).toEqual([{ type: "watch", tab_id: "" }]);
    socket.event({ type: "state" });
    expect(connection.snapshot()).toMatchObject({
      tabs: [],
      initialized: true,
      frame: null,
    });
    connection.disconnect();
    expect(socket.sent).toEqual([{ type: "watch", tab_id: "" }]);
  });
  it("pins the viewer without selecting the agent tab, filters frames and returns to follow on close", () => {
    const { connection, socket } = setup();
    socket.event(state());
    socket.event(frame());
    expect(connection.snapshot().frame?.tab_id).toBe("one");
    connection.watch("two");
    expect(connection.snapshot()).toMatchObject({
      pinned: "two",
      selected: "one",
      frame: null,
    });
    socket.event(frame());
    expect(connection.snapshot().frame).toBeNull();
    socket.event(state("two", "one", "two"));
    socket.event(frame("two"));
    expect(connection.snapshot().frame?.tab_id).toBe("two");
    socket.event({ ...state("one", "one", "two"), tabs: tabs.slice(0, 1) });
    expect(connection.snapshot().pinned).toBe("");
    expect(socket.sent.at(-1)).toEqual({ type: "watch", tab_id: "" });
    connection.disconnect();
  });
  it("only shows current-tab activity, accepts zero coordinates, expires and clears on navigation", () => {
    vi.useFakeTimers();
    const { connection, socket } = setup();
    socket.event(state());
    socket.event({ type: "activity", tab_id: "two", kind: "move", x: 1, y: 2 });
    expect(connection.snapshot().activity).toBeNull();
    socket.event({ type: "activity", tab_id: "one", kind: "click" });
    expect(connection.snapshot().activity).toMatchObject({
      x: 0,
      y: 0,
      kind: "click",
    });
    vi.advanceTimersByTime(1499);
    expect(connection.snapshot().activity).not.toBeNull();
    vi.advanceTimersByTime(1);
    expect(connection.snapshot().activity).toBeNull();
    socket.event({
      type: "activity",
      tab_id: "one",
      kind: "fill",
      x: 10,
      y: 20,
    });
    socket.event({
      ...state(),
      tabs: [{ ...tabs[0], url: "https://other.test" }],
    });
    expect(connection.snapshot().activity).toBeNull();
    socket.event(frame());
    connection.command({ type: "reload", tab_id: "one" });
    expect(connection.snapshot().frame?.tab_id).toBe("one");
    expect(socket.sent.slice(-2)).toEqual([
      { type: "release", tab_id: "one" },
      { type: "reload", tab_id: "one" },
    ]);
    connection.disconnect();
  });
  it("reconnects with the pinned watch but never replays human input, rejects stale socket events", () => {
    vi.useFakeTimers();
    const { connection, sockets, socket } = setup();
    socket.event(state());
    connection.watch("two");
    socket.event(state("two", "one", "two"));
    socket.event(frame("two"));
    connection.send({ type: "text", tab_id: "two", text: "private" });
    socket.close();
    expect(connection.snapshot()).toMatchObject({
      status: "reconnecting",
      frame: null,
    });
    expect(connection.send({ type: "new" })).toBe(false);
    vi.advanceTimersByTime(1000);
    sockets[1].open();
    expect(sockets[1].sent).toEqual([{ type: "watch", tab_id: "two" }]);
    socket.event({ type: "error", message: "stale" });
    expect(connection.snapshot().error).toBeNull();
    connection.disconnect();
    vi.advanceTimersByTime(20000);
    expect(sockets).toHaveLength(2);
  });
  it("surfaces errors, protects oversized pastes and handles malformed JSON", () => {
    const { connection, socket } = setup();
    socket.event(state());
    socket.onmessage?.({ data: "{" });
    expect(connection.snapshot().error).toContain("Invalid browser update");
    socket.event({ type: "error", message: "Navigation denied" });
    expect(connection.snapshot().error).toBe("Navigation denied");
    const sent = socket.sent.length;
    expect(
      connection.send({ type: "text", tab_id: "one", text: "a".repeat(16385) }),
    ).toBe(false);
    expect(socket.sent).toHaveLength(sent);
    connection.disconnect();
  });
});

it("maps CSS frame coordinates, scaled wheel deltas and CDP modifiers without devicePixelRatio", () => {
  const rect = { left: 100, top: 200, width: 640, height: 360 };
  expect(browserPoint(420, 380, rect, 1280, 720)).toEqual({ x: 640, y: 360 });
  expect(browserPoint(0, 0, rect, 1280, 720)).toEqual({ x: 0, y: 0 });
  expect(browserPoint(9999, 9999, rect, 1280, 720)).toEqual({
    x: 1279.99,
    y: 719.99,
  });
  expect(browserPoint(0, 0, { ...rect, width: 0 }, 1280, 720)).toBeNull();
  expect(
    browserModifiers({
      altKey: true,
      ctrlKey: true,
      metaKey: true,
      shiftKey: true,
    }),
  ).toBe(15);
  expect(browserWheel(2, 3, 0, 1280, 720, 2)).toEqual({
    delta_x: 4,
    delta_y: 6,
  });
  expect(browserWheel(2, 3, 1, 1280, 720, 2)).toEqual({
    delta_x: 32,
    delta_y: 48,
  });
  expect(browserWheel(1, 1, 2, 1280, 720, 2)).toEqual({
    delta_x: 1280,
    delta_y: 720,
  });
  expect(browserWheel(1e6, -1e6, 0, 1280, 720, 2)).toEqual({
    delta_x: 100000,
    delta_y: -100000,
  });
});

it("keeps an early frame through an unrelated periodic state until its matching state", () => {
  const { connection, socket } = setup();
  socket.event({ ...state(), tabs: tabs.slice(0, 1) });
  socket.event(frame("two"));
  socket.event({ ...state(), tabs: tabs.slice(0, 1) });
  expect(connection.snapshot().frame).toBeNull();
  socket.event(state("two", "one", "two"));
  expect(connection.snapshot().frame?.tab_id).toBe("two");
  connection.disconnect();
});

it("drops a pending frame once a previously known tab is confirmed closed", () => {
  const { connection, socket } = setup();
  socket.event(state());
  socket.event(frame("two"));
  socket.event({ ...state(), tabs: tabs.slice(0, 1) });
  socket.event(state("two", "one", "two"));
  expect(connection.snapshot().frame).toBeNull();
  connection.disconnect();
});

it("new tabs pin only the viewer and a frame arriving before its state is not lost", () => {
  const { connection, socket } = setup();
  socket.event({ ...state(), tabs: tabs.slice(0, 1) });
  connection.command({ type: "new", url: "about:blank" });
  socket.event(frame("two"));
  expect(connection.snapshot().frame).toBeNull();
  socket.event(state("two", "one", "two"));
  expect(connection.snapshot()).toMatchObject({
    selected: "one",
    viewed: "two",
    pinned: "two",
    frame: { tab_id: "two" },
  });
  // A periodic URL update may follow the newest frame. It must not erase that
  // image: a static page may never emit another screencast frame.
  socket.event({
    ...state("two", "one", "two"),
    tabs: [tabs[0], { ...tabs[1], url: "https://new.test" }],
  });
  expect(connection.snapshot().frame?.tab_id).toBe("two");
  connection.disconnect();
});

it("same-tab pin/follow and no-op navigation/closing background tabs retain a static frame", () => {
  const { connection, socket } = setup();
  socket.event(state());
  socket.event(frame());
  connection.watch("one");
  socket.event(state("one", "one", "one"));
  expect(connection.snapshot().frame?.tab_id).toBe("one");
  connection.watch();
  socket.event(state());
  expect(connection.snapshot().frame?.tab_id).toBe("one");
  for (const type of ["back", "forward", "reload"] as const) {
    connection.command({ type, tab_id: "one" });
    socket.event(state());
    expect(connection.snapshot().frame?.tab_id).toBe("one");
  }
  connection.command({ type: "close", tab_id: "two" });
  socket.event({ ...state(), tabs: tabs.slice(0, 1) });
  expect(connection.snapshot().frame?.tab_id).toBe("one");
  const beforeClose = socket.sent.length;
  socket.event({ type: "state", tabs: [] });
  connection.send({ type: "release", tab_id: "one" });
  expect(socket.sent).toHaveLength(beforeClose);
  connection.disconnect();
});

it("commits AltGraph and Option characters as text without swallowing genuine shortcuts", () => {
  const event = {
    key: "@",
    altKey: true,
    ctrlKey: true,
    metaKey: false,
    getModifierState: (key: string) => key === "AltGraph",
  };
  expect(browserCommittedText(event, "Win32")).toBe("@");
  expect(
    browserCommittedText(
      { ...event, key: "€", ctrlKey: false, getModifierState: () => false },
      "MacIntel",
    ),
  ).toBe("€");
  expect(browserCommittedText({ ...event, key: "Dead" }, "Win32")).toBeNull();
  expect(
    browserCommittedText(
      { ...event, key: "f", ctrlKey: false, getModifierState: () => false },
      "Linux x86_64",
    ),
  ).toBeNull();
});

it("uses authoritative pin when human and agent create tabs concurrently and reconnects that pin", () => {
  vi.useFakeTimers();
  const { connection, socket, sockets } = setup();
  socket.event({ ...state(), tabs: tabs.slice(0, 1) });
  connection.command({ type: "new", url: "about:blank" });
  socket.event(state("two", "two", ""));
  expect(connection.snapshot().pinned).toBe("");
  socket.event({
    ...state("human", "two", "human"),
    tabs: [...tabs, { id: "human", title: "New tab", url: "about:blank" }],
  });
  expect(connection.snapshot()).toMatchObject({
    pinned: "human",
    selected: "two",
    viewed: "human",
  });
  socket.close();
  vi.advanceTimersByTime(1000);
  sockets[1].open();
  expect(sockets[1].sent).toEqual([{ type: "watch", tab_id: "human" }]);
  connection.disconnect();
});

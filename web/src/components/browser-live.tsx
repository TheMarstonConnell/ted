import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  useSyncExternalStore,
  type KeyboardEvent,
  type PointerEvent,
} from "react";
import {
  ArrowLeft,
  ArrowRight,
  Globe,
  Maximize2,
  Minimize2,
  MousePointer2,
  Plus,
  RotateCw,
  X,
} from "lucide-react";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { cn } from "@/lib/utils";
import {
  LiveBrowserConnection,
  browserSocketURL,
  browserPoint,
  browserModifiers,
  browserCommittedText,
  browserWheel,
  type LiveCommand,
  type LiveFrame,
  type LiveActivity,
} from "@/lib/browser-live";

export function BrowserPanel({
  agentId,
  expanded,
  onExpand,
  onClose,
}: {
  agentId: string;
  expanded: boolean;
  onExpand: () => void;
  onClose: () => void;
}) {
  const [connection] = useState(
    () =>
      new LiveBrowserConnection(
        browserSocketURL(agentId, window.location.origin),
      ),
  );
  const state = useSyncExternalStore(connection.subscribe, connection.snapshot);
  useEffect(() => {
    connection.start();
    return connection.disconnect;
  }, [connection]);
  const tab = state.tabs.find((tab) => tab.id === state.viewed);
  const [address, setAddress] = useState("");
  const addressEditing = useRef(false);
  const closeRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!addressEditing.current) setAddress(tab?.url || "");
  }, [tab?.url, tab?.id]);
  const connected = state.status === "live" && state.initialized;
  const command = (type: LiveCommand["type"]) =>
    connection.command({ type, tab_id: state.viewed });
  return (
    <section
      aria-label="Live browser"
      className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden bg-background lg:border-l"
    >
      <div className="flex shrink-0 items-center gap-2 border-b p-2">
        <Globe className="ml-2 size-4 shrink-0" aria-hidden="true" />
        <h2 className="min-w-0 flex-1 text-sm font-semibold">Browser</h2>
        <Button
          variant="ghost"
          size="icon"
          className="hidden lg:inline-flex"
          aria-label={expanded ? "Narrow browser" : "Expand browser"}
          onClick={onExpand}
        >
          {expanded ? <Minimize2 /> : <Maximize2 />}
        </Button>
        <Button
          ref={closeRef}
          variant="ghost"
          size="icon"
          aria-label="Close browser panel"
          onClick={onClose}
        >
          <X />
        </Button>
      </div>
      <p className="shrink-0 border-b px-4 py-2 text-xs text-muted-foreground">
        Shared with Ted · Always interactive. Your actions don’t pause the
        agent.
      </p>
      <div
        className="flex shrink-0 items-center gap-2 overflow-x-auto border-b p-2"
        aria-label="Browser tabs"
      >
        <Button
          size="sm"
          variant={!state.pinned ? "secondary" : "ghost"}
          aria-pressed={!state.pinned}
          disabled={!connected}
          onClick={() => connection.watch()}
        >
          Follow agent
        </Button>
        {state.tabs.map((item) => (
          <div
            key={item.id}
            className={cn(
              "flex shrink-0 items-center rounded-md border",
              state.viewed === item.id && "bg-muted",
            )}
          >
            <Button
              size="sm"
              variant="ghost"
              className="max-w-40 justify-start"
              aria-pressed={state.pinned === item.id}
              title={`${item.title || item.url}${item.id === state.selected ? " · Ted’s tab" : ""}`}
              disabled={!connected}
              onClick={() => connection.watch(item.id)}
            >
              <span className="truncate">
                {item.title || item.url || "New tab"}
              </span>
              {item.id === state.selected && (
                <span className="text-xs text-muted-foreground">Ted</span>
              )}
            </Button>
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label={`Close tab ${item.title || item.url || "New tab"}`}
              disabled={!connected}
              onClick={() =>
                connection.command({ type: "close", tab_id: item.id })
              }
            >
              <X />
            </Button>
          </div>
        ))}
        <Button
          size="icon-sm"
          variant="ghost"
          aria-label="New browser tab"
          disabled={!connected}
          onClick={() =>
            connection.command({ type: "new", url: "about:blank" })
          }
        >
          <Plus />
        </Button>
      </div>
      <form
        aria-label="Browser navigation"
        className="flex shrink-0 flex-wrap items-center gap-2 border-b p-2"
        onSubmit={(event) => {
          event.preventDefault();
          if (address.trim())
            connection.command({
              type: "navigate",
              tab_id: state.viewed || undefined,
              url: address.trim(),
            });
        }}
      >
        <div className="flex items-center gap-2">
          <Button
            type="button"
            size="icon-sm"
            variant="ghost"
            aria-label="Browser back"
            disabled={!connected || !tab}
            onClick={() => command("back")}
          >
            <ArrowLeft />
          </Button>
          <Button
            type="button"
            size="icon-sm"
            variant="ghost"
            aria-label="Browser forward"
            disabled={!connected || !tab}
            onClick={() => command("forward")}
          >
            <ArrowRight />
          </Button>
          <Button
            type="button"
            size="icon-sm"
            variant="ghost"
            aria-label="Reload browser page"
            disabled={!connected || !tab}
            onClick={() => command("reload")}
          >
            <RotateCw />
          </Button>
        </div>
        <div className="flex min-w-0 flex-[1_1_16rem] items-center gap-2">
          <Input
            aria-label="Browser address"
            placeholder="Enter a URL"
            value={address}
            disabled={!connected}
            onFocus={() => {
              addressEditing.current = true;
            }}
            onBlur={() => {
              addressEditing.current = false;
            }}
            onChange={(event) => setAddress(event.target.value)}
          />
          <Button
            type="submit"
            variant="outline"
            size="sm"
            disabled={!connected || !address.trim()}
          >
            Go
          </Button>
        </div>
      </form>
      {state.error && (
        <div
          role="alert"
          className="flex shrink-0 flex-wrap items-center gap-2 border-b p-4 text-sm"
        >
          <span className="min-w-0 flex-1 break-words">{state.error}</span>
          <Button size="sm" variant="outline" onClick={connection.reconnect}>
            Reconnect browser
          </Button>
        </div>
      )}
      {!connected ? (
        <div
          role="status"
          className="m-auto space-y-4 p-inset text-center text-sm text-muted-foreground"
        >
          <p>
            {state.status === "reconnecting"
              ? "Browser disconnected. Reconnecting…"
              : "Connecting to browser…"}
          </p>
          <Button variant="outline" onClick={connection.reconnect}>
            Reconnect now
          </Button>
        </div>
      ) : !state.tabs.length ? (
        <div className="m-auto space-y-4 p-inset text-center">
          <Globe
            className="mx-auto size-8 text-muted-foreground"
            aria-hidden="true"
          />
          <h3 className="text-sm font-semibold">No browser open</h3>
          <p className="max-w-sm text-sm text-muted-foreground">
            Opening this panel doesn’t start a browser. Open one when you’re
            ready, or wait for Ted.
          </p>
          <Button
            onClick={() =>
              connection.command({ type: "new", url: "about:blank" })
            }
          >
            Open browser
          </Button>
        </div>
      ) : state.frame ? (
        <BrowserViewport
          key={state.frame.tab_id}
          frame={state.frame}
          activity={state.activity}
          send={connection.send}
          onEscape={() => closeRef.current?.focus()}
        />
      ) : (
        <p
          role="status"
          className="m-auto p-inset text-sm text-muted-foreground"
        >
          Waiting for the browser’s next frame…
        </p>
      )}
    </section>
  );
}

function BrowserViewport({
  frame,
  activity,
  send,
  onEscape,
}: {
  frame: LiveFrame;
  activity: LiveActivity | null;
  send: (command: LiveCommand) => boolean;
  onEscape: () => void;
}) {
  const host = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLTextAreaElement>(null);
  const [size, setSize] = useState({ width: 0, height: 0 });
  const composing = useRef(false);
  const blurring = useRef(false);
  const pressed = useRef(false);
  const capturedPointer = useRef<number | null>(null);
  const move = useRef<LiveCommand | null>(null);
  const moveTimer = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  );
  const wheel = useRef<LiveCommand | null>(null);
  const wheelTimer = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  );
  const touch = useRef<{
    pointerId: number;
    startX: number;
    startY: number;
    lastX: number;
    lastY: number;
    panning: boolean;
  } | null>(null);
  const click = useRef({ time: 0, x: 0, y: 0, button: -1, count: 0 });
  const current = useRef(frame);
  useLayoutEffect(() => {
    current.current = frame;
  }, [frame]);
  const flushMove = useCallback(() => {
    clearTimeout(moveTimer.current);
    moveTimer.current = undefined;
    if (move.current) {
      send(move.current);
      move.current = null;
    }
  }, [send]);
  const flushWheel = useCallback(() => {
    clearTimeout(wheelTimer.current);
    wheelTimer.current = undefined;
    if (wheel.current) {
      send(wheel.current);
      wheel.current = null;
    }
  }, [send]);
  const queueWheel = useCallback(
    (command: LiveCommand) => {
      const bound = (delta: number) =>
        Math.max(-100000, Math.min(100000, delta));
      wheel.current = {
        ...command,
        delta_x: bound((wheel.current?.delta_x || 0) + (command.delta_x || 0)),
        delta_y: bound((wheel.current?.delta_y || 0) + (command.delta_y || 0)),
      };
      if (!wheelTimer.current) wheelTimer.current = setTimeout(flushWheel, 32);
    },
    [flushWheel],
  );
  useEffect(() => {
    const element = host.current!;
    const resize = new ResizeObserver(([entry]) =>
      setSize({
        width: entry.contentRect.width,
        height: entry.contentRect.height,
      }),
    );
    resize.observe(element);
    return () => resize.disconnect();
  }, []);
  const release = useCallback(
    (blur = false) => {
      clearTimeout(moveTimer.current);
      clearTimeout(wheelTimer.current);
      moveTimer.current = undefined;
      wheelTimer.current = undefined;
      move.current = null;
      wheel.current = null;
      pressed.current = false;
      touch.current = null;
      const id = capturedPointer.current;
      capturedPointer.current = null;
      if (id !== null && input.current?.hasPointerCapture(id))
        input.current.releasePointerCapture(id);
      send({ type: "release", tab_id: current.current.tab_id });
      if (blur) {
        blurring.current = true;
        input.current?.blur();
        blurring.current = false;
      }
    },
    [send],
  );
  useEffect(() => {
    const releaseAndBlur = () => release(true);
    const visibility = () => {
      if (document.hidden) releaseAndBlur();
    };
    window.addEventListener("blur", releaseAndBlur);
    document.addEventListener("visibilitychange", visibility);
    return () => {
      window.removeEventListener("blur", releaseAndBlur);
      document.removeEventListener("visibilitychange", visibility);
      releaseAndBlur();
    };
  }, [release]);
  useEffect(() => {
    const element = input.current!;
    const nativeWheel = (event: WheelEvent) => {
      event.preventDefault();
      const frame = current.current;
      const rect = element.getBoundingClientRect();
      const point = browserPoint(
        event.clientX,
        event.clientY,
        rect,
        frame.width,
        frame.height,
      );
      if (!point) return;
      queueWheel({
        type: "mouse",
        tab_id: frame.tab_id,
        event: "mouseWheel",
        ...point,
        ...browserWheel(
          event.deltaX,
          event.deltaY,
          event.deltaMode,
          frame.width,
          frame.height,
          frame.width / rect.width,
        ),
        modifiers: browserModifiers(event),
      });
    };
    element.addEventListener("wheel", nativeWheel, { passive: false });
    return () => element.removeEventListener("wheel", nativeWheel);
  }, [queueWheel]);
  const pointer = (
    event: PointerEvent<HTMLTextAreaElement>,
    kind: "mouseMoved" | "mousePressed" | "mouseReleased",
  ) => {
    if (!event.isPrimary || event.button > 2) return;
    const element = event.currentTarget;
    const rect = element.getBoundingClientRect();
    const point = browserPoint(
      event.clientX,
      event.clientY,
      rect,
      frame.width,
      frame.height,
    );
    if (event.pointerType === "touch") {
      event.preventDefault();
      if (kind === "mousePressed") {
        element.setPointerCapture(event.pointerId);
        capturedPointer.current = event.pointerId;
        touch.current = {
          pointerId: event.pointerId,
          startX: event.clientX,
          startY: event.clientY,
          lastX: event.clientX,
          lastY: event.clientY,
          panning: false,
        };
        return;
      }
      const gesture = touch.current;
      if (!gesture || gesture.pointerId !== event.pointerId) return;
      if (kind === "mouseMoved") {
        if (
          !gesture.panning &&
          Math.hypot(
            event.clientX - gesture.startX,
            event.clientY - gesture.startY,
          ) >= 8
        )
          gesture.panning = true;
        if (gesture.panning && point) {
          queueWheel({
            type: "mouse",
            tab_id: frame.tab_id,
            event: "mouseWheel",
            ...point,
            ...browserWheel(
              gesture.lastX - event.clientX,
              gesture.lastY - event.clientY,
              0,
              frame.width,
              frame.height,
              frame.width / rect.width,
            ),
            modifiers: browserModifiers(event),
          });
        }
        gesture.lastX = event.clientX;
        gesture.lastY = event.clientY;
        return;
      }
      if (gesture.panning) {
        flushWheel();
      } else if (point) {
        element.focus({ preventScroll: true });
        send({
          type: "mouse",
          tab_id: frame.tab_id,
          event: "mousePressed",
          ...point,
          button: "left",
          buttons: 1,
          click_count: 1,
          modifiers: browserModifiers(event),
        });
        send({
          type: "mouse",
          tab_id: frame.tab_id,
          event: "mouseReleased",
          ...point,
          button: "left",
          buttons: 0,
          click_count: 1,
          modifiers: browserModifiers(event),
        });
      }
      touch.current = null;
      capturedPointer.current = null;
      if (element.hasPointerCapture(event.pointerId))
        element.releasePointerCapture(event.pointerId);
      send({ type: "release", tab_id: frame.tab_id });
      return;
    }
    if (kind === "mouseReleased" && !pressed.current) return;
    if (!point) return;
    if (kind === "mousePressed") {
      event.preventDefault();
      element.focus({ preventScroll: true });
      element.setPointerCapture(event.pointerId);
      capturedPointer.current = event.pointerId;
      pressed.current = true;
      const last = click.current;
      const double =
        performance.now() - last.time < 500 &&
        Math.hypot(event.clientX - last.x, event.clientY - last.y) < 5 &&
        event.button === last.button &&
        last.count === 1;
      click.current = {
        time: performance.now(),
        x: event.clientX,
        y: event.clientY,
        button: event.button,
        count: double ? 2 : 1,
      };
    }
    const buttons = pressed.current ? event.buttons : 0;
    const button =
      kind === "mouseMoved"
        ? buttons & 1
          ? "left"
          : buttons & 2
            ? "right"
            : buttons & 4
              ? "middle"
              : "none"
        : (["left", "middle", "right"] as const)[event.button];
    const command: LiveCommand = {
      type: "mouse",
      tab_id: frame.tab_id,
      event: kind,
      ...point,
      button,
      buttons,
      modifiers: browserModifiers(event),
      ...(kind !== "mouseMoved"
        ? { click_count: click.current.count || 1 }
        : {}),
    };
    if (kind === "mouseMoved") {
      move.current = command;
      if (!moveTimer.current) moveTimer.current = setTimeout(flushMove, 32);
    } else {
      flushMove();
      send(command);
    }
    if (kind === "mouseReleased") {
      pressed.current = false;
      capturedPointer.current = null;
      if (element.hasPointerCapture(event.pointerId))
        element.releasePointerCapture(event.pointerId);
    }
  };
  const keyboard = (
    event: KeyboardEvent<HTMLTextAreaElement>,
    kind: "keyDown" | "keyUp",
  ) => {
    event.stopPropagation();
    if (event.key === "Escape") {
      event.preventDefault();
      release(true);
      onEscape();
      return;
    }
    if (
      event.nativeEvent.isComposing ||
      composing.current ||
      event.keyCode === 229 ||
      event.key === "Dead"
    )
      return;
    const committedText = browserCommittedText(event, navigator.platform);
    if (committedText) {
      event.preventDefault();
      if (kind === "keyDown")
        send({ type: "text", tab_id: frame.tab_id, text: committedText });
      return;
    }
    // Leave paste to the native clipboard event. No clipboard permission or global listeners.
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "v")
      return;
    event.preventDefault();
    const text =
      kind === "keyDown" && !event.ctrlKey && !event.metaKey && !event.altKey
        ? event.key.length === 1
          ? event.key
          : event.key === "Enter"
            ? "\r"
            : undefined
        : undefined;
    send({
      type: "key",
      tab_id: frame.tab_id,
      event: kind,
      key: event.key,
      code: event.code,
      key_code: event.keyCode,
      modifiers: browserModifiers(event),
      ...(text ? { text } : {}),
    });
  };
  const scale = Math.min(size.width / frame.width, size.height / frame.height);
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div
        ref={host}
        className="flex min-h-0 flex-1 items-center justify-center overflow-hidden bg-muted/40"
      >
        <div
          className="relative shrink-0 overflow-hidden focus-within:ring-2 focus-within:ring-inset focus-within:ring-ring"
          style={{ width: frame.width * scale, height: frame.height * scale }}
        >
          <img
            alt="Live browser page"
            draggable={false}
            src={`data:image/jpeg;base64,${frame.data}`}
            className="pointer-events-none absolute inset-0 size-full select-none"
          />
          <textarea
            ref={input}
            aria-label="Interactive browser viewport"
            aria-describedby="browser-focus-help"
            autoCapitalize="off"
            autoComplete="off"
            spellCheck={false}
            className="absolute inset-0 size-full resize-none cursor-default touch-none text-base md:text-sm opacity-0"
            onBlur={() => {
              if (!blurring.current) release();
            }}
            onPointerMove={(event) => pointer(event, "mouseMoved")}
            onPointerDown={(event) => pointer(event, "mousePressed")}
            onPointerUp={(event) => pointer(event, "mouseReleased")}
            onPointerCancel={() => release()}
            onLostPointerCapture={() => {
              if (pressed.current || touch.current) release();
            }}
            onContextMenu={(event) => event.preventDefault()}
            onKeyDown={(event) => keyboard(event, "keyDown")}
            onKeyUp={(event) => keyboard(event, "keyUp")}
            onPaste={(event) => {
              event.preventDefault();
              const text = event.clipboardData.getData("text/plain");
              if (text) send({ type: "text", tab_id: frame.tab_id, text });
            }}
            onCompositionStart={() => {
              composing.current = true;
            }}
            onCompositionEnd={(event) => {
              composing.current = false;
              if (event.data)
                send({ type: "text", tab_id: frame.tab_id, text: event.data });
              event.currentTarget.value = "";
            }}
            onInput={(event) => {
              if (!composing.current && event.currentTarget.value) {
                send({
                  type: "text",
                  tab_id: frame.tab_id,
                  text: event.currentTarget.value,
                });
                event.currentTarget.value = "";
              }
            }}
          />
          {activity && activity.tab_id === frame.tab_id && (
            <div
              key={activity.sequence}
              data-testid="ted-browser-cursor"
              className="browser-agent-cursor pointer-events-none absolute z-10 text-primary"
              style={{
                left: `${(100 * activity.x) / frame.width}%`,
                top: `${(100 * activity.y) / frame.height}%`,
              }}
            >
              {activity.kind === "click" && (
                <span className="absolute -left-4 -top-4 size-8 rounded-full border-2 border-primary bg-primary/20" />
              )}
              <MousePointer2 className="size-6 fill-primary stroke-background" />
              <span className="ml-4 block rounded-sm bg-primary px-2 text-xs font-medium text-primary-foreground">
                {activity.kind === "fill" ? "Ted · fill" : "Ted"}
              </span>
            </div>
          )}
        </div>
      </div>
      <p
        id="browser-focus-help"
        className="shrink-0 border-t px-4 py-2 text-xs text-muted-foreground"
      >
        Click the page to type · Escape releases focus
      </p>
    </div>
  );
}

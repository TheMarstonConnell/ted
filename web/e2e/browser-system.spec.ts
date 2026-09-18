import { test, expect } from "@playwright/test";
import { spawn, execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, mkdir, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { createServer } from "node:http";

const exec = promisify(execFile);
const binary = process.env.TED_BROWSER_SYSTEM_BINARY;
test("real browser shares human input, agent clicks and recording", async ({
  page,
}) => {
  test.skip(
    !binary,
    "set TED_BROWSER_SYSTEM_BINARY to a freshly built ted executable",
  );
  test.setTimeout(90_000);
  page.setDefaultTimeout(10_000);
  const home = await mkdtemp(join(tmpdir(), "ted-browser-system-"));
  const projectRoot = join(home, "project");
  await mkdir(projectRoot);
  const runtimeHome = join(home, "controlplane", "runtime");
  const browserMode = process.env.TED_BROWSER_SYSTEM_MODE || "headless";
  expect(["headless", "headed"]).toContain(browserMode);
  await mkdir(join(runtimeHome, "browser"), { recursive: true, mode: 0o700 });
  await writeFile(
    join(runtimeHome, "browser", "config.json"),
    JSON.stringify({ mode: browserMode }),
    { mode: 0o600 },
  );
  const env = {
    ...process.env,
    TED_HOME: runtimeHome,
    TED_PROJECT_ROOT: projectRoot,
    TED_THREAD_ID: "",
  };
  const daemon = spawn(resolve(binary!), ["browser", "serve"], {
    env,
    detached: true,
    stdio: "ignore",
  });
  await expect
    .poll(async () =>
      stat(join(runtimeHome, "browser", "daemon.sock")).then(
        () => true,
        () => false,
      ),
    )
    .toBe(true);
  const server = spawn(
    resolve(binary!),
    [
      "serve",
      "--addr",
      "127.0.0.1:0",
      "--data-dir",
      join(home, "controlplane"),
    ],
    {
      env: { ...env, TED_HOME: join(home, "server-home") },
      detached: true,
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  let waitingStarted = false;
  const fixture = createServer((req, res) => {
    if (req.url === "/agent-wait") {
      waitingStarted = true;
      res.writeHead(204).end();
      return;
    }
    res.setHeader("Content-Type", "text/html");
    res.end(`<!doctype html><html><head><title>Shared browser playground</title><style>
      body{margin:0;background:#f6f7fb;color:#172033;font:18px system-ui}main{padding:48px;max-width:700px}
      h1{font-size:32px}label{display:block;margin-top:24px}input,button{font:inherit;padding:12px;border:1px solid #bbc3d0;border-radius:8px}
      button{background:#233c72;color:white;cursor:pointer;margin-top:24px}#result{padding:24px 0;font-weight:600}
    </style></head><body><main><small>TED · SHARED BROWSER</small><h1>Human + agent, one browser</h1>
      <p>Type a name and press the button. Both Ted and you can interact with this page.</p>
      <label for="name">Your name</label><input id="name" autocomplete="off">
      <br><button id="greet" onclick="document.querySelector('#result').textContent='Hello, '+document.querySelector('#name').value+'!';document.querySelector('#result').dataset.ready='true'">Say hello</button>
      <div id="result" aria-live="polite">Ready for shared interaction</div><div style="height:1000px"></div>
    </main><script>
      const queryAll = document.querySelectorAll.bind(document);
      let signalled = false;
      document.querySelectorAll = (selector) => {
        if (selector === '#result[data-ready]' && !signalled) {
          signalled = true;
          fetch('/agent-wait');
        }
        return queryAll(selector);
      };
    </script></body></html>`);
  });
  await new Promise<void>((done) => fixture.listen(0, "127.0.0.1", done));
  const address = fixture.address();
  if (!address || typeof address === "string")
    throw new Error("fixture address unavailable");
  const fixtureURL = `http://127.0.0.1:${address.port}`;
  let stderr = "";
  server.stderr.on("data", (chunk) => {
    stderr += String(chunk);
  });
  let agentID = "";
  const browser = async (...args: string[]) => {
    const { stdout } = await exec(
      resolve(binary!),
      ["browser", "--project", projectRoot, "--thread", agentID, ...args],
      { env, timeout: 35_000, maxBuffer: 4 << 20 },
    );
    const result = JSON.parse(stdout);
    expect(result.ok, result.error?.message).toBe(true);
    return result.data;
  };
  try {
    await expect
      .poll(() => stderr.match(/listening on (127\.0\.0\.1:\d+)/)?.[1], {
        timeout: 15_000,
      })
      .toBeTruthy();
    const base = `http://${stderr.match(/listening on (127\.0\.0\.1:\d+)/)![1]}`;
    const models = await (await page.request.get(`${base}/v1/models`)).json();
    expect(models.length).toBeGreaterThan(0);
    const p = await page.request.post(`${base}/v1/projects`, {
      data: {
        root: projectRoot,
        name: "Shared browser demo",
        defaults: {
          model: models[0].id,
          effort: models[0].default_effort || "",
        },
        workspace_defaults: { mode: "current_checkout" },
      },
    });
    expect(p.ok()).toBe(true);
    const project = await p.json();
    const a = await page.request.post(`${base}/v1/agents`, {
      data: { project_id: project.id, title: "Interactive browser" },
    });
    expect(a.ok()).toBe(true);
    agentID = (await a.json()).id;
    await page.goto(`${base}/agents/${agentID}`);
    await page.getByRole("button", { name: "Browser", exact: true }).click();
    // Opening the viewer alone must not launch Chrome.
    await expect(
      page.getByRole("button", { name: "Open browser", exact: true }),
    ).toBeVisible();
    expect((await browser("status")).projects).toEqual([]);
    await page
      .getByRole("button", { name: "Open browser", exact: true })
      .click();
    await expect(
      page.getByRole("textbox", { name: "Interactive browser viewport" }),
    ).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText("1440 × 900", { exact: true })).toBeVisible();
    const version = await browser("cdp", "Browser.getVersion", "--browser");
    expect(version.result.userAgent.includes("HeadlessChrome")).toBe(
      browserMode === "headless",
    );
    const addressBar = page.getByRole("textbox", { name: "Browser address" });
    await addressBar.fill(fixtureURL.replace("http://", ""));
    await addressBar.press("Enter");
    await expect
      .poll(async () => JSON.stringify(await browser("snapshot")))
      .toContain("Human + agent, one browser");
    await expect(addressBar).toHaveValue(fixtureURL + "/");
    const viewport = page.getByRole("textbox", {
      name: "Interactive browser viewport",
    });
    await expect(viewport).toBeVisible({ timeout: 15_000 });
    const position = async (selector: string) => {
      const result = await browser(
        "cdp",
        "Runtime.evaluate",
        "--params",
        JSON.stringify({
          expression: `(()=>{const r=document.querySelector(${JSON.stringify(selector)}).getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2,width:innerWidth,height:innerHeight}})()`,
          returnByValue: true,
        }),
      );
      return result.result.result.value as {
        x: number;
        y: number;
        width: number;
        height: number;
      };
    };
    const clickRemote = async (selector: string) => {
      const p = await position(selector);
      expect({ width: p.width, height: p.height }).toEqual({
        width: 1440,
        height: 900,
      });
      const box = await viewport.boundingBox();
      if (!box) throw new Error("viewport missing");
      await page.mouse.click(
        box.x + (p.x * box.width) / p.width,
        box.y + (p.y * box.height) / p.height,
      );
    };
    await clickRemote("#name");
    await page.keyboard.type("Taylor");
    await expect
      .poll(async () => JSON.stringify(await browser("snapshot")))
      .toContain("Taylor");
    // A waiting agent must not hold up a human click.
    const greet = await position("#greet");
    const viewportBox = await viewport.boundingBox();
    if (!viewportBox) throw new Error("viewport missing");
    const wait = browser("wait", "--selector", "#result[data-ready]");
    await expect.poll(() => waitingStarted).toBe(true);
    await page.mouse.click(
      viewportBox.x + (greet.x * viewportBox.width) / greet.width,
      viewportBox.y + (greet.y * viewportBox.height) / greet.height,
    );
    await wait;
    await expect(page.getByTestId("ted-browser-cursor")).toHaveCount(0);
    await browser("record", "start");
    await browser("click", "--selector", "#greet");
    await expect(page.getByTestId("ted-browser-cursor")).toBeVisible({
      timeout: 1000,
    });
    const recording = await browser("record", "stop");
    expect(recording.path).toBeTruthy();
    expect(recording.frames).toBeGreaterThan(0);
    expect((await stat(recording.path)).size).toBeGreaterThan(0);
    await expect
      .poll(async () => JSON.stringify(await browser("snapshot")))
      .toContain("Hello, Taylor!");
  } finally {
    if (agentID) await browser("close").catch(() => undefined);
    try {
      process.kill(-server.pid!, "SIGTERM");
    } catch {
      /* process exited */
    }
    fixture.closeAllConnections();
    await new Promise<void>((done) => fixture.close(() => done()));
    try {
      process.kill(-daemon.pid!, "SIGTERM");
    } catch {
      /* process exited */
    }
    await new Promise((done) => setTimeout(done, 500));
    await rm(home, { recursive: true, force: true }).catch(() => undefined);
  }
});

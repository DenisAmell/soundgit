import http from "http";
import { FolderWatcher } from "./watcher.js";
import { UploadQueue } from "./queue.js";
import { ConfigStore } from "./config.js";
import { YandexDisk } from "./yandex.js";

type Handler = (body: Record<string, string>, res: http.ServerResponse) => void;

export function createServer(
  config: ConfigStore,
  disk: YandexDisk,
  watcher: FolderWatcher,
  queue: UploadQueue
): http.Server {
  const routes = new Map<string, Handler>();

  // ── GET /status ────────────────────────────────────────────────────────────
  routes.set("GET /status", (_, res) => {
    const stats = queue.getStats();
    json(res, 200, {
      running: true,
      watchFolder: watcher.getFolder(),
      pendingFiles: stats.pending,
      totalUploaded: stats.totalUploaded,
      lastSyncedFile: stats.lastSyncedFile,
      recentErrors: stats.errors.slice(-5),
    });
  });

  // ── POST /config ───────────────────────────────────────────────────────────
  routes.set("POST /config", (body, res) => {
    if (body.watchFolder) {
      config.set("watchFolder", body.watchFolder);
      watcher.watch(body.watchFolder);
    }
    if (body.yandexToken) {
      config.set("yandexToken", body.yandexToken);
      disk.setToken(body.yandexToken);
    }
    if (body.yandexBasePath) {
      config.set("yandexBasePath", body.yandexBasePath);
    }
    json(res, 200, { ok: true, config: config.getAll() });
  });

  // ── POST /pause ────────────────────────────────────────────────────────────
  routes.set("POST /pause", (_, res) => {
    queue.pause();
    json(res, 200, { paused: true });
  });

  // ── POST /resume ───────────────────────────────────────────────────────────
  routes.set("POST /resume", (_, res) => {
    queue.resume();
    json(res, 200, { paused: false });
  });

  // ── POST /sync-now ─────────────────────────────────────────────────────────
  routes.set("POST /sync-now", (_, res) => {
    queue.flush();
    json(res, 200, { triggered: true });
  });

  // ── POST /shutdown ─────────────────────────────────────────────────────────
  routes.set("POST /shutdown", (_, res) => {
    json(res, 200, { bye: true });
    setTimeout(() => process.exit(0), 200);
  });

  // ── HTTP сервер ────────────────────────────────────────────────────────────
  const server = http.createServer((req, res) => {
    res.setHeader("Content-Type", "application/json");
    res.setHeader("Access-Control-Allow-Origin", "*");

    const key = `${req.method} ${new URL(req.url ?? "/", "http://localhost").pathname}`;
    const handler = routes.get(key);

    if (!handler) {
      json(res, 404, { error: "Not found", route: key });
      return;
    }

    // Читаем тело запроса
    let raw = "";
    req.on("data", (chunk) => (raw += chunk));
    req.on("end", () => {
      let body: Record<string, string> = {};
      try { body = raw ? JSON.parse(raw) : {}; } catch { /* ignore */ }
      try { handler(body, res); } catch (err) {
        json(res, 500, { error: String(err) });
      }
    });
  });

  return server;
}

function json(res: http.ServerResponse, status: number, data: unknown): void {
  res.writeHead(status).end(JSON.stringify(data));
}

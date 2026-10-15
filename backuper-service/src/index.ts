import { parseArgs } from "util";
import fs from "fs";
import { ConfigStore } from "./config.js";
import { YandexDisk } from "./yandex.js";
import { UploadQueue } from "./queue.js";
import { FolderWatcher } from "./watcher.js";
import { createServer } from "./server.js";

// ── CLI аргументы ─────────────────────────────────────────────────────────────
const { values: args } = parseArgs({
  options: { port: { type: "string" } },
  strict: false,
});

// ── Инициализация ─────────────────────────────────────────────────────────────
const config  = new ConfigStore();
const port    = parseInt(String(args.port ?? config.get("port") ?? 47291), 10);

const disk    = new YandexDisk(config.get("yandexToken"));
const queue   = new UploadQueue(disk, config);
const watcher = new FolderWatcher(queue);
const server  = createServer(config, disk, watcher, queue);

// ── Старт сервера ─────────────────────────────────────────────────────────────
server.listen(port, "127.0.0.1", () => {
  console.log(`[backuper] Service started on http://127.0.0.1:${port}`);
  console.log(`[backuper] Config: ~/.audio-backuper.json`);

  // Восстанавливаем папку из конфига
  const savedFolder = config.get("watchFolder");
  console.log(savedFolder)
  if (savedFolder && fs.existsSync(savedFolder)) {
    console.log(`[backuper] Restoring watch folder: ${savedFolder}`);
    watcher.watch(savedFolder);
  } else if (savedFolder) {
    console.warn(`[backuper] Watch folder does not exist: ${savedFolder}`);
  } else {
    console.log(`[backuper] No watch folder set. Use POST /config to set one.`);
  }
});

// ── Graceful shutdown ─────────────────────────────────────────────────────────
function shutdown(signal: string): void {
  console.log(`\n[backuper] Received ${signal}, shutting down...`);
  watcher.stop();
  server.close(() => {
    console.log("[backuper] Server closed.");
    process.exit(0);
  });
  // Force exit после 3 секунд
  setTimeout(() => process.exit(0), 3000);
}

process.on("SIGTERM", () => shutdown("SIGTERM"));
process.on("SIGINT",  () => shutdown("SIGINT"));
process.on("uncaughtException", (err) => {
  console.error("[backuper] Uncaught exception:", err);
});
process.on("unhandledRejection", (reason) => {
  console.error("[backuper] Unhandled rejection:", reason);
});

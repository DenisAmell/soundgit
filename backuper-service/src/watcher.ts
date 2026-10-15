import chokidar, { FSWatcher } from "chokidar";
import { UploadQueue } from "./queue.js";

export class FolderWatcher {
  private watcher: FSWatcher | null = null;
  private currentFolder = "";

  constructor(private readonly queue: UploadQueue) {}

  watch(folder: string): void {
    if (folder === this.currentFolder) return;

    this.stop();
    this.currentFolder = folder;

    console.log(`[watcher] Watching: ${folder}`);

    this.watcher = chokidar.watch(folder, {
      ignored: /(^|[/\\])\../,  // скрытые файлы
      persistent: true,
      ignoreInitial: true,      // не копируем уже существующие файлы
      awaitWriteFinish: {
        stabilityThreshold: 2000, // ждём 2с после последней записи в файл
        pollInterval: 100,
      },
      depth: 10,
    });
    console.log('gg')
    this.watcher
      .on("add", (filePath) => {
        console.log('add gg')
        if (this.queue.isAudioFile(filePath)) {
          console.log(`[watcher] New file: ${filePath}`);
          this.queue.enqueue(filePath);
        }
      })
      .on("error", (err) => {
        console.error("[watcher] Error:", err);
      });
  }

  stop(): void {
    if (this.watcher) {
      this.watcher.close();
      this.watcher = null;
      this.currentFolder = "";
      console.log("[watcher] Stopped");
    }
  }

  getFolder(): string {
    return this.currentFolder;
  }
}

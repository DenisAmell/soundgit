import path from "path";
import { YandexDisk } from "./yandex.js";
import { ConfigStore } from "./config.js";

export interface QueueItem {
  localPath: string;
  remotePath: string;
  retries: number;
  addedAt: Date;
}

export interface Stats {
  pending: number;
  totalUploaded: number;
  lastSyncedFile: string;
  errors: Array<{ time: string; message: string }>;
}

const AUDIO_EXTENSIONS = new Set([
  ".wav", ".aif", ".aiff", ".mp3", ".flac",
  ".ogg", ".m4a", ".aac", ".caf", ".bwf", ".w64",
]);

const MAX_RETRIES = 5;
const RETRY_DELAY_MS = 5_000;

export class UploadQueue {
  private queue: QueueItem[] = [];
  private processing = false;
  private paused = false;

  private stats: Stats = {
    pending: 0,
    totalUploaded: 0,
    lastSyncedFile: "",
    errors: [],
  };

  constructor(
    private readonly disk: YandexDisk,
    private readonly config: ConfigStore
  ) {}

  isAudioFile(filePath: string): boolean {
    return AUDIO_EXTENSIONS.has(path.extname(filePath).toLowerCase());
  }

  enqueue(localPath: string): void {
    if (!this.isAudioFile(localPath)) return;

    const date = new Date().toISOString().slice(0, 10); // YYYY-MM-DD
    const basePath = this.config.get("yandexBasePath");
    const fileName = path.basename(localPath);
    const remotePath = `${basePath}/${date}/${fileName}`;

    // Не добавляем дубликаты
    const exists = this.queue.some((item) => item.localPath === localPath);
    if (exists) return;

    this.queue.push({ localPath, remotePath, retries: 0, addedAt: new Date() });
    this.stats.pending = this.queue.length;

    console.log(`[queue] Queued: ${fileName} (total: ${this.queue.length})`);

    if (!this.paused) this.processNext();
  }

  pause(): void {
    this.paused = true;
    console.log("[queue] Paused");
  }

  resume(): void {
    this.paused = false;
    console.log("[queue] Resumed");
    this.processNext();
  }

  flush(): void {
    if (!this.paused) this.processNext();
  }

  getStats(): Stats {
    return { ...this.stats };
  }

  private async processNext(): Promise<void> {
    if (this.processing || this.paused || this.queue.length === 0) return;

    this.processing = true;
    const item = this.queue[0];

    try {
      await this.uploadItem(item);

      // Успех — убираем из очереди
      this.queue.shift();
      this.stats.pending = this.queue.length;
      this.stats.totalUploaded++;
      this.stats.lastSyncedFile = path.basename(item.localPath);

      console.log(`[queue] Uploaded: ${item.localPath} → ${item.remotePath}`);
    } catch (err) {
      console.log(err)
      const message = err instanceof Error ? err.message : String(err);
      console.error(`[queue] Upload failed (attempt ${item.retries + 1}): ${message}`);

      item.retries++;

      if (item.retries >= MAX_RETRIES) {
        // Исчерпали попытки — убираем и логируем ошибку
        this.queue.shift();
        this.stats.pending = this.queue.length;
        this.addError(`${path.basename(item.localPath)}: ${message}`);
      } else {
        // Повтор через паузу
        this.processing = false;
        setTimeout(() => this.processNext(), RETRY_DELAY_MS * item.retries);
        return;
      }
    }

    this.processing = false;

    // Продолжаем очередь
    if (this.queue.length > 0) {
      setImmediate(() => this.processNext());
    }
  }

  private async uploadItem(item: QueueItem): Promise<void> {
    // Создаём директории на Яндекс.Диске
    // const dir = item.remotePath.substring(0, item.remotePath.lastIndexOf("/"));
    // await this.disk.mkdirp(dir);

    // Загружаем файл
    await this.disk.upload(item.localPath, item.remotePath);
  }

  private addError(message: string): void {
    this.stats.errors.push({ time: new Date().toISOString(), message });
    if (this.stats.errors.length > 50) {
      this.stats.errors = this.stats.errors.slice(-30);
    }
  }
}

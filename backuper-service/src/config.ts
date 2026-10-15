import fs from "fs";
import path from "path";
import os from "os";

export interface Config {
  watchFolder: string;
  yandexToken: string;
  yandexBasePath: string;
  port: number;
}

const DEFAULT_CONFIG: Config = {
  watchFolder: "/Users/denis/test",
  yandexToken: "",
  yandexBasePath: "/AudioBackups",
  port: 47291,
};

const CONFIG_PATH = path.join(os.homedir(), ".audio-backuper.json");

export class ConfigStore {
  private data: Config;

  constructor() {
    this.data = this.load();
  }

  get<K extends keyof Config>(key: K): Config[K] {
    return this.data[key];
  }

  set<K extends keyof Config>(key: K, value: Config[K]): void {
    this.data[key] = value;
    this.save();
  }

  getAll(): Config {
    return { ...this.data };
  }

  private load(): Config {
    try {
      if (fs.existsSync(CONFIG_PATH)) {
        const raw = fs.readFileSync(CONFIG_PATH, "utf8");
        return { ...DEFAULT_CONFIG, ...JSON.parse(raw) };
      }
    } catch {
      console.warn("[config] Failed to load config, using defaults");
    }
    return { ...DEFAULT_CONFIG };
  }

  private save(): void {
    try {
      fs.writeFileSync(CONFIG_PATH, JSON.stringify(this.data, null, 2), "utf8");
    } catch (err) {
      console.error("[config] Failed to save:", err);
    }
  }
}

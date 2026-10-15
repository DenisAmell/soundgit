import https from "https";
import fs from "fs";
import path from "path";

type HttpMethod = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';

interface RequestOptions {
  method?: HttpMethod;
  headers?: Record<string, string>;
  body?: any;
  timeoutMs?: number;
}



export class YandexDisk {
  private token: string;

  constructor(token: string) {
    this.token = token;
  }

  setToken(token: string): void {
    this.token = token;
  }

  async upload(localPath: string, remotePath: string): Promise<void> {
    if (!this.token) throw new Error("Yandex token is not set");

    const stream = fs.createReadStream(localPath);

    console.log('upload path', remotePath)
    const uploadUrl = (await this.request(`https://cloud-api.yandex.net/v1/disk/resources/upload?path=disk:${remotePath}`, {method: 'GET'})).href;
    console.log('upload url', uploadUrl)

    await this.request(uploadUrl, {method: 'PUT', body: stream});
  }

  async mkdir(remotePath: string): Promise<void> {
    await this.request(`https://cloud-api.yandex.net/v1/disk/resources?path=disk:${remotePath}`, { method: 'PUT' });
  }

  /**
   * Создаёт всю цепочку директорий.
   * mkdir("/AudioBackups/2025-01-15") создаст обе папки.
   */
  async mkdirp(remotePath: string): Promise<void> {
    const parts = remotePath.split("/").filter(Boolean);
    let current = "";
    for (const part of parts) {
      current += "/" + part;
      await this.mkdir(current);
    }
  }


  private async request(
    url: string,
    options: RequestOptions = {},
  ): Promise<any> {
  const {
    method = 'GET',
    headers = {},
    body,
    timeoutMs = 30000,
  } = options;

    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), timeoutMs);

    console.log(url)
    try {
      const response = await fetch(url, {
        method,
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `OAuth ${this.token}`,
          ...headers,
        },
        body: body ? JSON.stringify(body) : undefined,
        signal: controller.signal,
      });

      const contentType = response.headers.get('content-type');

      let data: unknown;
      if (contentType?.includes('application/json')) {
        data = await response.json();
      } else {
        data = await response.text();
      }

      if (!response.ok) {
        throw new Error(
          `HTTP ${response.status}: ${JSON.stringify(data)}`
        );
      }

      return data;
    } catch (error: any) {
      if (error.name === 'AbortError') {
        throw new Error('Request timeout');
      }
      throw error;
    } finally {
      clearTimeout(timeout);
    }
  }

  // private request(method: string, remotePath: string): Promise<void> {

  //   const res =  fetch('https://cloud-api.yandex.net/v1/disk/resources?path=disk/AudioBackuper', {
  //     method,
  //     headers: {
  //         "Authorization": `OAuth ${this.token}`,
  //         "Content-Length": "0",
  //     },
  //   }).then((response) => {
  //     console.log(response.status)
  //   }) 
  //   console.log('remotePath', remotePath)
  //   return new Promise((resolve, reject) => {
  //     const req = https.request('https://cloud-api.yandex.net/v1/disk/resources',
  //       {
  //         path: `?path=${encodeURI(remotePath)}`,
  //         method,
  //         headers: {
  //           Authorization: `OAuth ${this.token}`,
  //           "Content-Length": 0,
  //         },
  //       },
  //       (res) => {
  //         // 201 создана, 405 уже существует — оба ОК
  //         res.resume();
  //         if ([200, 201, 204, 405].includes(res.statusCode ?? 0)) resolve();
  //         else reject(new Error(`${method} ${remotePath}: HTTP ${res.statusCode}`));
  //       }
  //     );
  //     req.on("error", reject);
  //     req.end();
  //   });
  // }
}

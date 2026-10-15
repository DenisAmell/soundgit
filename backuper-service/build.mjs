import esbuild from "esbuild";
import { execSync } from "child_process";
import fs from "fs";

// Проверяем TypeScript
console.log("→ Type checking...");
try {
  execSync("npx tsc --noEmit", { stdio: "inherit" });
} catch {
  process.exit(1);
}

// Собираем один файл
console.log("→ Bundling...");
await esbuild.build({
  entryPoints: ["src/index.ts"],
  bundle: true,         // всё в один файл включая chokidar
  platform: "node",
  target: "node18",
  format: "cjs",        // CommonJS — совместим с любым Node.js
  outfile: "dist/index.js",
  minify: false,        // оставляем читаемым для отладки
  sourcemap: true,

  // Node.js built-ins не включаем в bundle
  external: [
    "fs", "path", "os", "http", "https",
    "util", "events", "stream", "child_process",
    "crypto", "net", "tls", "zlib", "buffer",
  ],
});

// Добавляем shebang чтобы можно было запускать напрямую
const dist = fs.readFileSync("dist/index.js", "utf8");
fs.writeFileSync("dist/index.js", "#!/usr/bin/env node\n" + dist);

const size = (fs.statSync("dist/index.js").size / 1024).toFixed(1);
console.log(`✓ dist/index.js  (${size} KB)`);

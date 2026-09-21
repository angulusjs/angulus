import { build } from "vite";
import { gzipSync } from "node:zlib";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const result = await build({
  configFile: false,
  root,
  logLevel: "error",
  build: {
    write: false,
    minify: true,
    target: "es2022",
    lib: { entry: resolve(root, "packages/core/src/index.ts"), formats: ["es"], fileName: "angulus-core" },
    sourcemap: false,
  },
});
const outputs = Array.isArray(result) ? result : [result];
const code = outputs.flatMap(output => output.output).filter(chunk => chunk.type === "chunk").map(chunk => chunk.code).join("\n");
const minified = Buffer.byteLength(code);
const gzip = gzipSync(code, { level: 9 }).length;
console.log(JSON.stringify({ package: "@angulus/core", scope: "all public core exports, no router/app/dev tooling", minifiedBytes: minified, gzipBytes: gzip, budgetBytes: 10240, withinBudget: gzip <= 10240 }, null, 2));

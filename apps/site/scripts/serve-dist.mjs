import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";

const dist = fileURLToPath(new URL("../dist", import.meta.url));
const types = {
  ".html": "text/html; charset=utf-8", ".js": "text/javascript", ".css": "text/css",
  ".svg": "image/svg+xml", ".json": "application/json", ".webp": "image/webp",
  ".webm": "video/webm", ".mp4": "video/mp4", ".woff2": "font/woff2",
};

export async function serveDist({ port = 0, base = process.env.SITE_BASE ?? "/" } = {}) {
  const prefix = base.endsWith("/") ? base : base + "/";
  const cache = new Map();
  const server = createServer(async (request, response) => {
    try {
      const pathname = decodeURIComponent(new URL(request.url, "http://localhost").pathname);
      if (!pathname.startsWith(prefix)) throw new Error("outside site base");
      const relative = pathname.slice(prefix.length) + (pathname.endsWith("/") ? "index.html" : "");
      const file = resolve(dist, relative);
      if (!file.startsWith(dist + sep)) throw new Error("outside site directory");
      if (!cache.has(file)) {
        const body = await readFile(file);
        const compressible = /\.(html|js|css|svg|json)$/.test(file);
        cache.set(file, { body, gzip: compressible ? gzipSync(body) : null });
      }
      const { body, gzip } = cache.get(file);
      const encoded = gzip && request.headers["accept-encoding"]?.includes("gzip");
      response.setHeader("Content-Type", types[extname(file)] ?? "application/octet-stream");
      if (encoded) response.setHeader("Content-Encoding", "gzip");
      const payload = encoded ? gzip : body;
      response.setHeader("Content-Length", payload.length);
      response.end(payload);
    } catch {
      response.statusCode = 404;
      response.end("Not found");
    }
  });
  await new Promise(resolve => server.listen(port, "127.0.0.1", resolve));
  const address = server.address();
  return { server, url: `http://127.0.0.1:${address.port}${prefix}` };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const { url } = await serveDist({ port: Number(process.argv[2] ?? 4382) });
  console.log(url);
}

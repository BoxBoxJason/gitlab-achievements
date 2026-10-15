/// <reference types="vitest/config" />
import { writeFileSync } from 'node:fs'
import { fileURLToPath, URL } from 'node:url'

import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'
import { defineConfig, type Plugin } from 'vite'

// The Go binary embeds dist with go:embed, which refuses to compile when
// the directory is empty. Vite empties dist before every build, so the
// placeholder that keeps a never-built checkout compiling is put back
// afterwards.
const keepPlaceholder: Plugin = {
  name: 'keep-dist-placeholder',
  apply: 'build',
  closeBundle() {
    writeFileSync(fileURLToPath(new URL('./dist/.gitkeep', import.meta.url)), '')
  },
}

export default defineConfig(async ({ mode }) => ({
  // The fixture API is only ever loaded for `npm run dev:mock`.
  plugins: [vue(), tailwindcss(), keepPlaceholder, mode === 'mock' && (await import('./mock/plugin')).mockApi()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  server: {
    // Against a running app (go run . or a port-forward), so the session
    // cookie and the API share the dev server's origin, as they share the
    // app's in production. `npm run dev:mock` answers from fixtures instead.
    proxy:
      mode === 'mock'
        ? undefined
        : Object.fromEntries(
            ['/api', '/oauth'].map((prefix) => [
              prefix,
              { target: process.env.API_TARGET ?? 'http://localhost:8080', changeOrigin: false },
            ]),
          ),
  },
  build: {
    target: 'es2022',
    // Every file under assets/ is named by content hash, which is what lets
    // the Go server cache that directory for good.
    assetsDir: 'assets',
  },
  test: {
    environment: 'happy-dom',
  },
}))

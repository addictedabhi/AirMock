import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import fs from 'node:fs'

// The pre-paint theme script in index.html must know every theme in
// src/lib/theme.js (and each one's light/dark mode), or a reload flashes the
// default theme until the app boots. It used to be a hand-copied list that
// drifted; this reads the real list at build time so it cannot. A theme
// definition that the pattern fails to find fails the build instead of
// silently shipping a stale list.
function themeModes() {
  const src = fs.readFileSync(new URL('./src/lib/theme.js', import.meta.url), 'utf8')
  const modes = {}
  for (const m of src.matchAll(/^ {4}id: '([a-z-]+)', label: '[^']+', swatch: '[^']+', mode: '(light|dark)'/gm)) {
    modes[m[1]] = m[2]
  }
  const declared = (src.match(/^ {4}id: '/gm) || []).length
  if (declared < 2 || Object.keys(modes).length !== declared) {
    throw new Error(`index.html theme list: found ${Object.keys(modes).length} of ${declared} themes in src/lib/theme.js; update the pattern in vite.config.js`)
  }
  return modes
}

const injectThemeModes = {
  name: 'inject-theme-modes',
  transformIndexHtml: {
    order: 'pre',
    handler(html) {
      return html.replace('__THEME_MODES__', JSON.stringify(themeModes()))
    },
  },
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [svelte(), injectThemeModes],
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
    },
  },
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: true,
  },
})

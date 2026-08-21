import { defineConfig, type ProxyOptions } from 'vite'
import react from '@vitejs/plugin-react'

// Dev-only proxy. The Backend only opens its explicit `--dev-origin` to Dashboard
// management routes, so Query/MCP/Feed requests must not look cross-origin: we
// rewrite Host to the Backend loopback and strip the browser's Vite Origin.
// A plain pass-through is rejected with `403 untrusted_request`.
const BACKEND = '127.0.0.1:8787'

const loopbackProxy: ProxyOptions = {
  target: `http://${BACKEND}`,
  changeOrigin: true,
  configure: (proxy) => {
    proxy.on('proxyReq', (proxyReq) => {
      proxyReq.setHeader('Host', BACKEND)
      proxyReq.removeHeader('origin')
      proxyReq.removeHeader('referer')
    })
  },
}

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5273,
    proxy: {
      '/v1': loopbackProxy,
      '/openapi.json': loopbackProxy,
      '/feeds': loopbackProxy,
      '/mcp': loopbackProxy,
    },
  },
  build: {
    // Emitted into the Go module that serves them via go:embed.
    outDir: '../../internal/transport/dashboardassets/dist',
    emptyOutDir: true,
  },
})

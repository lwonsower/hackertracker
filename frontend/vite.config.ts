import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],

  build: {
    // Build straight into the Go server's embed directory so `go build`
    // produces a single self-contained binary. emptyOutDir is required
    // because the target lives outside this project root.
    outDir: '../backend/web',
    emptyOutDir: true,
  },

  server: {
    port: 5173,
    // Proxy API traffic to the Go server so the browser sees one origin.
    // This is why we don't need CORS configured on the Go side.
    proxy: {
      '/api': { target: 'http://localhost:8080', changeOrigin: true },
      '/healthz': { target: 'http://localhost:8080', changeOrigin: true },
    },
  },
})

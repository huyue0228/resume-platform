import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    setupFiles: './src/test/setup.js',
  },
  build: {
    rolldownOptions: {
      output: {
        assetFileNames: (assetInfo) => {
          const sourceName = assetInfo.names?.[0] || assetInfo.name || ''
          if (sourceName.includes('pdf.worker')) {
            return 'assets/pdf.worker.mjs'
          }
          return 'assets/[name]-[hash][extname]'
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: process.env.VITE_API_PROXY_TARGET || 'http://localhost:8000',
        changeOrigin: true,
      },
    },
  },
})

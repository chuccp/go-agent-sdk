import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5174,
    proxy: {
      '/api': {
        target: 'http://localhost:19009',
        changeOrigin: true,
      },
      '/ws': {
        target: 'ws://localhost:19009',
        ws: true,
        changeOrigin: true,
      },
    },
  },
})

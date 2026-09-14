import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd())
  return {
    plugins: [react()],
    server: {
      port: 5175,
      proxy: {
        '/api': {
          target: env.VITE_API_BASE,
          changeOrigin: true,
        },
        '/ws': {
          target: env.VITE_WS_BASE,
          ws: true,
          changeOrigin: true,
        },
      },
    },
  }
})

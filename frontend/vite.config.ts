import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  server: {
    host: true, // 容器内监听 0.0.0.0，便于 docker 暴露
    port: 5173,
  },
})

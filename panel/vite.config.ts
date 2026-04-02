import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5174,
    proxy: {
      '/admin': {
        target: 'http://localhost:8989',
        changeOrigin: true,
      },
    },
  },
})

/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** 后端 HTTP 地址，vite dev server 代理 /api 的目标 */
  readonly VITE_API_BASE: string
  /** 后端 WebSocket 地址，前端直连 /ws */
  readonly VITE_WS_BASE: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}

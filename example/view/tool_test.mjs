// 工具流测试：验证 executeTools 新流程（per-tool Response + tool_execution + tool_result）
// 用法: node tool_test.mjs <session_id>
const SESSION_ID = Number(process.argv[2] || 38)
const BASE = 'http://localhost:19009'
const WS_URL = 'ws://localhost:19009/ws/chat'

// ── 加载历史，计算 start（与 e2e_test.mjs 一致）──
const res = await fetch(`${BASE}/api/chat/sessions/${SESSION_ID}/messages`)
const msgs = (await res.json()).data ?? []
const last = msgs[msgs.length - 1]
const start = last ? last.start + last.offset : 0
console.log(`[history] ${msgs.length} 条消息, 计算 start = ${start}`)

const ws = new WebSocket(WS_URL)
const events = []
let done = false

const timeout = setTimeout(() => {
  console.error('[FAIL] 90s 超时未收到 done')
  console.log('[events]', events.map(e => `${e.seq}:${e.block?.type}`).join(', '))
  process.exit(1)
}, 90000)

ws.onopen = () => {
  console.log('[ws] open, create session=' + SESSION_ID)
  ws.send(JSON.stringify({ type: 'create', session_id: SESSION_ID, start }))
}

ws.onmessage = (evt) => {
  const msg = JSON.parse(evt.data)
  // created 回执仍是顶层 {type: "created", ...}
  if (msg.type === 'created') {
    ws.send(JSON.stringify({ type: 'chat', message: '请使用 execute_command 工具执行命令 go version，然后根据工具返回的结果告诉我版本号' }))
    return
  }
  // 事件格式：{seq, block: {type, ...}}
  const block = msg.block
  if (!block) return
  const blockType = block.type
  events.push(msg)
  if (blockType === 'delta') process.stdout.write(block.content || '')
  if (blockType === 'tool_execution') {
    console.log(`\n[tool_execution] ${block.tool_name} | args=${block.args} | output=${(block.output || '').slice(0, 120)}`)
  }
  if (blockType === 'error') {
    console.error('[FAIL] error 事件:', block.message)
    process.exit(1)
  }
  if (blockType === 'done') {
    done = true
    clearTimeout(timeout)
    verify()
    ws.close()
  }
}

ws.onerror = (e) => {
  console.error('[FAIL] ws error', e.message ?? e)
  process.exit(1)
}

function verify() {
  let pass = true
  const fail = (m) => { pass = false; console.error('[FAIL]', m) }

  const types = events.map(e => e.block?.type)
  if (!types.includes('tool_execution')) fail('缺少 tool_execution 事件（工具执行流程未生效）')
  if (!types.includes('delta')) fail('缺少 delta（第二轮 LLM 流式输出未到达）')
  if (!done) fail('未收到 done')

  // tool_execution 必须出现在 done 之前，且其后仍有 delta（第二轮 LLM 调用）
  const teIdx = types.indexOf('tool_execution')
  if (teIdx >= 0) {
    const deltaAfterTool = types.slice(teIdx + 1).includes('delta')
    if (!deltaAfterTool) fail('tool_execution 之后没有第二轮 delta（tool_result 未回传 LLM）')
    const te = events[teIdx]
    const output = te.block.output || ''
    if (!output.includes('go version')) console.warn('[WARN] tool_execution 输出未包含命令输出:', output)
    else console.log('[ok] tool_execution 携带了命令输出')
  }

  console.log(`\n[events] 共 ${events.length} 个, 类型: ${[...new Set(types)].join(', ')}`)
  console.log(pass ? '\n✅ PASS' : '\n❌ FAIL')
  process.exit(pass ? 0 : 1)
}

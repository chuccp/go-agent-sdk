// stop 单轮语义冒烟测试（真实 LLM）：
// 1. 创建会话，发起长文生成
// 2. 生成中发送 stop
// 3. 验证：被停轮以 done 结束（无 error 事件）
// 4. 发送新消息，验证正常生成到 done（停止只对单轮生效）
// 5. 校验历史：被停轮的 assistant 内容被丢弃，只有新一轮的回复入历史
// 用法: node stop_test.mjs

const BASE = 'http://localhost:19009'
const WS_URL = 'ws://localhost:19009/ws/chat'

const createRes = await fetch(`${BASE}/api/chat/sessions`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ title: 'stop 冒烟测试' }),
})
const session = (await createRes.json()).data
console.log(`[session] id=${session.id}`)

const ws = new WebSocket(WS_URL)
let phase = 'generating' // generating → second
let gotError = false
let deltas = 0
let deltasAfterStop = 0
let stopped = false
let stoppedByTimer = false

const timeout = setTimeout(() => {
  console.error('[FAIL] 90s 超时')
  process.exit(1)
}, 90000)

ws.onopen = () => ws.send(JSON.stringify({ type: 'create', session_id: session.id, start: 0 }))

ws.onmessage = async (evt) => {
  const msg = JSON.parse(evt.data)
  // created 回执仍是顶层 {type: "created", ...}
  if (msg.type === 'created') {
    ws.send(JSON.stringify({ type: 'chat', message: '请直接写一篇 800 字左右的、关于大海的童话故事正文，不要提问、不要使用任何工具，慢慢写' }))
    // 3 秒后停止（此时应仍在流式生成中）
    setTimeout(() => {
      if (phase === 'generating' && !stopped) {
        stopped = true
        stoppedByTimer = true
        console.log(`[stop] 已收到 ${deltas} 个 delta，发送 stop`)
        ws.send(JSON.stringify({ type: 'stop' }))
      }
    }, 3000)
    return
  }
  // 事件格式：{seq, block: {type, ...}}
  const block = msg.block
  if (!block) return
  const blockType = block.type

  if (blockType === 'error') {
    gotError = true
    console.error('[FAIL] 收到 error 事件:', block.message)
  }
  if (blockType === 'delta') {
    deltas++
    if (stopped) deltasAfterStop++
  }
  if (blockType === 'done') {
    if (phase === 'generating') {
      if (!stoppedByTimer) {
        // 生成在 stop 前就已结束，本次未验证到停止，直接判失败（重试）
        console.error('[FAIL] 生成在 stop 之前已完成，未验证到停止语义')
        process.exit(1)
      }
      console.log(`[ok] 被停轮以 done 结束（stop 后残留 delta: ${deltasAfterStop}）`)
      phase = 'second'
      deltas = 0
      ws.send(JSON.stringify({ type: 'chat', message: '用一句话回答：1+1 等于几？' }))
      return
    }
    if (phase === 'second') {
      clearTimeout(timeout)
      console.log(`[ok] stop 后的新一轮正常完成（${deltas} 个 delta）`)
      ws.close()
      await verifyHistory()
      process.exit(0)
    }
  }
}

ws.onerror = (e) => {
  console.error('[FAIL] ws error', e.message ?? e)
  process.exit(1)
}

// 校验历史：被停轮的 assistant 内容被丢弃——历史中 assistant 消息应只有 1 条（新一轮的回复）
async function verifyHistory() {
  if (gotError) {
    console.error('[FAIL] 过程中收到 error 事件（被停轮不应报错）')
    process.exit(1)
  }
  const res = await fetch(`${BASE}/api/chat/sessions/${session.id}/messages`)
  const msgs = (await res.json()).data ?? []
  const dumped = JSON.stringify(msgs)
  const userCount = (dumped.match(/"role"\s*:\s*"user"/g) ?? []).length
  const assistantCount = (dumped.match(/"role"\s*:\s*"assistant"/g) ?? []).length
  console.log(`[history] 共 ${msgs.length} 条消息: user=${userCount}, assistant=${assistantCount}`)
  if (assistantCount !== 1) {
    console.error(`[FAIL] 被停轮内容应被丢弃，期望 1 条 assistant 消息，实际 ${assistantCount}`)
    process.exit(1)
  }
  if (!dumped.includes('2')) {
    console.error('[FAIL] 历史中未找到新一轮的回复内容')
    process.exit(1)
  }
  console.log('✅ PASS')
}

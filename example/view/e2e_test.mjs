// 端到端协议测试：模拟前端完整流程
// 1. REST 加载历史 → 计算 start
// 2. WS create(start) → 等待 created 回执
// 3. 发送 chat → 收集事件流，校验 seq 连续性与 done 归属
// 用法: node e2e_test.mjs <session_id>

const SESSION_ID = Number(process.argv[2] || 1)
const BASE = 'http://localhost:19009'
const WS_URL = 'ws://localhost:19009/ws/chat'

// ── 1. 加载历史，计算 start ──
const res = await fetch(`${BASE}/api/chat/sessions/${SESSION_ID}/messages`)
const json = await res.json()
const msgs = json.data ?? []
const last = msgs[msgs.length - 1]
const start = last ? last.start + last.offset : 0
console.log(`[history] ${msgs.length} 条消息, 计算 start = ${start}`)

// ── 2. WS 连接：create → created 回执 → chat ──
const ws = new WebSocket(WS_URL)
const events = []
let gotCreated = false
const timeout = setTimeout(() => {
  console.error('[FAIL] 60s 超时未收到 done')
  console.log('[events]', JSON.stringify(events.map(e => `${e.seq}:${e.block?.type}`), null, 0))
  process.exit(1)
}, 60000)

ws.onopen = () => {
  console.log('[ws] open, 发送 create start=' + start)
  ws.send(JSON.stringify({ type: 'create', session_id: SESSION_ID, start }))
}

ws.onmessage = (evt) => {
  const msg = JSON.parse(evt.data)
  // created 回执仍是顶层 {type: "created", ...}
  if (msg.type === 'created') {
    gotCreated = true
    console.log(`[ws] 收到 created 回执 (session_id=${msg.session_id})，发送 chat`)
    ws.send(JSON.stringify({ type: 'chat', message: '用一句话介绍你自己' }))
    return
  }
  // 事件格式：{seq, block: {type, ...}}
  const block = msg.block
  if (!block) return
  const blockType = block.type
  events.push(msg)
  if (blockType === 'delta') process.stdout.write(block.content || '')
  if (blockType === 'done') {
    clearTimeout(timeout)
    console.log('\n[ws] 收到 done, seq=' + msg.seq)
    verify()
    ws.close()
  }
}

ws.onerror = (e) => {
  console.error('[FAIL] ws error', e.message ?? e)
  process.exit(1)
}

// ── 3. 校验 ──
async function verify() {
  let pass = true
  const fail = (m) => { pass = false; console.error('[FAIL]', m) }

  if (!gotCreated) fail('未收到 created 回执')

  // seq 必须从 start 开始且单调连续
  const seqs = events.map(e => e.seq)
  if (seqs[0] !== start) fail(`首个事件 seq=${seqs[0]}, 期望 start=${start}`)
  for (let i = 1; i < seqs.length; i++) {
    if (seqs[i] !== seqs[i - 1] + 1) fail(`seq 不连续: ${seqs[i - 1]} -> ${seqs[i]}`)
  }

  // 事件序列结构（block.type）
  const types = events.map(e => e.block?.type)
  // User block (用户消息入队/消费)
  if (!types.includes('User')) fail('缺少 User block')
  // 有 delta（流式文本增量）
  if (!types.some(t => t === 'delta')) fail('缺少 delta')
  // 最后一个事件是 done
  if (types[types.length - 1] !== 'done') fail('最后一个事件不是 done')

  // 去重后的类型列表
  const uniqueTypes = [...new Set(types)]
  console.log(`[events] 共 ${events.length} 个: ${uniqueTypes.join(', ')}`)

  // done 归属校验：最后一条 assistant 历史消息的 start+offset 应 = done.seq + 1
  const res2 = await fetch(`${BASE}/api/chat/sessions/${SESSION_ID}/messages`)
  const msgs2 = (await res2.json()).data ?? []
  const lastAssistant = [...msgs2].reverse().find(m => m.role === 'assistant')
  if (!lastAssistant) fail('历史中没有 assistant 消息')
  else {
    const end = lastAssistant.start + lastAssistant.offset
    const doneSeq = events[events.length - 1].seq
    if (end !== doneSeq + 1) fail(`done 未被历史 offset 覆盖: last.start+offset=${end}, done.seq+1=${doneSeq + 1}`)
    else console.log(`[history] done 已归属 assistant 消息 offset (start=${lastAssistant.start}, offset=${lastAssistant.offset})`)
  }

  console.log(pass ? '\n✅ PASS' : '\n❌ FAIL')
  process.exit(pass ? 0 : 1)
}

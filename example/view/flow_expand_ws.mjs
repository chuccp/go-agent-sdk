// 迭代 flow 冒烟测试（真实 LLM）：expand001 分段 → 逐段扩写 → 缝合
// 用法: node flow_expand_ws.mjs

const BASE = 'http://localhost:19009'
const WS_URL = 'ws://localhost:19009/ws/chat'

const createRes = await fetch(`${BASE}/api/chat/sessions`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ title: '迭代 flow 冒烟' }),
})
const session = (await createRes.json()).data
console.log(`[session] id=${session.id}`)

const ws = new WebSocket(WS_URL)
const toolExecs = []
const flowProgress = []

const timeout = setTimeout(() => {
  console.error('[FAIL] 120s 超时')
  console.log('[tools]', toolExecs.map(t => t.name))
  console.log('[progress]', JSON.stringify(flowProgress.map(p => `${p.stepId}:${p.phase}`)))
  process.exit(1)
}, 120000)

ws.onopen = () => ws.send(JSON.stringify({ type: 'create', session_id: session.id, start: 0 }))

ws.onmessage = (evt) => {
  const msg = JSON.parse(evt.data)
  // created 回执仍是顶层 {type: "created", ...}
  if (msg.type === 'created') {
    ws.send(JSON.stringify({ type: 'chat', message: '把「小狐狸想看月亮」这个梗概扩写成一个小故事，每一段短一点' }))
    return
  }
  // 事件格式：{seq, block: {type, ...}}
  const block = msg.block
  if (!block) return
  const blockType = block.type

  if (blockType === 'error') {
    console.error('[FAIL] error:', block.message)
    process.exit(1)
  }
  if (blockType === 'tool_execution') toolExecs.push({ name: block.tool_name, output: block.output })
  // flow_progress 是 TextBlock with text_type="flow_progress"
  if (blockType === 'text' && block.text_type === 'flow_progress') flowProgress.push(JSON.parse(block.text))
  if (blockType === 'delta') process.stdout.write(block.content || '')
  if (blockType === 'done') {
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
  console.log('\n\n===== 验证结果 =====')
  console.log('[tools 调用序列]', toolExecs.map(t => t.name).join(' → '))
  console.log('[progress 序列]', flowProgress.map(p => `${p.stepId}:${p.phase}`).join(' → '))

  let pass = true
  const check = (cond, label) => {
    console.log(`${cond ? '✅' : '❌'} ${label}`)
    if (!cond) pass = false
  }
  const activate = toolExecs.find(t => t.name === 'activate_flow')
  check(!!activate && activate.output.includes('expand001') || !!activate, '激活了 flow')
  check(toolExecs.filter(t => t.name === 'exec_node').length >= 3, '至少 3 次 exec_node（split/expand/merge）')
  check(flowProgress.some(p => p.stepId === 'split' && p.phase === 'done'), 'split 完成')
  check(flowProgress.filter(p => p.phase === 'item').length >= 2, '迭代逐项事件（≥2 项）')
  check(flowProgress.some(p => p.stepId === 'merge' && p.phase === 'done'), 'merge 完成（全文产出）')
  const mergeDone = flowProgress.find(p => p.stepId === 'merge' && p.phase === 'done')
  check(!!mergeDone && mergeDone.output.length > 50, '缝合全文有实际内容')
  process.exit(pass ? 0 : 1)
}

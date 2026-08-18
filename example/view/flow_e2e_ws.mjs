// flow 全链路冒烟测试（真实 LLM）：
// 验证 story003 剧本：activate_flow（零提问直通）→ exec_node（零上下文执行）
// → flow_progress 事件 → 交付收尾
// 用法: node flow_e2e_ws.mjs

const BASE = 'http://localhost:19009'
const WS_URL = 'ws://localhost:19009/ws/chat'

// 1. 创建新会话
const createRes = await fetch(`${BASE}/api/chat/sessions`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ title: 'flow 冒烟测试' }),
})
const session = (await createRes.json()).data
console.log(`[session] id=${session.id}`)

// 2. WS 连接并驱动对话
const ws = new WebSocket(WS_URL)
const toolExecs = []
const flowProgress = []
let sawDone = false

const timeout = setTimeout(() => {
  console.error('[FAIL] 90s 超时')
  console.log('[tools]', toolExecs.map(t => t.name))
  process.exit(1)
}, 90000)

ws.onopen = () => {
  ws.send(JSON.stringify({ type: 'create', session_id: session.id, start: 0 }))
}

ws.onmessage = (evt) => {
  const msg = JSON.parse(evt.data)
  // created 回执仍是顶层 {type: "created", ...}
  if (msg.type === 'created') {
    // 原话已含主题+受众 → 期望零提问直通
    ws.send(JSON.stringify({ type: 'chat', message: '给我 5 岁孩子写一个关于太空的故事，短一点，100字以内' }))
    return
  }
  // 事件格式：{seq, block: {type, ...}}
  const block = msg.block
  if (!block) return
  const blockType = block.type

  if (blockType === 'error') {
    console.error('[FAIL] error 事件:', block.text)
    process.exit(1)
  }
  if (blockType === 'tool_execution') toolExecs.push({ name: block.tool_name, output: block.output })
  // flow_progress 是 TextBlock with text_type="flow_progress"
  if (blockType === 'text' && block.text_type === 'flow_progress') flowProgress.push(JSON.parse(block.text))
  if (blockType === 'delta') process.stdout.write(block.content || '')
  if (blockType === 'done') {
    sawDone = true
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
  const names = toolExecs.map(t => t.name)
  console.log('[tools 调用序列]', names.join(' → '))
  console.log('[flow_progress]', JSON.stringify(flowProgress))

  let pass = true
  const check = (cond, label) => {
    console.log(`${cond ? '✅' : '❌'} ${label}`)
    if (!cond) pass = false
  }
  check(sawDone, '收到 done')
  check(names.includes('activate_flow'), 'LLM 自主触发 activate_flow')
  check(names.includes('exec_node'), '执行了 exec_node')
  check(flowProgress.some(p => p.phase === 'start' && p.stepId === 'story'), 'story 节点 start 事件')
  check(flowProgress.some(p => p.phase === 'done' && p.stepId === 'story'), 'story 节点 done 事件（含产出）')
  const activate = toolExecs.find(t => t.name === 'activate_flow')
  check(activate && activate.output.includes('【执行守则】'), 'activate 返回完整卡片')
  const execResult = toolExecs.find(t => t.name === 'exec_node')
  check(execResult && execResult.output.includes('【进度】'), 'exec_node 结果带进度脚标')
  process.exit(pass ? 0 : 1)
}

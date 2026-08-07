// 中文编码全链路验证：让 LLM 执行 systeminfo，检查工具输出与最终回答无乱码
const SESSION_ID = Number(process.argv[2] || 40)
const WS_URL = 'ws://localhost:19009/ws/chat'

const ws = new WebSocket(WS_URL)
const toolCalls = []
let answer = ''

const timeout = setTimeout(() => {
  console.error('[FAIL] 120s 超时')
  process.exit(1)
}, 120000)

ws.onopen = () => ws.send(JSON.stringify({ type: 'create', session_id: SESSION_ID, start: 0 }))

ws.onmessage = (evt) => {
  const msg = JSON.parse(evt.data)
  if (msg.type === 'created') {
    ws.send(JSON.stringify({ type: 'chat', message: '请使用 execute_command 工具执行 systeminfo 命令，然后告诉我 OS Name（操作系统名称）是什么' }))
    return
  }
  if (msg.type === 'tool_execution') {
    toolCalls.push({ args: msg.args, content: msg.content || '' })
    console.log(`[tool_execution #${toolCalls.length}] 命令: ${msg.args}`)
    console.log(`  输出前 150 字符: ${(msg.content || '').slice(0, 150)}`)
  }
  if (msg.type === 'chunk') answer += msg.content
  if (msg.type === 'done') {
    clearTimeout(timeout)
    console.log('\n[最终回答]', answer.slice(0, 200))
    // 乱码特征检测：替换字符 U+FFFD 或连续的 Latin-1 补充区字符（GBK 被按单字节误解的典型特征）
    let pass = true
    for (const [i, call] of toolCalls.entries()) {
      const garbled = /[\uFFFD]|[\u00C0-\u00FF]{4,}|[\u0370-\u03FF\u0400-\u04FF]{3,}/.test(call.content)
      console.log(`调用 #${i + 1} (${call.args}): ${garbled ? '❌ 疑似乱码' : '✅ 无乱码特征'}`)
      if (garbled) pass = false
    }
    console.log(pass ? '\n✅ PASS' : '\n❌ FAIL')
    process.exit(pass ? 0 : 1)
  }
}

ws.onerror = (e) => {
  console.error('[FAIL] ws error', e.message ?? e)
  process.exit(1)
}

# Flow-in-Chat 设计文档 v3：剧本编排 + 零上下文执行核

> 本版取代 v1（阻塞式引擎）。主轴不变——**按 flow 走 + 自然**——
> 但实现路径重构：编排交给主 loop（skill 式剧本指导），
> 具体 node 的执行保持零上下文隔离。

## 1. 设计主轴

- **按 flow 走**：flow 是结构化剧本，执行节点零上下文、确定性产出；
  步骤间有依赖校验，完成有验收，保证不漏步、不绕过关卡；
- **自然**：主 loop 从头到尾不失去控制权——确认是普通对话、跑题是
  普通对话、回来无需机制。没有消息拦截、没有退出/挂起装置。

## 2. 总体架构

```
用户: "帮我写个太空故事"
  │
主 LLM 调 activate_flow("story003", {topic:"太空"})
  │   ① FlowState 初始化（input 存档）
  │   ② 此后每轮 buildRequest 把 flow 卡片+进度注入 System（不进历史）
  ▼
主 LLM 按卡片剧本走（主线始终掌权）：
  ├─ 对话步骤: 用 ask_user_question 确认受众（普通对话，天然自然）
  ├─ 执行步骤: 调 exec_node("story")
  │      ① 工具自动从 FlowState 取上游产出（topic/audience）
  │      ② 渲染模板 → 一次性 LLM 调用（零会话历史，硬边界）
  │      ③ 输出写回 FlowState + 推 flow_progress 事件 + 摘要回给主 LLM
  └─ 交付步骤: 主 LLM 把故事呈现给用户、询问是否润色
  ▼
finish_flow(complete) → 清理状态。
中途跑题？正常聊——卡片还在 System、FlowState 还在，聊完接着走。
```

核心分工：**主 LLM 管编排与对话（自然），exec_node 管干活（隔离）**。

## 3. Flow 定义：剧本 + 节点

步骤分两类：

```go
flow := flow.NewBuilder("story003", "故事生成").
    Description("根据主题生成定制故事，先确认受众再创作").
    InputSchema(map[string]any{ /* topic: string */ }).
    // 对话步骤：指导主 LLM 做什么（它用普通对话/现有工具完成）
    Talk("confirm", "确认受众",
        "用 ask_user_question 询问用户故事面向儿童还是成人，把答案记住").
    // 执行步骤：绑定节点，由 exec_node 零上下文执行
    Exec("story", node.NewChatNodeBuilder("story").
        SystemTemplate("你是一位故事创作者").
        UserTemplate("主题：{{topic}}，受众：{{audience}}，写一个 800 字的故事").
        Build()).
    // 交付步骤
    Talk("deliver", "交付", "把故事呈现给用户，询问是否需要润色").
    Build()
```

- `Talk` 步骤 = 剧本指令，主 LLM 在对话中自行完成（确认、澄清、交付）。
  脚本采用"目标 + 完成条件 + 分支指引"格式（见 8.2），核心是
  让 LLM 能自行判断"信息已知则免问"；
- `Exec` 步骤 = 绑定节点（复用现有 `workflow/node` 的 ChatNode 及其
  Builder），只能经 `exec_node` 工具执行；可指定产出去向
  （直达屏幕 / 进上下文，见 5.2）；
- 步骤有声明顺序，构成依赖链（见 6.2）。

`Workflow`/`Builder`（`workflow/exec/workflow.go`）相应扩展
Description/InputSchema/steps；卡片渲染 `Render() string` 生成 markdown 剧本。

## 4. 工具集（tools/flow_tools.go）

| 工具 | 职责 |
|---|---|
| `activate_flow(flow_id, input)` | 初始化 FlowState；tool_result 返回完整卡片（剧本+进度+建议下一步） |
| `exec_node(flow_id, step_id)` | 零上下文执行节点（核心，见第 5 节）；result 附进度脚标 |
| `flow_status(flow_id)` | 查全量状态（同 task_list，防注意力衰减） |
| `finish_flow(flow_id, action, summary)` | action=complete 验收并清理；action=abandon 放弃并清理 |

四个工具注册进 Manager，共享工具层 FlowStore（结构对齐 TodoStore）；
`activate_flow` 的 Definition 动态拼接所有已注册 flow 的
id/name/description（既有动态 Definition 机制）。

> 实测经验：仅靠工具 definition 的 description 不足以可靠触发。
> 解法已内置为 SDK 机制：`agent.PromptProvider` 可选接口——工具自带
> `UsagePrompt()` 引导词，`buildRequest` 每轮自动拼进 System（用了哪个
> 工具就拼哪个的引导词）。`ActivateFlowTool` 已实现，宿主应用零硬编码。

## 5. exec_node：零上下文执行核（硬边界）

```
1. 取 FlowState：不存在/flow 未激活 → 拒绝
2. 依赖校验：step_id 之前的所有步骤必须已完成，否则拒绝并列出缺项
3. 组装输入：FlowState.input + 各已完成步骤的输出（按 step_id 注册）
4. 渲染节点模板（{{var}} 从组装后的数据取值）
5. 一次性 LLM 调用：不带会话历史（硬边界，不提供开关）；
   节点可用自己的 model/options；请求 context 用会话 runCtx 快照
6. 输出处理（按产出去向，见 5.2）：
   ① 写入 FlowState.outputs[step_id]
   ② EmitEvent(flow_progress，含产出) → 前端可见
   ③ event 模式：全文随事件直达前端渲染，主 LLM 只拿到摘要；
      context 模式：全文返回主 LLM
7. 失败：返回人类可读的错误描述（工具层包装，不暴露原始堆栈），
   主 LLM 自然决策——重试 / 问用户 / 放弃
8. 重跑：已完成步骤可再次 exec_node，输出覆盖旧值（支持中途改参数
   与交付后润色，见 8.5）；依赖校验按最新完成状态计算
```

### 5.2 产出去向（自然度与上下文的关键平衡）

| 模式 | 行为 | 适用 |
|---|---|---|
| `event`（生成物默认） | 全文随 flow_progress 事件直达前端，渲染成作品卡片；主 LLM 只拿摘要 | 故事/文案等直接交付物：用户秒看到作品，LLM 只管前后对话，零上下文成本 |
| `context` | 全文返回主 LLM | LLM 需要内容继续加工的产出（如需要总结/改写的中间物） |

没有这个区分，交付环节会两头不讨好：全文进上下文违背少上下文，
只给摘要又无法呈现作品。节点 Builder 扩展 `.Deliver(node.DeliverEvent)`。

### 5.3 迭代执行（批处理模式）

迭代属于**执行核**，不属于编排层：主 LLM 不循环，只对"迭代型执行
步骤"调用一次 exec_node，工具内部完成逐项执行与聚合：

```go
Exec("translate", node.NewChatNodeBuilder("translate").
    UserTemplate("翻译成英文（{{index}}/{{total}}）：{{item}}，保持 {{style}} 风格").
    Build()).
Iterate("paragraphs")   // 迭代源：FlowState 中的数组键
```

执行语义：

1. **取数组（迭代源寻址）**：`Iterate(key)` 先查 input[key]，再查
   outputs[key]（上游步骤产出：数组直接用，或路径引用如
   `Iterate("split.segments")`）；缺失/非数组 → 拒绝；`maxItems` 上限
   （默认 20），超限拒绝并提示，防 LLM 喂巨批；
2. **逐项执行**：模板变量 = FlowState 数据（共享）+ item/index/total
   （项变量，扁平合并——**无需作用域链**：节点是零上下文一次性调用，
   不存在跨层引用）；item 为对象时支持 `{{item.title}}` 嵌套路径；
3. **串行执行**（并发留作演进），每项零上下文；逐项发
   flow_progress（phase=item, index/total）；
4. **{{prev}} 滑动上下文**：串行迭代中第 i 项（i>0）模板自动可用
   `{{prev}}` = 上一项输出，`.PrevWindow(n)` 只取尾部 n 字。用于
   扩写/续写等需要前后文衔接的场景——prev 是 FlowState 的迭代数据
   而非会话历史，零上下文硬边界不破；整体一致性靠共享变量（如
   大纲），局部衔接靠 prev；
5. **聚合**：outputs[step_id] = 结果数组；产出去向（5.2）作用于聚合产出，
   返回给主 LLM 的是摘要（如 "5/5 段完成"）；下游步骤用
   `{{step_id}}` 直接消费聚合数组（如缝合/归约，就是普通 Exec）；
6. **失败与 index 级跳过**：某项失败 → 步骤以失败返回但保留已完成部分；
   主 LLM 重试 exec_node → 已完成项自动跳过（outputs[step_id][i] 已存在），
   只补跑失败/缺失项——重跑语义（8.4）的自然延伸，天然断点续跑；
7. **上游重跑失效**：某步骤重跑且输出变化时，自动清空其下游步骤的
   输出（否则 index 级跳过会拿基于旧上游的脏数据当"已完成"）。

**典型场景：故事扩写（分段 → 逐段扩写 → 缝合）**：

```go
Exec("split", splitNode)                     // 输出 segments 数组
Talk("confirm", "确认分段", `...调整则补录重跑 split`).DoneWhen("segments_confirmed")
Exec("expand", expandNode).
    Iterate("split").PrevWindow(500)          // {{item}}+{{prev}}+共享大纲
Exec("merge", mergeNode).Deliver(node.DeliverEvent)  // {{expand}} 缝合，普通 Exec
```

边界：超长故事的 merge 可能塞不下全文 → 分层归约（段内合并→组
合并）见演进（13.1）。

与 v1 对比：展开/聚合/预填跳过的思想保留，但作用域链与嵌套寻址
（`iter_0`）不再需要——因为循环内没有对话（交互归编排层）、节点无
跨层引用。边界：一期仅单节点迭代；每项跑多节点小流程见演进（13.1）。

执行核心直接复用 ChatNode 的模板+选项逻辑（顺带修复
`ChatNodeBuilder.UserTemplate` 误写 systemTemplate 的 bug，
chat_node.go L48-50）。

**与 v1 的关系**：这就是 v1 里节点的执行逻辑，只是不再由阻塞式引擎
串联，而是被主 loop 按剧本逐步调用。零上下文、可换模型、产出隔离——
引擎式的隔离价值全部保留。

## 6. FlowState 与进度

### 6.1 FlowState（per session，内存）

```go
type FlowState struct {
    FlowID   string
    Input    map[string]any            // activate 时的入参
    Outputs  map[string]any            // step_id → 节点输出
    Status   map[string]StepStatus     // pending / done / skipped
    Note     string                    // 主 LLM 记录的对话结论（如受众=儿童）
}
```

生命周期：activate 创建；complete/abandon 清理；新 activate 覆盖；
会话移除时清理。**不做持久化**（重启丢失，与 v1 一致）。
存放于工具层 FlowStore（对齐 TodoStore 模式，见第 7 节），
不侵入 SessionContext。

Talk 步骤的结论（如"受众=儿童"）通过 **activate_flow 幂等更新**流入
FlowState——这是"对话"与"执行"之间唯一的数据接缝：

- `activate_flow` 幂等：flow 已激活时再次调用不重建状态，而是**合并
  input**（新键追加、同键覆盖），并返回当前进度；
- 守则强制：主 LLM 在对话中获得任何新信息（确认结果、附加要求、
  中途修改），必须在 exec 前通过 activate_flow 补录；
- 一次调用同时完成"登记 + 继续"，无需额外工具。

最自然的路径——**零提问直通**：用户原话已含全部信息时，激活即
可执行，一个问题都不用问：

```
用户: "给我 5 岁孩子写个太空故事"
主 LLM: activate_flow("story003", {topic:"太空", audience:"儿童"})
        ← 原话已含主题+受众，跳过提问，直接：
        exec_node("story")
```

### 6.2 确定性骨架：依赖校验 + 完成验收

"按 flow 走"不靠拦截机制，靠校验：

- **exec_node 顺序校验**：前置步骤未完成 → 拒绝执行并返回缺项清单，
  主 LLM 据此纠正（漏步被硬性兜底）；
- **complete 验收**：finish_flow(complete) 校验所有必需步骤 done，
  否则拒绝并列缺项；
- **进度注入**：每轮 System 卡片带当前进度（"步骤 2/4 未完成"），
  主 LLM 始终知道走到哪了。

软化的部分也要承认：Talk 步骤的质量依赖主 LLM 跟随剧本（可能啰嗦/
绕路），但 Exec 步骤的隔离与顺序校验是硬的。

## 7. 剧本与进度的传递：todo 式工具状态（不注入 System）

参考 SDK 现有 `tools/todo.go`（TodoStore/状态机/blocked_by 依赖校验）
的做法：flow 的剧本与进度是**工具层状态**，经 tool_result 进入对话，
不碰 System 提示词，buildRequest 零改动：

1. **剧本一次入场**：`activate_flow` 的 tool_result 返回完整卡片
   （执行守则 + 步骤剧本 + 初始进度 + 建议下一步），作为 user(tool_result)
   消息入历史——与 task_create 返回任务详情同一模式；
2. **进度随每次调用刷新**：所有 flow 工具（activate 补录 / exec_node /
   finish）的 tool_result 末尾自动附一行紧凑进度脚标：
   `【进度】confirm✓ story✓ → 下一步: 交付`，防止长对话中注意力衰减；
3. **主动查询**：提供 `flow_status` 工具（同 task_list），LLM 不确定时
   随时查全量状态；
4. **依赖即 blocked_by**：步骤状态机对齐 todo（pending → in_progress →
   completed），exec_node 执行前校验等价于 task_update 的 checkDeps。

卡片四段式内容（activate 返回）：

1. **【执行守则】**：全局自然度规则，所有 flow 共用（见 8.1）；
2. **步骤剧本**：各 Talk/Exec 步骤的指引与完成条件；
3. **当前进度**：已完成/未完成步骤；
4. **建议下一步**：工具层计算的第一个依赖满足的未完成步骤，
   把 LLM 的推理负担降为跟读。

与 System 注入方案的对比：

| 维度 | todo 式（采用） | System 注入 |
|---|---|---|
| 内核改动 | 零（buildRequest 不动） | 需改 buildRequest + SessionContext |
| 历史残留 | 卡片留在历史（一次性，几百 token） | 零残留 | 
| 上下文成本 | 一次性 + 每次一行脚标 | 每轮全量卡片 |
| 前端可视化 | 复用 todo 式 UI 渲染步骤清单 | 需额外事件 |
| 架构一致性 | 与 TodoStore 同构，实现可直接借鉴 | 独有机制 |

代价：卡片驻留历史（一次性成本，flow 越长越划算）；注意力衰减用
进度脚标 + flow_status 缓解。

### 7.1 编排机制：主 LLM 到底怎么驱动 flow

编排不是新引擎，复用现有 doLoop 的 ReAct 循环（LLM → tool_use →
tool_result → LLM），靠三层分工支撑——LLM 只做选择题，不管状态：

| 层 | 内容 | 归属与属性 |
|---|---|---|
| 决策 | 下一步做什么：问/登记/执行/交付/答跑题 | LLM 判断（软，对话式） |
| 状态 | input、各步骤产出、进度 | FlowStore 外部化（todo 式），LLM 不携带不记忆 |
| 边界 | 依赖校验（exec_node，同 checkDeps）+ 完成验收（finish） | 工具层硬执行，防偏离 |

关键：**进度不靠 LLM 汇报，全部由工具层计算**：

- Exec 步骤：执行成功自动 done；
- Talk 步骤：声明式判定 `DoneWhen(input 键)`——对应键被登记进
  FlowState（经 activate_flow）即自动完成，无需 LLM 上报；
- "建议下一步"与 finish 验收均由此推导，随每次 tool_result 脚标送达。

典型轨迹（story003）：

```
轮1  用户: "给我 5 岁孩子写个太空故事"
     LLM: activate_flow("story003", {topic:"太空", audience:"儿童"})
     tool_result: 完整卡片（守则+剧本）+【进度】→ confirm；
                  原话已含受众 → 工具层将 confirm 自动 done ✓
轮2  LLM: exec_node("story")（按卡片建议）
     tool_result: 完成摘要 + 作品直达屏幕 +【进度】confirm✓ story✓ → 交付
轮3  LLM: "故事生成好了，看看喜不喜欢"（交付即对话）
轮4  用户: "不错"
     LLM: finish_flow(complete) → 验收（全步骤 done）→ 清理
```

LLM 可以犯懒但出不了格：跳步 → exec_node 拒绝并告知缺项；漏步 →
finish 拒绝并列缺项；数据从不经过 LLM 搬运（exec_node 自动取上游）。

## 8. 自然度设计详解

自然度在 v3 里不靠运行时机制（主 loop 全程掌权，无拦截/退出/挂起
装置），而是**剧本工程**：守则、脚本格式、语义设计决定体验。以下是
逐场景的详细设计。

### 8.1 全局执行守则（卡片前导，所有 flow 共用）

```
【执行守则】
1. 先挖掘再提问：提问前先确认用户原话与历史中是否已含答案，
   已知则直接登记并推进，绝不重复问
2. 零提问直通：信息完备时直接执行，不制造形式化的确认
3. 跑题：先回答用户的新问题，然后可用一句话温柔桥接（带出当前
   进度），但不连续提醒两次；用户继续聊别的就自然陪着聊
4. 不机械播报进度，不暴露 flow/node/步骤编号等内部概念，
   保持平常对话语气
5. 失败说人话：用自然语言解释问题并给出重试/调整/放弃选项
6. 中断后回归时，先用一句话概括进度再推进
7. 对话中获得的任何新信息（确认结果、附加要求、修改），
   先 activate_flow 补录，再执行节点
```

### 8.2 Talk 步骤脚本格式：目标 + 完成条件 + 分支指引

Talk 脚本的核心是让 LLM 能自行判断"该不该问"；而完成判定用
`DoneWhen(input 键)` 声明式外化（工具层自动判定，见 7.1）：

```go
Talk("confirm", "确认受众与要求", `
  目标：明确故事的受众（儿童/成人）
  指引：
  - 用户消息已表明受众（如"给孩子"）→ 直接登记，不问
  - 否则用 ask_user_question 提问，相关问题可合并一次问
  - 用户回答中的附加要求（如"加一条龙"）一并登记到 input
`).DoneWhen("audience")   // 声明式完成判定：audience 登记即自动完成
```

对比机械式脚本（"请问受众是儿童还是成人？"）：分支指引让确认环节
在信息已知时自动隐形，这是自然度的第一来源。

### 8.3 跑题与回归（桥接一次原则）

```
（flow：确认已完成，故事初稿已生成，尚未交付）
用户: "对了，今天天气怎么样？"
主 LLM: "今天晴，25°C。对了，儿童向的太空故事已经写好了，要看看吗？"
        ← 先回答 + 一句桥接（带进度，不施压）
用户: "等等，25 度是多少华氏度？"
主 LLM: "大约 77°F。"   ← 不再第二次提醒，自然陪着聊
用户: "行，看故事吧"
主 LLM: （按 deliver 步骤呈现作品）
```

要点：桥接句只出现一次；用户再次跑题就纯回答；回归由用户发起，
回归时若隔了多轮，先用一句话概括进度（守则 6）。

### 8.4 中途修改与交付后润色（重跑语义）

```
（确认已完成，story 尚未执行）
用户: "主题改成海洋吧"
主 LLM: activate_flow("story003", {topic:"海洋"})   ← 幂等合并 input
        exec_node("story")                          ← 正常执行

（已交付全文）
用户: "结尾再温情一点"
主 LLM: activate_flow("story003", {note:"结尾更温情"})  ← 补录要求
        exec_node("story")          ← 重跑，输出覆盖旧值，新作品直达屏幕
```

重跑（exec_node 第 8 条）是自然度的关键支撑：没有它，改参数只能
重来整个 flow，润色则完全无处安放。

### 8.5 失败表达

节点失败时 exec_node 返回人类可读描述（工具层包装，不暴露堆栈），
主 LLM 按守则 5 处理：

```
主 LLM: "故事生成这一步出了点问题（模型服务暂时繁忙）。
        我可以再试一次，或者你想先调整一下主题？"
```

重试 = 直接再次 exec_node；换参数 = activate 补录后 exec；
放弃 = finish(abandon)。三条路都是普通对话，无需专门机制。

### 8.6 为什么不需要 v1 那堆机制

主 loop 从未失去控制权：

| v1 机制 | v3 为什么不需要 |
|---|---|
| 消息拦截/直投 waiting | 确认就是普通对话（ask_user_question） |
| 节点自判 / ExitToMain | 跑题就是普通对话，主 LLM 直接回答 |
| 挂起注册表 / resume / discard | FlowState 一直在、卡片一直在，随时接着走；放弃 = finish(abandon) |
| FlowContext 桥接 / runCtx 快照继承 / 作用域链 | 没有阻塞式引擎，没有节点级上下文 |
| AnswersConsumer / 历史顺序问题 | 没有工具内消费用户消息的场景 |
| 中断快照 tool_result | 无需退出交接，主 LLM 全程在场 |

### 8.7 自然度的反面清单（实现时逐条避免）

- 机械审问："请回答问题 1/3"；
- 重复提问：用户已说过的信息再问一遍；
- 催促：跑题后连续两次把话题拉回；
- 暴露内部概念："流程已执行到步骤 2"、"节点执行失败 code=500"；
- 静默继续：隔了多轮回来，不提进度直接往下做；
- 形式化确认：信息明明完备，还要问一遍"确认吗？"。

## 9. 上下文账本

| 环节 | 上下文策略 |
|---|---|
| 执行节点干活（exec_node 内部 LLM 调用） | **零上下文**：不带会话历史，模板只吃 FlowState 数据（硬边界） |
| flow 卡片 | activate 的 tool_result 一次入场（todo 式，第 7 节）+ 每次调用一行进度脚标；不进 System |
| exec_node 返回给主 LLM | 只回摘要；全文走事件流给前端（outputMode 控制） |
| 确认对话 | 正常对话消息（这是对话本身，不是 flow 污染） |
| 历史净增量 | activate/finish 的 tool_use+tool_result（简短）+ 正常对话 |

## 10. 事件（前端）

复用事件通道，新增：

```jsonc
{ "eventType": "flow_progress",
  "sessionId": "...", "flowId": "story003",
  "stepId": "story", "stepName": "生成故事",
  "phase": "start" | "done" | "error",
  "output": "全文或摘要" }
```

激活/完成可各发一条（flow_started / flow_finished）。
前端渲染：`event` 模式产出（5.2）渲染成消息式作品卡片（带 flow 来源
标识）；步骤进度可渲染为轻量指示。第一阶段可只处理作品卡片，
步骤指示第二阶段再做。

## 11. 改动清单

| 文件 | 改动 |
|---|---|
| `workflow/exec/workflow.go` | Builder 扩展 Description/InputSchema/Talk/Exec 步骤 + Render 卡片 |
| `workflow/node/chat_node.go` | UserTemplate bug 修复；模板渲染 + 零上下文调用逻辑抽为可复用执行核 |
| `agent/manager.go` | 注册 flow 工具组；暴露按 id 查 flow |
| `tools/flow_tools.go`（新） | FlowStore（对齐 TodoStore）+ activate_flow / exec_node / flow_status / finish_flow；exec_node 含迭代模式（5.3）；不侵入 SessionContext/buildRequest |
| 前端 | flow_progress 事件定义（第二阶段步骤卡片） |

对比 v1：`agent/flow_context.go`、`tools/run_flow.go` 的拦截/挂起逻辑、
`AnswersConsumer`、`Enqueue`、作用域链——**全部不需要**。

## 12. 实施里程碑

1. **M1 Flow 定义与卡片**：Builder 扩展（Talk/Exec/Description）+
   Render 卡片 + UserTemplate bug 修复 + 单元测试
2. **M2 激活与剧本传递**：FlowStore + activate_flow（卡片一次入场）/
   flow_status / finish_flow + 进度脚标；端到端验证"卡片可见、进度随
   tool_result 刷新"（todo 式，无内核改动）
3. **M3 执行核**：exec_node（FlowState 上游注入 + 模板渲染 + 零上下文调用 +
   依赖校验 + 摘要返回）；端到端跑通无确认 flow
4. **M4 自然度验收**：story003 加确认步骤（ask_user_question + activate
   补录），验收清单（对应 8.7 反面清单逐项反向验证）：
   - 零提问直通："给我 5 岁孩子写个太空故事" → 不提问直接产出
   - 不重复问：已提供的信息不再问
   - 跑题回归：答跑题 + 桥接一次；连续两次跑题不二次提醒
   - 中途修改："主题改成海洋" → 补录 + 正常执行，不重启 flow
   - 交付后润色："结尾温情一点" → 重跑覆盖，新作品直达屏幕
   - 失败说人话：节点故障时不暴露原始错误
   - 无机械播报：全程无"步骤 2/4"式措辞
5. **M5 迭代（可选）**：exec_node 批处理模式（展开/逐项零上下文/聚合/
   index 级跳过/逐项事件）；验收：多段落逐项翻译，故意失败一项后重试
   只补跑该项

## 13. 边界与演进

**本期边界**：

- 步骤顺序是"校验兜底 + LLM 跟随"，不是代码强制——接受这个软化，
  换取对话自然；
- FlowState 内存态，不持久化，重启丢失；
- 迭代仅支持单节点（5.3）；每项多节点子流程属演进项。

**演进方向**：

1. 多节点子流程迭代：每项内部跑多个节点组成的小流程（单节点迭代
   已入本期 5.3）；迭代并发（bounded parallel，阻塞式需等全部项完成）；
   超长聚合输出的分层归约（段内合并 → 组合并，支撑超长故事缝合）；
2. 卡片压缩与长会话策略（超长会话中 System 卡片的预算控制）；
3. Talk 步骤的结构化验收（如必须出现 ask_user_question 调用才算完成）；
4. flow 定义可视化/DLS（Builder 之上加声明式描述）。

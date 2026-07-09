# Worksflow 生成阶段工作台与团队协作原型文档

版本：v1.2  
日期：2026-07-09  
用途：用于创建 Figma / Framer / HTML 原型  
范围：覆盖登录后的生成阶段工作台、团队协作文档域、文档自由绑定、功能蓝图编辑器、原型开发工具接入，不覆盖首页营销页

## 1. 原型目标

本原型用于复刻并验证 Worksflow 在用户提交需求后的核心生成体验，重点表现：

- 用户从 prompt 进入生成工作台。
- AI 先生成 plan，再由用户确认实现。
- 生成过程以 checklist 形式透明展示。
- 生成结果可在 Preview 中查看。
- 用户可切换 Code、Database、项目设置和更多工具。
- 用户可继续用 prompt 迭代当前项目。
- 团队可从独立入口进入协作域，围绕项目创建、绑定和流转所有交付文档。
- 文档之间形成可追踪的上下游关系，并能把需求、原型、接口和开发文档注入到生成工作台上下文。
- 文档节点不被固定链路限制，可以按项目实际情况自由绑定文档、成员、功能、页面、接口和实现版本。
- 团队可以在蓝图编辑器中组合不同功能模块，形成可生成文档和代码的产品功能蓝图。
- 原型设计稿、开源画布工具和组件开发工具可以作为原型资产来源或验证入口。

原型不需要真实生成代码，但需要完整模拟状态流、布局切换、按钮状态、进度反馈和关键空态。

## 2. 原型范围

### 包含

- 工作台主界面。
- 计划生成态。
- 计划完成态。
- 构建执行态。
- 构建完成态。
- Preview 视图。
- Code 视图。
- Database 视图。
- 项目标题菜单。
- 更多设置菜单。
- 左侧继续输入区。
- 团队协作入口。
- 团队空间 Dashboard。
- 文档依赖图。
- 文档编辑器。
- 蓝图编辑器。
- 成员绑定和评审流。
- 原型开发工具入口。
- 设计稿导入 / 同步中心。

### 不包含

- 未登录首页。
- 账号登录流程。
- 真实发布流程。
- GitHub 授权流程。
- 数据库真实创建流程。
- 移动端完整适配，仅提供响应式规则。

## 3. 用户故事

1. 作为用户，我输入一个应用需求后，希望看到 AI 正在理解项目，而不是黑盒等待。
2. 作为用户，我希望先看到实现计划，并能决定是否执行。
3. 作为用户，我希望生成过程中知道当前在做哪一步、写了哪些文件。
4. 作为用户，我希望完成后立即查看可运行预览。
5. 作为用户，我希望能查看代码、终端输出和项目文件。
6. 作为用户，我希望能继续用自然语言迭代当前项目。
7. 作为产品负责人，我希望在团队协作域中创建需求文档，并把后续页面拆分、功能清单、接口契约和开发文档串起来。
8. 作为团队成员，我希望自己被绑定到具体文档节点，并清楚看到上游输入、下游交付和评审状态。
9. 作为设计或前端成员，我希望可以接入 Figma、Penpot、Excalidraw、tldraw 或组件原型工具，把设计稿/原型转换为可追踪的 UI 文档。
10. 作为开发者，我希望生成工作台可以读取已确认的需求、接口、UI 原型和开发文档，减少重复描述。
11. 作为项目负责人，我希望文档节点可以自由绑定，不被默认流程限制，适配不同团队和项目类型。
12. 作为架构或产品负责人，我希望通过蓝图编辑器组合功能模块，并从蓝图生成文档、页面、接口和开发任务。

## 4. 信息架构

```text
App Shell
├─ Global Navigation
│  ├─ Workbench
│  ├─ Team Collaboration
│  ├─ Recent Projects
│  └─ Account / Settings
├─ Workbench
│  ├─ Top Bar
│  │  ├─ Home
│  │  ├─ Workspace / project path
│  │  ├─ Project title menu
│  │  ├─ View switcher: Preview / Code / Database
│  │  ├─ More options
│  │  ├─ GitHub connect
│  │  ├─ Share
│  │  └─ Publish
│  ├─ Left Chat Panel
│  │  ├─ User prompt
│  │  ├─ AI plan / build messages
│  │  ├─ Task checklist
│  │  ├─ Linked team documents
│  │  ├─ Result summary
│  │  ├─ Version card
│  │  └─ Prompt composer
│  └─ Main Workspace
│     ├─ Preview view
│     ├─ Code view
│     └─ Database view
└─ Team Collaboration
   ├─ Team Space Dashboard
   ├─ Document Graph
   ├─ Document Editor
   ├─ Blueprint Editor
   ├─ Prototype Studio
   ├─ Design Import Center
   ├─ Review Center
   └─ Member / Permission Settings
```

## 5. 桌面布局规格

推荐原型画布：`1440 x 800` 或 `1460 x 772`

### 基础布局

```text
┌──────────────────────────────────────────────────────────────────────────────┐
│ Top Bar                                                                      │
├──────────────────────┬───────────────────────────────────────────────────────┤
│                      │                                                       │
│ Left Chat Panel      │ Main Workspace                                        │
│ 448px                │ Flexible width                                        │
│                      │                                                       │
│                      │                                                       │
│                      │                                                       │
│ ┌──────────────────┐ │                                                       │
│ │ Prompt Composer  │ │                                                       │
│ └──────────────────┘ │                                                       │
└──────────────────────┴───────────────────────────────────────────────────────┘
```

### 尺寸建议

- 顶部栏高度：`50px`
- 左侧面板宽度：`448px`
- 左侧面板内边距：`16px - 20px`
- 主工作区边距：右侧容器距离顶部栏 `0px`，内部卡片边距 `12px - 16px`
- 主工作区容器圆角：`8px - 10px`
- Prompt 输入框高度：默认 `136px` 左右，文本区 `80px`
- 底部输入框固定在左侧面板底部

## 6. 关键 Frame 清单

### Frame 01：提交后 Planning 中

用途：展示用户刚提交 prompt 后的工作台初始态。

左侧：

- 用户 prompt 气泡。
- Worksflow 标识。
- 状态文案：`Planning...`
- 底部输入框 placeholder：`What do you want to plan?`
- 输入区按钮：
  - `+`
  - `Standard`
  - `Select`
  - `Plan` 高亮
  - 停止按钮

右侧：

- Preview 模式高亮。
- 预览区空态：
  - Worksflow 标识
  - `Your preview will appear here`
  - 底部帮助链接：`Help Center`、`Join our Community`

线框：

```text
┌ Top Bar: Project / Preview Code Database / Share Publish ┐
├───────────────┬───────────────────────────────────────────┤
│ Prompt bubble │                                           │
│               │                                           │
│ worksflow          │              b                            │
│ Planning...   │       Your preview will appear here       │
│               │                                           │
│               │                                           │
│ Composer      │ Help Center     Join our Community        │
└───────────────┴───────────────────────────────────────────┘
```

### Frame 02：Plan 生成完成

用途：用户看到完整计划，尚未开始构建。

左侧内容：

- 文件读取状态：`0 1 2 3 4 5 6 7 8 9 files read`
- AI 计划标题：`Plan: Simple Todo App with Placeholder Data`
- 计划分组：
  - App Structure and State Foundation
  - Top Navigation Bar
  - Task Input and Task List
  - Filters and Empty States
  - Polish and Responsiveness
- Summary 段落。
- 响应操作：复制、好评、差评、更多。
- 主 CTA：`Implement this plan`
- 版本卡片：
  - 标题：`Create Todo App UI with Placeholder Data`
  - 副标题：`Version 1 at Jul 09 11:15 AM`
  - 收藏图标

右侧：

- Preview 空态。
- 中部提示：`Click Implement this plan to start building`
- `Implement this plan` 文本按钮样式更突出。

交互：

- 点击 `Implement this plan` 进入 Frame 03。
- 点击 `Plan` 按钮保持计划模式。
- 点击输入框可继续补充计划要求。

### Frame 03：构建执行中

用途：展示 AI 正在写文件、执行任务。

左侧内容：

- 用户确认消息：`Perfect, you can start implementing this plan!`
- Worksflow 文案：`I'll implement the plan now. Let me set up task tracking and start building.`
- Checklist：
  - `Create types and placeholder data files`
  - `Build TopNavBar component`
  - `Build TaskInput component`
  - `Build TaskItem and TaskList components`
  - `Build Filters component`
  - `Build EmptyState component`
  - `Wire everything together in App.tsx`
  - `Verify build passes`
- 当前任务状态：
  - 当前行蓝色旋转/加载图标。
  - 已完成行绿色勾。
  - 未开始行灰色空圆。
- 子状态示例：
  - `Writing src/components/EmptyState.tsx`
  - `Installing dependencies`
  - `Running build`

右侧：

- Preview 仍可能为空态。
- 如果代码已编译，可出现 loading 状态。

输入区：

- Placeholder：`How can Worksflow help you today? (or /command)`
- Send 按钮变为 `Stop generation`。
- Plan 按钮不高亮。

交互：

- 点击停止按钮：生成中断，显示 stopped 状态。
- 点击 checklist 项：可展开步骤详情。
- 用户仍可输入新需求，但发送按钮处于停止优先。

### Frame 04：构建完成 + Preview 加载成功

用途：展示完成后主成功态。

左侧：

- Checklist 全部绿色勾。
- 状态：`Plan completed`
- 完成文案：
  - `Done. The todo app is built with placeholder data only and the production build passes cleanly.`
- `What was built:` 列表：
  - Top navigation
  - Task input
  - Task list
  - Filters
  - Empty states
- 响应操作：复制、好评、差评、更多。
- 版本卡片：
  - `Build Todo App with React State`
  - `Version 2 at Jul 09 11:17 AM`

右侧 Preview：

- 顶部预览工具栏：
  - 地址栏 `/`
  - 刷新
  - 打开新窗口
  - 复制 / 设备 / 全屏类图标
- 应用预览内容：
  - 顶部导航 `Taskflow`
  - 统计：`3 Active`、`2 Done`、`5 Total`
  - 输入框：`Add a new task...`
  - 优先级：Low / Med / High
  - 筛选：All / Active / Completed
  - 任务列表
  - `Made in Worksflow` 标记

交互：

- 点击刷新：重新加载 preview。
- 点击 Preview 中任务 checkbox：任务完成状态变化。
- 点击 filter：任务列表变化。
- 点击 Code：切换到 Frame 05。
- 点击 Database：切换到 Frame 06。

### Frame 05：Code 视图

用途：展示开发者检查代码的模式。

右侧结构：

```text
┌──────────────────────────────────────────────┐
│ Files / Search                               │
├───────────────┬──────────────────────────────┤
│ file tree     │ editor                       │
│ .worksflow         │ line numbers                 │
│ src           │ code content                 │
│ package.json  │                              │
├───────────────┴──────────────────────────────┤
│ Worksflow | Publish Output | Terminal | +         │
│ [vite] page reload src/components/...        │
└──────────────────────────────────────────────┘
```

必须展示：

- 左侧文件树。
- 中央编辑器。
- 底部终端 tabs：
  - `Worksflow`
  - `Publish Output`
  - `Terminal`
  - `+`
- 终端示例：
  - `[vite] page reload src/components/TaskInput.tsx`

交互：

- 点击文件树文件：编辑器显示文件内容。
- 点击 Terminal：底部 panel 切换。
- 点击关闭终端：隐藏底部终端区。
- 点击 Preview：返回 Frame 04。

### Frame 06：Database 视图

用途：展示后端能力入口。

右侧内容：

- 标题：`Power up your backend with Worksflow Database`
- 副标题：`Ask Worksflow to create a database to unlock these services.`
- 六个能力卡片：
  - Tables
  - Authentication
  - Server functions
  - Secrets
  - User management
  - File storage
- 主 CTA：`Ask Worksflow to create a database`
- 次入口：`Connect to Supabase`

卡片格式：

```text
┌────────────────────────────┐
│ Icon                       │
│ Tables                     │
│ Create tables, manage...   │
│ Learn more                 │
└────────────────────────────┘
```

交互：

- 点击 `Ask Worksflow to create a database`：在左侧 composer 中注入创建数据库的 prompt，或直接发送。
- 点击 `Connect to Supabase`：打开连接弹窗。
- 点击 Learn more：打开文档链接或帮助弹窗。

### Frame 07：项目标题菜单

触发：点击顶部项目名。

菜单项：

- Recent projects
- Version history
- Transfer to...
- Rename...
- Duplicate
- Star project
- Export
- Delete

视觉：

- 深色浮层。
- 宽约 `240px`。
- 每项高度约 `32px`。
- 危险项 Delete 使用警示色或放在底部。

交互：

- `Version history`：打开版本历史面板。
- `Rename`：打开重命名弹窗。
- `Duplicate`：创建项目副本确认。
- `Export`：打开导出子菜单或下载。
- `Delete`：必须二次确认。

### Frame 08：更多菜单

触发：顶部模式切换右侧的更多/设置按钮。

菜单项：

- Analytics
- Knowledge
- Connectors
- All project settings
- Integrations
  - Stripe
  - Worksflow Database

用途：

- 承载项目增强能力。
- 避免顶栏塞满低频功能。

## 7. 组件清单

### TopBar

属性：

- projectName
- activeView：`preview | code | database`
- hasGitHubConnected
- publishState：`idle | publishing | published`

组件：

- Home button
- Workspace badge
- Project title button
- View switcher
- GitHub button
- Share button
- Publish button

状态：

- Normal
- Active view
- Hover
- Disabled
- Loading

### ChatPanel

属性：

- messages
- currentPhase：`planning | planReady | building | complete | error`
- versions

组件：

- UserPromptCard
- AssistantMessage
- FileReadStatus
- PlanBlock
- TaskChecklist
- ResultSummary
- ResponseActions
- VersionCard
- PromptComposer

### PromptComposer

默认结构：

```text
┌────────────────────────────────────┐
│ How can Worksflow help you today?       │
│                                    │
│ +   Standard   Select   Plan   ↑   │
└────────────────────────────────────┘
```

状态：

- Empty
- Focused
- Has text
- Planning mode
- Building mode
- Generating / stop button
- Disabled

按钮：

- `+`：上下文菜单。
- `Standard`：Agent/model 菜单。
- `Select`：上下文/选择工具。
- `Plan`：计划模式 toggle。
- `Send`：提交。
- `Stop`：中断生成。

### TaskChecklist

任务状态：

- pending：灰色空圆。
- active：蓝色 spinner。
- writing：active + 子文本。
- done：绿色勾。
- failed：红色错误。

任务行字段：

- icon
- title
- subStatus
- expandable

### PreviewPanel

状态：

- Empty：`Your preview will appear here`
- Loading：`Waiting for preview to load`
- Ready：展示 iframe / app preview
- Error：显示错误和重试按钮

工具栏：

- path input
- refresh
- open external
- copy
- device / layout
- fullscreen

### CodePanel

结构：

- FileExplorer
- Editor
- BottomConsole

FileExplorer：

- Files tab
- Search tab
- folder/file tree
- selected file

Editor：

- line numbers
- code area
- empty file state

BottomConsole：

- Worksflow tab
- Publish Output tab
- Terminal tab
- add tab
- close/collapse

### DatabasePanel

组件：

- Heading
- CapabilityCard x 6
- Primary CTA
- Supabase link

能力卡片字段：

- icon
- title
- description
- learnMoreLink

## 8. 关键交互流程

### Flow A：从 prompt 到 plan

1. 用户输入 prompt。
2. 点击 `Generate plan`。
3. 进入工作台。
4. 左侧显示 prompt。
5. Worksflow 显示 `Planning...`。
6. 读取文件状态出现。
7. 计划正文生成。
8. 显示 `Implement this plan`。

### Flow B：从 plan 到 build

1. 用户点击 `Implement this plan`。
2. 左侧添加确认消息。
3. Worksflow 生成 checklist。
4. 当前任务进入 active。
5. 每完成一步，灰色圆变绿色勾。
6. 文件写入时显示具体文件路径。
7. 执行 build。
8. checklist 全部完成。
9. 输出完成总结。
10. Preview 自动刷新并展示应用。

### Flow C：继续迭代

1. 用户在底部 composer 输入新需求。
2. 可选择是否开启 Plan。
3. 提交后在当前项目追加新任务。
4. 保留历史版本卡。
5. 新版本生成完成后添加新的 VersionCard。

## 9. 文案规范

### 计划中

- `Planning...`
- `I'll start by reading the current project files to understand the setup before creating a plan.`
- `files read`

### 计划完成

- `Plan: [Project Name]`
- `To proceed, switch back to "build" mode and I will implement this plan.`
- `Implement this plan`

### 构建中

- `I'll implement the plan now. Let me set up task tracking and start building.`
- `Writing [file path]`
- `Running build`
- `Verifying build`

### 构建完成

- `Done. The app is built and the production build passes cleanly.`
- `What was built:`
- `You can continue iterating from the prompt box below.`

### 空态

- Preview：`Your preview will appear here`
- Database：`Ask Worksflow to create a database to unlock these services.`
- Code：`Select a file to view its contents.`

## 10. 视觉规范

### 色彩

- 背景主色：`#111114`
- 面板背景：`#171719`
- 卡片背景：`#1E1E21`
- 边框：`rgba(255,255,255,0.08)`
- 主蓝：`#1488FC`
- 亮蓝：`#2BA6FF`
- 成功绿：`#4ADE80`
- 警示红：`#EF4444`
- 主文字：`#FFFEFE`
- 次文字：`rgba(255,255,255,0.6)`
- 弱文字：`rgba(255,255,255,0.45)`

### 字体

- UI 字体：Inter / system sans-serif
- Code 字体：Fira Code / Menlo / monospace

### 字号

- 顶栏按钮：`14px`
- 面板正文：`14px`
- 次级说明：`12px`
- 标题：`18px - 20px`
- Database 主标题：`20px`
- 代码：`12px - 13px`

### 圆角

- 主面板：`8px`
- 按钮：`6px - 8px`
- Composer：`8px`
- 卡片：`8px`
- 图标圆按钮：`999px`

## 11. 响应式规则

### 桌面

- 左侧面板固定 `448px`。
- 右侧自适应。
- Preview/Code/Database 保持同一顶部切换。

### 中等宽度

- 左侧面板可缩窄到 `360px`。
- 右侧保留最小宽度 `640px`。
- 顶部按钮可隐藏文字，只保留图标。

### 移动端建议

移动端不建议并排显示。

结构：

- 顶部显示项目名和更多按钮。
- 使用底部 tab：
  - Chat
  - Preview
  - Code
  - Database
- PromptComposer 固定底部。
- Chat 与 Preview 单屏切换。

## 12. 可访问性要求

- 所有 icon-only 按钮必须有 tooltip 和 aria-label。
- 当前视图必须有 selected 状态。
- Checklist 状态不能只靠颜色，需要图标区分。
- Stop generation 必须明确标注。
- Delete、Publish、Share 必须二次确认或明确结果。
- 输入框 focus ring 明显。
- 菜单支持 Esc 关闭。
- Tab 顺序：TopBar -> ChatPanel -> Composer -> MainWorkspace。

## 13. 原型验收标准

原型完成后应满足：

- 可以从 Planning 状态点击到 Plan Ready。
- 可以从 Plan Ready 点击进入 Building。
- Building 状态 checklist 能逐步变化。
- Complete 状态能展示版本卡和完成总结。
- Preview / Code / Database 三个模式可以切换。
- 项目标题菜单可打开并显示项目管理项。
- 更多菜单可打开并显示 Analytics / Knowledge / Connectors / Integrations。
- Composer 在生成中显示 Stop，完成后显示 Send。
- Preview 完成态有真实应用画面或高保真假画面。

## 14. 建议原型实现方式

### Figma

建议创建以下 Page：

- `01 Foundations`
- `02 Components`
- `03 Desktop Flow`
- `04 Menus`
- `05 Team Collaboration`
- `06 Document Graph`
- `07 Blueprint Editor`
- `08 Prototype Imports`
- `09 Responsive`

建议创建以下 Components：

- TopBar
- ViewSwitcher
- ChatPanel
- PromptComposer
- TaskChecklist
- VersionCard
- PreviewPanel
- CodePanel
- DatabasePanel
- ProjectMenu
- MoreMenu
- TeamDashboard
- DocumentGraphCanvas
- DocumentNode
- BindingInspector
- BlueprintEditor
- BlueprintNode
- BlueprintEdge
- ModuleLibrary
- PrototypeImportCard
- ReviewPanel

### HTML / React 原型

建议路由：

- `/workbench/planning`
- `/workbench/plan-ready`
- `/workbench/building`
- `/workbench/complete?view=preview`
- `/workbench/complete?view=code`
- `/workbench/complete?view=database`
- `/team/:teamId/project/:projectId`
- `/team/:teamId/project/:projectId/graph`
- `/team/:teamId/project/:projectId/blueprint`
- `/team/:teamId/project/:projectId/imports`

建议状态：

```ts
type Phase = 'planning' | 'planReady' | 'building' | 'complete' | 'error';
type View = 'preview' | 'code' | 'database';
type TaskStatus = 'pending' | 'active' | 'done' | 'error';
```

## 15. 原型内容示例数据

项目名：`Simple Todo App`

用户 prompt：

```text
Create a simple todo app with a top navigation, task list, filters, and empty states. Use placeholder data only.
```

任务列表：

```text
Create types and placeholder data files
Build TopNavBar component
Build TaskInput component
Build TaskItem and TaskList components
Build Filters component
Build EmptyState component
Wire everything together in App.tsx
Verify build passes
```

版本卡：

```text
Create Todo App UI with Placeholder Data
Version 1 at Jul 09 11:15 AM

Build Todo App with React State
Version 2 at Jul 09 11:17 AM
```

Preview 应用假数据：

```text
Taskflow
3 Active | 2 Done | 5 Total

Review the quarterly product roadmap - High - 2 days ago
Prepare slides for the design review meeting - Medium - Yesterday
Send the onboarding welcome email to new hires - Low - 4 days ago
Fix the navigation overflow on mobile viewports - High - Today
Archive the completed sprint backlog items - Medium - 5 days ago
```

## 16. 设计重点总结

这个原型的核心不是“代码编辑器”，而是“AI 生成过程的可见化工作台”。左侧负责让用户信任 AI 的过程，右侧负责让用户验证结果。最重要的体验节点是：

- 先计划，再实现。
- 任务拆解可见。
- 文件写入可见。
- 构建验证可见。
- 结果预览可见。
- 后续迭代入口始终可见。

## 17. 团队协作扩展定位

团队协作不是工作台里的一个弹窗，而是与 Workbench 并列的第二个主入口。Workbench 负责“生成与验证”，Team Collaboration 负责“组织项目知识、分配成员责任、维护上下游文档关系、沉淀交付资产”。

### 产品定位

- Workbench：面向个人或小组的 AI 生成、代码、预览、数据库能力。
- Team Collaboration：面向团队的文档生产、任务责任、评审、原型设计稿接入、上下游交付追踪。
- 两者关系：Team Collaboration 产出的已确认文档可以作为 Workbench 的上下文；Workbench 生成出的代码、预览、版本记录可以回写到对应文档节点。

### 入口设计

建议在全局左侧或顶部增加一级入口：

```text
Global Navigation
├─ Workbench
├─ Team Collaboration
├─ Recent
└─ Settings
```

入口状态：

- 默认：`Team Collaboration`
- 有待办时：显示未读圆点或数量，例如 `3`
- 当前团队空间：展示团队名和项目名，例如 `Acme / CRM Rewrite`
- 权限不足时：入口可见，进入后显示只读或申请加入状态

## 18. 团队协作文档域信息架构

```text
Team Collaboration
├─ Team Space Dashboard
│  ├─ Project overview
│  ├─ My assigned documents
│  ├─ Blocked dependencies
│  ├─ Recent activity
│  └─ Generate / Import entry
├─ Document Graph
│  ├─ Requirement Doc
│  ├─ Requirement Page Split Doc
│  ├─ Feature List
│  ├─ API Contract
│  ├─ Backend Development Doc
│  ├─ Page Prototype UI Doc
│  └─ Frontend Development Doc
├─ Blueprint Editor
│  ├─ Feature modules
│  ├─ Pages and routes
│  ├─ API and data nodes
│  ├─ Prototype assets
│  ├─ Generated document nodes
│  └─ Workbench implementation targets
├─ Document Editor
│  ├─ Document content
│  ├─ Linked upstream / downstream docs
│  ├─ Bound members
│  ├─ Review status
│  ├─ Version history
│  └─ Use as Worksflow context
├─ Prototype Studio
│  ├─ Canvas / wireframe mode
│  ├─ Imported design mode
│  ├─ Component prototype mode
│  └─ Preview / handoff mode
├─ Design Import Center
│  ├─ Figma
│  ├─ Penpot
│  ├─ Excalidraw
│  ├─ tldraw
│  ├─ Storybook / Ladle
│  └─ Image / SVG / PDF upload
└─ Review Center
   ├─ Pending reviews
   ├─ Comments
   ├─ Change requests
   └─ Approval history
```

## 19. 文档自由绑定模型

核心文档链路按有向依赖图建模，不建议做成普通文件夹树。原因是一个功能清单可能同时生成接口契约和 UI 原型，而前端开发文档通常同时依赖功能清单、页面原型和部分 API 契约。

文档节点必须支持自由绑定。系统可以提供默认链路模板，但模板只是初始化建议，不是强制流程。团队可以在任何文档节点之间建立依赖、引用、实现、评审、归属或组合关系。

### 默认链路模板

```text
需求文档
└─ 需求页面拆分文档
   └─ 功能清单
      ├─ API 契约
      │  └─ 后端开发文档
      └─ 页面原型 UI 文档
         └─ 前端开发文档
```

该链路用于快速创建项目骨架。创建后，用户可以：

- 删除任意节点。
- 新增任意类型节点。
- 将任意节点连接到任意节点。
- 一个节点绑定多个上游或下游。
- 一个下游节点同时消费多个上游文档。
- 将文档节点绑定到功能蓝图节点、页面节点、API 节点、原型资产或 Workbench 实现版本。

### 节点定义

| 文档节点 | 主要产出 | 默认负责人 | 主要下游 |
|---|---|---|---|
| 需求文档 | 业务目标、用户角色、范围、验收口径 | PM / 产品负责人 | 需求页面拆分文档 |
| 需求页面拆分文档 | 页面列表、路由、页面职责、入口关系 | PM + UX | 功能清单 |
| 功能清单 | 功能点、优先级、状态、边界条件 | PM + Tech Lead | API 契约、页面原型 UI 文档 |
| API 契约 | endpoint、request、response、错误码、鉴权 | 后端负责人 | 后端开发文档、前端开发文档 |
| 后端开发文档 | 数据模型、服务拆分、任务清单、测试策略 | 后端成员 | Workbench / 代码实现 |
| 页面原型 UI 文档 | 页面结构、组件状态、交互、空态、异常态 | 设计 / 前端 | 前端开发文档 |
| 前端开发文档 | 组件拆分、状态管理、接口对接、测试点 | 前端成员 | Workbench / 代码实现 |

### 依赖关系类型

- `depends_on`：当前文档依赖上游文档。
- `generates`：当前文档可生成下游文档草稿。
- `blocks`：当前文档未确认会阻塞下游。
- `implements`：Workbench 生成版本实现了该文档节点。
- `reviews`：成员或角色负责评审该文档节点。
- `references`：弱引用，用于补充背景，不阻塞下游。
- `composes`：当前节点组合了多个功能、页面、接口或文档节点。
- `derives_from`：当前节点由另一个节点派生生成。
- `syncs_with`：当前节点与外部设计稿、组件原型或代码版本保持同步。

### 自由绑定对象

文档节点可绑定的对象不只限于文档：

| 绑定对象 | 示例 | 用途 |
|---|---|---|
| 文档节点 | 功能清单、API 契约、UI 文档 | 建立上下游交付关系 |
| 成员 | PM、设计、前端、后端、测试、评审人 | 明确责任和通知范围 |
| 功能节点 | 登录、权限、搜索、支付、报表 | 将文档绑定到具体业务能力 |
| 页面节点 | `/login`、`/dashboard`、`/settings` | 关联页面职责、UI 原型和前端实现 |
| API 节点 | `GET /tasks`、`POST /auth/login` | 关联接口契约和前后端开发 |
| 数据节点 | User、Task、Order、Permission | 关联数据模型和后端实现 |
| 原型资产 | Figma frame、Penpot board、Storybook story | 关联设计来源和视觉状态 |
| Workbench 版本 | Version 2、preview URL、commit summary | 关联实际实现结果 |

### 自由绑定交互规则

- 支持从节点拖出连线绑定到任意节点。
- 支持在 Inspector 中通过 `Add binding` 搜索并绑定任意对象。
- 每条绑定关系必须选择关系类型，默认是 `references`。
- 绑定关系可以设置 `blocking`、`requiredForReview`、`notifyOnChange`。
- 如果绑定对象状态变化，当前节点显示影响提示，但不强制改变状态，除非该绑定设置了 `blocking`。
- 系统允许非树状结构、交叉连接和多父节点，必须避免 UI 假设一个节点只有一个上游。

## 20. 成员绑定与协作规则

每个文档节点都要支持自由绑定成员。这里的“下级成员”不是组织架构下级，而是该文档节点的下游执行、消费、评审或关注成员。

### 成员字段

```ts
type DocMemberRole =
  | 'owner'
  | 'assignee'
  | 'downstreamOwner'
  | 'reviewer'
  | 'watcher';
```

字段含义：

- `owner`：文档责任人，负责内容质量和状态推进。
- `assignee`：文档协作者，可以编辑内容。
- `downstreamOwner`：下游文档或实现负责人，例如前端、后端、设计。
- `reviewer`：评审人，负责 approve 或 request changes。
- `watcher`：关注者，只接收状态变化。

### 绑定规则

- 创建文档时必须至少指定一个 `owner`。
- 文档可以绑定任意数量的成员，且同一成员可以拥有多个角色。
- 文档进入 `Ready for Review` 前是否必须绑定下游负责人，由文档模板或项目规则配置。
- 上游文档内容发生重大修改时，下游文档自动进入 `Needs Sync` 状态。
- 下游负责人可以对上游文档提出 `Change Request`，但不能直接修改已批准版本。
- Workbench 使用文档作为上下文前，默认只读取 `Approved` 或用户手动确认的版本。
- 成员绑定可以继承自蓝图节点。例如“支付功能”绑定了后端负责人，则由该功能生成的 API 契约默认继承该负责人。
- 继承绑定必须可覆盖，覆盖后保留来源提示。

### 文档状态

```text
Draft
Ready for Review
Changes Requested
Approved
Needs Sync
Archived
```

## 21. 新增关键 Frame 清单

### Frame 09：全局双入口 App Shell

用途：展示 Workbench 与 Team Collaboration 的并列关系。

结构：

```text
┌────────────────────────────────────────────────────────────────┐
│ Global Top Bar: Team / Project / Search / Notifications / User │
├─────────────┬──────────────────────────────────────────────────┤
│ Workbench   │                                                  │
│ Team Collab │             Current main surface                 │
│ Recent      │                                                  │
│ Settings    │                                                  │
└─────────────┴──────────────────────────────────────────────────┘
```

交互：

- 点击 `Workbench`：回到生成阶段工作台。
- 点击 `Team Collaboration`：进入团队空间 Dashboard。
- 如果当前 Workbench 绑定了团队文档，顶部显示 `Linked docs: 4`。

### Frame 10：团队空间 Dashboard

用途：团队成员进入协作域后的默认页。

左侧：

- 团队 / 项目切换器。
- 导航：
  - Overview
  - Document Graph
  - Blueprint Editor
  - My Documents
  - Prototype Studio
  - Imports
  - Reviews
  - Members

主区域：

- Project overview：
  - 项目名
  - 当前阶段
  - 文档完成率
  - 阻塞数量
- My assigned documents：
  - 文档标题
  - 文档类型
  - 状态
  - 截止时间
- Blocked dependencies：
  - 阻塞文档
  - 被阻塞下游
  - 责任人
- Recent activity：
  - 谁更新了什么
  - 哪个文档进入评审
  - 哪个原型已同步

主 CTA：

- `Create document`
- `Generate document chain`
- `Open blueprint editor`
- `Import prototype`

### Frame 11：文档依赖图 Document Graph

用途：可视化展示自由绑定后的文档、成员、功能、页面、接口、原型和实现关系。

画布结构：

```text
[需求文档] ──depends_on──→ [功能清单]
     │                         │
     │ references              ├──generates──→ [API 契约] ──implements──→ [后端开发文档]
     │                         │
     └──syncs_with──→ [蓝图: 任务管理] ──composes──→ [页面: /tasks]
                               │
                               └──generates──→ [页面原型 UI 文档] ──generates──→ [前端开发文档]
```

节点内容：

- 文档类型 icon。
- 文档标题。
- 状态 badge。
- 负责人头像。
- 阻塞标记。
- 最近更新时间。
- 绑定数量。
- 外部同步状态。

右侧 Inspector：

- 节点详情。
- 自由绑定列表。
- 上游依赖。
- 下游输出。
- 关联蓝图节点。
- 关联原型资产。
- 关联 Workbench 版本。
- 绑定成员。
- 评审状态。
- `Add binding`
- `Change relation type`
- `Open document`
- `Generate downstream doc`
- `Use as Worksflow context`

交互：

- 拖动画布查看大项目图。
- 点击节点打开 Inspector。
- 双击节点进入文档编辑器。
- 点击连线查看依赖关系说明。
- 从节点连接锚点拖出连线，松开到目标节点后选择关系类型。
- 点击 `Add binding` 搜索成员、文档、功能、页面、API、原型资产或 Workbench 版本。
- 使用图谱筛选器只显示 `blocking`、`review`、`implementation` 或 `prototype` 关系。
- 节点状态变更时，下游节点自动显示影响提示。

### Frame 12：文档编辑器

用途：创建和编辑单个交付文档。

布局：

```text
┌────────────────────────────────────────────────────────────────┐
│ Doc Header: type / title / status / owner / review / actions   │
├─────────────────┬──────────────────────────────┬───────────────┤
│ Outline         │ Editor                       │ Context Panel │
│ Sections        │ Markdown / rich text blocks  │ Links         │
│ Linked docs     │ Tables / API schema / media  │ Members       │
│ Versions        │ Comments                     │ Reviews       │
└─────────────────┴──────────────────────────────┴───────────────┘
```

顶部动作：

- `Save draft`
- `Request review`
- `Approve`
- `Generate downstream`
- `Use in Workbench`
- `Export`

右侧 Context Panel：

- Upstream：
  - 需求文档
  - 功能清单
- Downstream：
  - 前端开发文档
  - 后端开发文档
- Bound members：
  - owner
  - assignee
  - downstreamOwner
  - reviewer
- Prototype links：
  - 设计稿链接
  - 原型画布
  - Storybook 组件状态

### Frame 13：原型开发工具 Prototype Studio

用途：在团队协作域内创建或接入页面原型 UI 文档。

模式切换：

- `Wireframe`
- `Design import`
- `Component prototype`
- `Preview handoff`

Wireframe 模式：

- 基础画布。
- 页面 frame。
- 组件库。
- 连线和流程箭头。
- 状态标注：empty、loading、error、success、disabled。

Design import 模式：

- 展示已接入的设计稿 frame。
- 支持选中 frame 生成页面原型 UI 文档。
- 支持同步组件、颜色、字号、间距和切图引用。

Component prototype 模式：

- 接入 Storybook / Ladle 组件目录。
- 将组件状态绑定到页面原型 UI 文档。
- 选择组件后可生成前端开发文档中的组件拆分章节。

Preview handoff 模式：

- 设计稿 / 原型画布左侧。
- 文档注释和验收点右侧。
- 支持 `Create frontend doc from prototype`。

### Frame 14：设计稿导入 / 同步中心

用途：统一管理外部原型和设计资产来源。

导入源：

- Figma file URL。
- Penpot project URL 或自托管实例。
- Excalidraw `.excalidraw` 文件或分享链接。
- tldraw snapshot / SDK canvas。
- Storybook / Ladle URL。
- 图片、SVG、PDF。

导入卡片字段：

- source type
- source name
- sync status
- last synced time
- linked document
- owner
- permissions

状态：

- `Not connected`
- `Connected`
- `Syncing`
- `Needs permission`
- `Sync failed`
- `Outdated`

交互：

- `Connect source`：打开授权或 URL 输入弹窗。
- `Map frames`：选择外部 frame 对应内部页面。
- `Generate UI doc`：从选中 frame 生成页面原型 UI 文档草稿。
- `Sync changes`：拉取最新设计稿。
- `Detach`：解除外部关联，但保留已生成文档。

### Frame 15：评审中心 Review Center

用途：集中处理文档评审、设计确认和上下游变更。

列表字段：

- 文档标题。
- 文档类型。
- 当前状态。
- 责任人。
- 评审人。
- 阻塞下游。
- 最后更新。

筛选：

- Assigned to me
- Waiting for my review
- Blocked
- Needs sync
- Approved

详情面板：

- 修改摘要。
- 评论线程。
- 上下游影响。
- `Approve`
- `Request changes`
- `Mark as synced`

### Frame 16：Workbench 绑定团队文档

用途：让生成阶段工作台读取团队文档上下文，并把生成结果回写到团队协作域。

Workbench 顶部新增：

- `Linked docs`
- `Context locked`
- `Sync back`

左侧 ChatPanel 新增卡片：

```text
Linked team documents
├─ 功能清单 · Approved
├─ API 契约 · Approved
├─ 页面原型 UI 文档 · Needs Sync
└─ 前端开发文档 · Draft
```

交互：

- 点击 `Linked docs`：打开文档选择器。
- 勾选文档后，PromptComposer 上方显示当前上下文摘要。
- 如果文档不是 Approved，系统提示：`This document is not approved. Use this version anyway?`
- 生成完成后点击 `Sync back`，把实现版本、预览链接、代码摘要写回对应前端/后端开发文档。

### Frame 17：蓝图编辑器 Blueprint Editor

用途：将不同功能模块组合成产品蓝图，并从蓝图生成文档、页面、接口、原型和 Workbench 实现任务。

蓝图编辑器与 Document Graph 的区别：

- Document Graph 关注“文档与交付物之间的依赖关系”。
- Blueprint Editor 关注“功能如何组合成产品能力，以及这些功能需要哪些页面、接口、数据、权限和原型支持”。

布局：

```text
┌────────────────────────────────────────────────────────────────────────┐
│ Blueprint Header: name / version / status / owner / generate actions  │
├───────────────┬──────────────────────────────────────┬────────────────┤
│ Module Library│ Canvas                               │ Inspector      │
│ Feature packs │ [功能]──uses──[页面]──calls──[API]   │ Properties     │
│ Page patterns │    │           │          │           │ Bindings       │
│ API patterns  │    └──requires──[权限]──reads──[数据] │ Generated docs │
│ UI patterns   │                                      │ Members        │
└───────────────┴──────────────────────────────────────┴────────────────┘
```

左侧 Module Library：

- Feature packs：
  - Auth
  - Team management
  - Task workflow
  - Search
  - Notification
  - Payment
  - Reporting
  - File upload
- Page patterns：
  - List page
  - Detail page
  - Form page
  - Settings page
  - Dashboard page
  - Empty state page
- API patterns：
  - CRUD resource
  - Search endpoint
  - Auth endpoint
  - Webhook
  - Upload endpoint
- UI patterns：
  - Table
  - Filter bar
  - Kanban
  - Timeline
  - Modal form
  - Side panel

画布节点类型：

| 节点类型 | 示例 | 可生成内容 |
|---|---|---|
| Feature | 任务管理、团队权限、支付订阅 | 功能清单、验收标准、开发任务 |
| Page | `/tasks`、`/billing`、`/members` | 页面拆分文档、UI 原型文档、前端开发文档 |
| Component | TaskCard、FilterBar、MemberPicker | 组件清单、Storybook stories、前端实现提示 |
| API | `GET /tasks`、`POST /members/invite` | API 契约、后端开发文档 |
| Data Model | Task、User、Role、Invoice | 数据模型、数据库表、后端服务说明 |
| Permission | Admin、Editor、Viewer | 权限矩阵、鉴权规则、测试用例 |
| Prototype | Figma frame、Penpot board、tldraw canvas | 页面原型 UI 文档 |
| Workbench Target | Build v3、Refactor auth、Add billing | 生成任务、版本回写、实现追踪 |

连线类型：

- `contains`：功能包含页面或子功能。
- `uses`：功能使用组件、页面或外部能力。
- `calls`：页面或组件调用 API。
- `reads`：API 读取数据模型。
- `writes`：API 写入数据模型。
- `requires`：功能或页面需要权限。
- `renders`：页面渲染组件。
- `syncs_with`：节点同步外部原型或代码资产。
- `generates`：蓝图节点可生成文档节点。
- `implemented_by`：蓝图节点已由 Workbench 版本实现。

右侧 Inspector：

- 节点名称、类型、描述。
- 输入和输出。
- 绑定文档。
- 绑定成员。
- 绑定原型资产。
- 生成设置。
- 风险和缺失项。
- `Generate docs from selection`
- `Use selection in Workbench`
- `Create prototype from selection`

顶部动作：

- `Save blueprint`
- `Validate blueprint`
- `Generate documents`
- `Generate prototype brief`
- `Use in Workbench`
- `Export blueprint`

组合交互：

- 从左侧 Module Library 拖动功能模块到画布。
- 多选节点后点击 `Group as capability`，形成复合功能。
- 拖动连线建立功能、页面、接口、数据和权限关系。
- 选中一个功能模块后，可以一键生成该功能相关的文档节点。
- 蓝图校验会提示缺失页面、缺失 API、权限未定义、无负责人、无验收标准等问题。
- 蓝图节点可以自由绑定文档节点和成员，绑定关系会同步显示到 Document Graph。

蓝图状态：

```text
Draft
Validated
Ready for Docs
Docs Generated
In Implementation
Implemented
Outdated
```

蓝图示例：

```text
[Feature: Task Workflow]
├─ contains → [Page: /tasks]
├─ contains → [Page: /tasks/:id]
├─ uses → [Component: FilterBar]
├─ uses → [Component: TaskCard]
├─ calls → [API: GET /tasks]
├─ calls → [API: PATCH /tasks/:id/status]
├─ reads → [Data Model: Task]
├─ requires → [Permission: Editor]
└─ syncs_with → [Prototype: Figma Task List Frame]
```

## 22. 原型工具与设计稿接入方案

原型开发工具建议做成开放式接入层，而不是绑定单一供应商。原型中需要表现三类能力：内置画布、外部设计稿导入、组件级原型验证。

### 推荐接入类别

| 类别 | 候选工具 | 适合场景 | 原型中表现 |
|---|---|---|---|
| 开源设计平台 | [Penpot](https://penpot.app/)、[Penpot Self-host](https://penpot.app/self-host) | 团队需要开源或自托管设计协作 | Connect Penpot、选择项目、同步 frame |
| 商业设计稿 | [Figma REST API](https://developers.figma.com/docs/rest-api/file-endpoints/)、[Figma Plugin API](https://developers.figma.com/docs/plugins/api/api-reference/) | 团队已有 Figma 文件和组件库 | 粘贴 Figma URL、选择 frame、生成 UI 文档 |
| 白板 / 低保真 | [Excalidraw](https://github.com/excalidraw/excalidraw) | 需求讨论、流程图、低保真线框 | 导入白板、转换为页面流程 |
| 无限画布 SDK | [tldraw SDK](https://tldraw.dev/) | 自研原型画布、协作标注、产品流程图 | 内置 Prototype Studio canvas |
| 组件原型 | [Storybook](https://storybook.js.org/docs)、[Ladle](https://ladle.dev/docs/) | 前端组件状态验证和交互交付 | 绑定组件 stories 到 UI 文档 |
| 自动化验证 | [Playwright Component Testing](https://playwright.dev/docs/test-components) | 原型/组件状态截图和交互验证 | 从 UI 文档生成测试点 |

### 接入原则

- 设计稿是资产来源，不是唯一事实源；内部页面原型 UI 文档才是团队流转的结构化交付物。
- 外部设计稿同步后必须生成可审阅的快照，避免设计源变化导致历史文档失真。
- 原型工具导入的 frame 需要映射到内部页面、组件、状态和验收点。
- 对 Workbench 暴露的是结构化上下文，不是直接把设计稿截图塞进 prompt。

### 设计稿到开发文档的转换链路

```text
Figma / Penpot / Excalidraw / tldraw / Storybook
        ↓
Design Import Center
        ↓
页面原型 UI 文档
        ↓
前端开发文档
        ↓
Workbench 生成 / 修改代码
        ↓
Preview + Code summary 回写文档
```

## 23. 团队协作数据模型建议

```ts
type DocType =
  | 'requirement'
  | 'pageSplit'
  | 'featureList'
  | 'apiContract'
  | 'backendDev'
  | 'uiPrototype'
  | 'frontendDev';

type DocStatus =
  | 'draft'
  | 'readyForReview'
  | 'changesRequested'
  | 'approved'
  | 'needsSync'
  | 'archived';

type DependencyType =
  | 'depends_on'
  | 'generates'
  | 'blocks'
  | 'implements'
  | 'reviews'
  | 'references'
  | 'composes'
  | 'derives_from'
  | 'syncs_with';

type DocMemberRole =
  | 'owner'
  | 'assignee'
  | 'downstreamOwner'
  | 'reviewer'
  | 'watcher';

type BindingTargetKind =
  | 'document'
  | 'member'
  | 'blueprint'
  | 'feature'
  | 'page'
  | 'component'
  | 'api'
  | 'dataModel'
  | 'permission'
  | 'prototype'
  | 'workbenchVersion'
  | 'externalAsset';

type BlueprintNodeType =
  | 'feature'
  | 'page'
  | 'component'
  | 'api'
  | 'dataModel'
  | 'permission'
  | 'prototype'
  | 'workbenchTarget';

type BlueprintEdgeType =
  | 'contains'
  | 'uses'
  | 'calls'
  | 'reads'
  | 'writes'
  | 'requires'
  | 'renders'
  | 'syncs_with'
  | 'generates'
  | 'implemented_by';

interface TeamDocument {
  id: string;
  teamId: string;
  projectId: string;
  type: DocType;
  title: string;
  status: DocStatus;
  ownerId: string;
  members: DocumentMember[];
  dependencies: DocumentDependency[];
  bindings: NodeBinding[];
  prototypeArtifacts: PrototypeArtifact[];
  version: number;
  lastApprovedVersion?: number;
  updatedAt: string;
}

interface DocumentMember {
  userId: string;
  role: DocMemberRole;
  boundReason?: string;
}

interface DocumentDependency {
  sourceDocId: string;
  targetDocId: string;
  type: DependencyType;
  isBlocking: boolean;
}

interface NodeBinding {
  id: string;
  sourceKind: BindingTargetKind;
  sourceId: string;
  targetKind: BindingTargetKind;
  targetId: string;
  relation: DependencyType | BlueprintEdgeType;
  isBlocking: boolean;
  requiredForReview: boolean;
  notifyOnChange: boolean;
  inheritedFromBindingId?: string;
  createdBy: string;
  createdAt: string;
}

interface PrototypeArtifact {
  id: string;
  source: 'internalCanvas' | 'figma' | 'penpot' | 'excalidraw' | 'tldraw' | 'storybook' | 'ladle' | 'upload';
  sourceUrl?: string;
  externalId?: string;
  linkedDocId: string;
  syncStatus: 'notConnected' | 'connected' | 'syncing' | 'needsPermission' | 'syncFailed' | 'outdated';
  snapshotUrl?: string;
  lastSyncedAt?: string;
}

interface Blueprint {
  id: string;
  teamId: string;
  projectId: string;
  title: string;
  status: 'draft' | 'validated' | 'readyForDocs' | 'docsGenerated' | 'inImplementation' | 'implemented' | 'outdated';
  ownerId: string;
  nodes: BlueprintNode[];
  edges: BlueprintEdge[];
  generatedDocIds: string[];
  version: number;
  updatedAt: string;
}

interface BlueprintNode {
  id: string;
  type: BlueprintNodeType;
  title: string;
  description?: string;
  boundDocumentIds: string[];
  boundMemberIds: string[];
  boundPrototypeArtifactIds: string[];
  generatedDocIds: string[];
  position: { x: number; y: number };
}

interface BlueprintEdge {
  id: string;
  sourceNodeId: string;
  targetNodeId: string;
  type: BlueprintEdgeType;
  isRequired: boolean;
}
```

## 24. 团队协作关键流程

### Flow D：从需求生成完整文档链

1. 用户进入 Team Collaboration。
2. 点击 `Generate document chain`。
3. 输入项目背景或选择已有需求文档。
4. 系统生成默认链路模板：
   - 需求文档
   - 需求页面拆分文档
   - 功能清单
   - API 契约
   - 后端开发文档
   - 页面原型 UI 文档
   - 前端开发文档
5. 用户可以删除节点、增加节点、改连线、添加交叉绑定。
6. 用户确认后进入 Document Graph，并将所有节点设为 `Draft`。
7. 用户逐个绑定 owner、downstreamOwner 和 reviewer，也可以从蓝图节点继承绑定成员。

### Flow E：绑定下级成员并推进评审

1. 用户打开功能清单。
2. 在右侧 Bound members 中添加前端、后端、设计负责人。
3. 点击 `Generate downstream doc`。
4. 系统创建 API 契约和页面原型 UI 文档草稿。
5. 下游负责人收到待办。
6. 上游功能清单进入 `Ready for Review`。
7. reviewer 通过后状态变为 `Approved`。
8. 下游文档解除阻塞。

### Flow F：导入设计稿生成页面原型 UI 文档

1. 用户进入 Design Import Center。
2. 选择 `Connect Figma`、`Connect Penpot` 或上传低保真文件。
3. 授权或输入 URL。
4. 选择需要导入的 frame。
5. 系统生成页面列表、组件清单、状态清单和交互说明。
6. 用户修订后点击 `Request review`。
7. 页面原型 UI 文档通过后，可生成前端开发文档。

### Flow G：团队文档驱动 Workbench 生成

1. 前端开发文档进入 `Approved`。
2. 用户点击 `Use in Workbench`。
3. Workbench 打开，并自动绑定相关文档上下文。
4. 用户输入补充 prompt。
5. Worksflow 生成计划时引用已绑定文档。
6. 生成完成后，用户点击 `Sync back`。
7. 前端开发文档记录实现版本、预览链接和代码摘要。

### Flow H：用蓝图编辑器组合功能并生成交付物

1. 用户进入 Team Collaboration。
2. 点击 `Open blueprint editor`。
3. 从 Module Library 拖入功能模块，例如 Task Workflow、Team management、Notification。
4. 为功能模块添加页面、组件、API、数据模型和权限节点。
5. 用连线表达组合关系，例如 `contains`、`calls`、`requires`、`renders`。
6. 多选一组节点，点击 `Group as capability`，形成复合功能。
7. 点击 `Validate blueprint`，系统提示缺失接口、缺失权限、无负责人或无原型资产。
8. 用户补齐绑定成员和文档关系。
9. 点击 `Generate documents`。
10. 系统生成或更新需求页面拆分文档、功能清单、API 契约、页面原型 UI 文档、前后端开发文档。
11. 用户选择蓝图中的一部分，点击 `Use selection in Workbench`。
12. Workbench 只读取所选功能组合的相关文档、页面、API 和原型上下文。

## 25. 团队协作补充文案

### 入口与 Dashboard

- `Team Collaboration`
- `Create, connect, and review every project document.`
- `Generate document chain`
- `Import prototype`
- `My assigned documents`
- `Blocked dependencies`

### 文档图谱

- `This document blocks 2 downstream docs.`
- `No downstream owner assigned.`
- `Add binding`
- `Select relation type`
- `This binding is inherited from Blueprint: Task Workflow.`
- `Show only blocking relations`
- `Generate downstream doc`
- `Use as Worksflow context`
- `Open linked prototype`

### 蓝图编辑器

- `Open blueprint editor`
- `Drag modules to compose a product capability.`
- `Group as capability`
- `Validate blueprint`
- `Generate documents from selection`
- `Use selection in Workbench`
- `Missing API contract`
- `No owner assigned`
- `This feature is implemented by Workbench Version 3.`

### 文档编辑器

- `Request review`
- `Approve`
- `Request changes`
- `Mark as synced`
- `This upstream document changed after approval.`
- `Use approved version in Workbench`

### 原型接入

- `Connect design source`
- `Map frames to pages`
- `Generate UI prototype document`
- `Sync latest changes`
- `The external source changed. Review differences before syncing.`

## 26. 团队协作原型验收标准

团队协作扩展完成后，原型应满足：

- 全局导航中存在 Workbench 和 Team Collaboration 两个一级入口。
- 可以进入团队空间 Dashboard，并看到我的文档、阻塞依赖和最近活动。
- 可以打开 Document Graph，并看到默认文档链路模板生成的节点。
- 文档节点可以自由绑定到其他文档、成员、功能、页面、API、数据模型、原型资产和 Workbench 版本。
- 文档节点可点击，右侧 Inspector 能展示自由绑定、上下游、成员、状态和动作。
- 可以从任意节点拖出连线到任意目标节点，并选择关系类型。
- 可以从需求或功能清单生成下游文档。
- 每个文档可以绑定 owner、assignee、downstreamOwner、reviewer 和 watcher。
- 可以打开 Blueprint Editor，用功能、页面、组件、API、数据模型、权限、原型和 Workbench Target 节点组合功能蓝图。
- Blueprint Editor 支持从 Module Library 拖拽模块、节点连线、组合能力、校验缺失项。
- 可以从蓝图选择区域生成文档，也可以把选择区域作为 Workbench 上下文。
- 文档支持 Draft、Ready for Review、Changes Requested、Approved、Needs Sync、Archived 状态。
- 可以进入 Design Import Center，并展示 Figma、Penpot、Excalidraw、tldraw、Storybook/Ladle、Upload 等入口。
- 可以从导入的设计稿或画布生成页面原型 UI 文档。
- 可以从页面原型 UI 文档生成前端开发文档。
- 可以把 Approved 文档绑定到 Workbench 作为生成上下文。
- Workbench 生成完成后，可以把实现摘要和预览链接回写到对应开发文档。

## 27. 扩展后的原型路由建议

```text
/workbench/:projectId
/workbench/:projectId?view=preview
/workbench/:projectId?view=code
/workbench/:projectId?view=database

/team/:teamId
/team/:teamId/project/:projectId
/team/:teamId/project/:projectId/graph
/team/:teamId/project/:projectId/blueprint
/team/:teamId/project/:projectId/blueprint/:blueprintId
/team/:teamId/project/:projectId/doc/:docId
/team/:teamId/project/:projectId/prototype
/team/:teamId/project/:projectId/imports
/team/:teamId/project/:projectId/reviews
```

## 28. 扩展后的设计重点

加入团队协作后，原型的核心从“AI 生成过程可见化”升级为“团队知识到 AI 生成的闭环”：

- 团队在协作域创建和确认文档。
- 文档节点可以自由绑定，不被固定流程限制。
- 蓝图编辑器用于组合不同功能，并把功能组合映射到页面、接口、数据、权限、原型和实现任务。
- 文档之间有明确但可自定义的上下游依赖。
- 成员责任绑定在具体文档节点上。
- 原型设计稿可以接入，但必须转成结构化 UI 文档。
- Workbench 使用已确认文档作为上下文。
- 生成结果回写团队文档，形成可追踪历史。

这套结构可以支持从需求、功能蓝图、原型、接口到前后端开发的完整原型，而不只是一个单点 AI 生成页面。

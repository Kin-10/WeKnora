# 00 仓库基线与适配核查

## 核查状态

目标为 Kin-10/WeKnora，分支由用户工作副本决定。本包不锁定一个未经读取的提交。此次可读取腾讯上游开发和对话接口文档，无法直接读取目标分支源码，因此不能宣称已完成 fork 差异审计。

上游文档记录了会话附件异步上传、状态查询以及 agent_id 参数；本包仅将它们作为候选适配入口。不得把附件 ready 自动视为已完成招标结构化解析。

## P00 需要执行的只读调查

```bash
git status --short
git rev-parse HEAD
git branch --show-current
git remote -v
rg --files -g 'AGENTS.md' -g 'go.mod' -g 'package.json' -g '*lock*' -g '*migrat*'
rg -n 'temporary.documents|attachments|TemporaryDocument' internal frontend
rg -n 'CustomAgent|AgentConfig|agent-chat|tool_call' internal frontend
rg -n 'asynq|outbox|StreamResponse|Last-Event-ID' internal
rg -n 'tenant_id|TenantID|Authoriz|Permission|RBAC' internal/router internal/middleware
```

路径不匹配时用 rg --files 定位，不创造占位类绕过真实服务。git remote 中如有凭据，只保留脱敏结果。不要 reset、clean 或覆盖用户未提交文件。

## 集成映射的必填内容

| 领域 | 需记录的证据 |
|---|---|
| Agent | 保存模型/工具配置的位置、工具定义与注册、运行用户身份传播 |
| 聊天 | 消息存储结构、SSE 事件、渲染入口、回读与删除行为 |
| 附件 | 原件存储、解析触发、TTL、清理任务、文件复用和权限 |
| 解析 | 页/块/表格/图片返回结构、定位字段可空条件、OCR 失败表示 |
| 知识库 | 文件/分块/修订模型、筛选能力、检索权限、原件获取 |
| 任务 | 队列、worker、重试、取消、事务发件箱是否现成可用 |
| DB | 数据库类型、tenant/user/session ID 原生类型、迁移命名规范 |
| 前端 | Vue 状态库、TDesign 版本、HTTP客户端、流读取和路由规范 |
| 导出 | 现有文件产物接口、下载鉴权、可复用模板能力 |

每项写真实文件路径、符号名、已有能力、缺口、适配方案、对应测试。新增工具名和路径以本包为逻辑建议，最终映射由源码确认。

## 基线产物

- progress/BASELINE.md：目标提交、工作区改动、运行时版本、基线启动/测试结果。
- progress/INTEGRATION_MAP.md：上表的具体结果。
- progress/ADR-001.md：是否沿用临时上传通道，或仅复用上传界面调用新增持久上传接口。
- progress/P00.md：能否进入 P01，以及影响后续的实际阻塞。

默认方案：在标书模式下复用上传交互，调用新增持久项目文件上传服务；上传存储与解析引擎沿用 WeKnora 的服务适配器。只有明确证明可安全提升临时附件生命周期时，才选择临时附件转项目原件方案，禁止靠延长 TTL 代替项目存档。

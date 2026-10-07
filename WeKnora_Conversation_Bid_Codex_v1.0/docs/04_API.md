# 04 API、卡片与事件契约

`contracts/openapi.json` 中列出的 `/api/v1/bid` 均为拟新增接口，不宣称是 WeKnora 现有API。目标采用OpenAPI 3.1；实现时将schema纳入后端和前端契约检查。

## 1. 请求约定

认证复用宿主。服务端从认证上下文确定租户与用户，客户端body不得提交tenant_id来切换范围。写请求带 `Idempotency-Key`；更新请求body含 expected_revision 或 expected_content_revision。相同身份+路由+key、相同body返回原结果；不同body返回409 IDEMPOTENCY_CONFLICT。单独存request_hash，避免重复扣费和重复启动任务。

幂等窗口建议至少7天；任务本身再按input_fingerprint去重。客户端通过资源快照恢复，不能因幂等记录过期就无条件重跑已完成任务。列表采用cursor/limit，limit默认50最大200。

## 2. 关键接口

| 方法与后缀（前缀/api/v1/bid） | 行为 |
|---|---|
| POST /projects | 校验会话和智能体，创建或返回会话所绑定项目 |
| GET /projects/{project_id} | 返回当前项目快照、revision与latest_event_seq |
| POST /projects/{project_id}/files | multipart持久化原件，返回文件对象；不隐式合并另一项目 |
| POST /projects/{project_id}/analyses | 创建document_set快照与解析任务 |
| GET /projects/{project_id}/analyses/{analysis_id} | 解析状态、覆盖、标段及待确认信息 |
| PATCH /projects/{project_id}/analysis-corrections | 修改候选结果，记录人工修正 |
| PUT /projects/{project_id}/selection | 确认标段与生效selection版本 |
| GET /projects/{project_id}/requirements | 按标段返回共通与专属要求 |
| POST /projects/{project_id}/confirmations | 确认要求/目录/事实等具体revision |
| POST /projects/{project_id}/outlines | 生成目录草案任务 |
| PUT /projects/{project_id}/outline | 保存人工调整后的目录 |
| POST /projects/{project_id}/materials/match | 在授权范围匹配，返回任务 |
| PUT /projects/{project_id}/material-bindings | 设置已核对的资料引用 |
| PUT /projects/{project_id}/facts | 保存候选事实；确认需独立confirmation |
| POST /projects/{project_id}/generations | 按章节和固定输入版本创建任务 |
| GET/PUT /projects/{project_id}/sections/{section_id} | 读取与保存AST版本 |
| POST /projects/{project_id}/sections/{section_id}/apply-candidate | 对比后应用AI候选，受并发版本保护 |
| POST /projects/{project_id}/reviews | 执行规则和语义检查 |
| PATCH /projects/{project_id}/issues/{issue_id} | 记录处理结果及依据 |
| POST /projects/{project_id}/exports | 固定snapshot，创建草稿/定稿导出任务 |
| GET /projects/{project_id}/exports/{export_id} | 状态、manifest、下载入口 |
| GET /projects/{project_id}/exports/{export_id}/artifacts/{artifact_id} | 鉴权下载/短期授权，重新核对来源权限 |
| GET /projects/{project_id}/jobs/{job_id} | 任务快照 |
| POST /projects/{project_id}/jobs/{job_id}/commands | cancel/retry/resume；状态与身份检查 |
| GET /projects/{project_id}/events | SSE/after_seq，断线续传 |
| GET /projects/{project_id}/cards | 返回可回读的卡片快照 |
| GET /projects/{project_id}/source-preview | 鉴权返回file/block定位的预览描述 |

## 3. 错误约定

错误体包含 code、message、request_id、details（不带秘钥或原文敏感片段）。

| HTTP | code | 客户端处理 |
|---|---|---|
| 401 | UNAUTHENTICATED | 重新认证，不创建新项目 |
| 403/404 | FORBIDDEN/NOT_FOUND | 按宿主隔离策略，不泄露对象存在性 |
| 409 | REVISION_CONFLICT | 刷新快照，展示变化，保留本地输入 |
| 409 | STALE_INPUT | 输入已失效，重新选择当前版本 |
| 409 | IDEMPOTENCY_CONFLICT | 客户端修复key/body组合 |
| 422 | INVALID_SCOPE | 展示错误标段或跨项目ID |
| 422 | FINALIZATION_BLOCKED | 展示阻断问题及定位 |
| 413/415 | FILE_TOO_LARGE/UNSUPPORTED_MEDIA | 上传前后都解释限制 |
| 429/503 | RATE_LIMITED/DEPENDENCY_UNAVAILABLE | 有界退避；保留任务ID |

## 4. 项目事件

事件持久化后再发流；同项目seq严格递增。字段：event_id、seq、project_id、event_type、entity_type、entity_id、entity_revision、occurred_at、payload。事件种类包括 job.updated、analysis.ready、selection.updated、card.updated、section.candidate_ready、dependencies.stale、review.completed、export.ready。

前端按seq去重并按实体revision拒绝旧更新；断线携带Last-Event-ID或after_seq重放（两者同时存在须相等，不等返回400）。超过事件保留窗口返回410 EVENT_CURSOR_EXPIRED，先GET项目快照，再从latest_event_seq接流。SSE认证沿用带认证头的fetch流；若改原生EventSource需使用宿主安全cookie或短期一次性流票据，不能将长期token放URL。

## 5. 卡片动作

card_id、card_type、project_id、entity_ref、revision、status、payload、allowed_actions由后端生成。allowed_actions控制显示但不能替代服务端授权。过期卡片可查看历史，提交返回409并定位到当前卡片。用户动作通过具体业务API提交；成功后再记录系统事件消息，禁止客户端自行发送“已确认”的假卡片。

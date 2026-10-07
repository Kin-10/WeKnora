# 03 数据、版本与一致性

## 1. 标识与通用字段

对外ID为不透明字符串；新增业务ID可用UUID。tenant_id、user_id、session_id在数据库内沿用锁定仓库原生类型。schema_reference.sql使用text模拟边界，目的是审阅关系，不能直接替换宿主类型。时间存UTC，展示按项目时区；金额用十进制定点及货币，日期与时间戳分型。

所有业务实体至少包含 tenant_id、project_id（项目子对象）、id、created_at、updated_at；可变聚合含 revision。项目成员权限和源知识权限分别校验。外键或服务层需确保子实体属于同一租户与同一项目。

## 2. 表与职责

| 表 | 关键字段/关系 |
|---|---|
| bid_projects | owner、agent_id、title、lifecycle、revision、active_document_set_id |
| bid_project_members | project/user/role；复用宿主成员体系也可 |
| bid_session_links | tenant/session唯一绑定project；清空消息不删除project |
| bid_source_files | role、object_key、sha256、mime、size、version、supersedes_file_id |
| bid_document_sets | 不变file-version集合、revision、是否当前有效 |
| bid_analysis_runs | document_set、parser/prompt/model版本、coverage、status |
| bid_analysis_corrections | analysis、受控字段patch、理由、作者与输入版本 |
| bid_source_blocks | analysis、block_id、order、text/table/image、locator、hash |
| bid_lots | analysis、lot_code、name、aliases、候选范围及来源 |
| bid_requirements | analysis、kind、verbatim、normalized、scope_mode、status、来源 |
| bid_requirement_lots | 要求对多个标段的显式适用关系 |
| bid_selections | revision、lot_ids、analysis_run_id、confirmed_by/at |
| bid_confirmations | requirements/outline/facts等确认记录与输入指纹 |
| bid_sections | lot、parent、order、strategy、当前content_revision |
| bid_outline_versions | 不变目录树及requirement-section映射、确认版本 |
| bid_section_versions | 不变AST、作者类型、人工锁定、生成输入指纹 |
| bid_requirement_sections | 要求与响应章节的多对多关系 |
| bid_fact_versions | schema化facts、来源、确认记录、lot/shared范围 |
| bid_materials | 真实资料类型、主体、范围、组成页、来源版本、审核/效期 |
| bid_material_versions | 稳定material_id下的不变来源快照与组成、版本号 |
| bid_material_bindings | requirement/section/material、适用性结论、证据快照 |
| bid_dependencies | from_type/id/version → to_type/id/version，失效理由 |
| bid_review_issues | kind、severity、位置、依据、status、resolution |
| bid_jobs | kind、status、input_fingerprint、attempt、lease、checkpoint、error |
| bid_events | project内递增seq、type、entity_ref、业务revision |
| bid_cards | 持久卡片信封、引用对象、revision与状态 |
| bid_outbox | 待投递队列/事件消息，delivery_status、next_attempt |
| bid_idempotency | actor/route/key、request_hash、resource_ref、result |
| bid_export_snapshots | selection/facts/requirements/outline/contents/materials版本集合 |
| bid_export_plans | 标段分册计划及版本、依据与确认 |
| bid_exports | snapshot、mode、artifact清单、manifest、layout_review状态 |
| bid_audit_logs | actor、action、资源版本、reason、trace_id |

## 3. 要求范围模型

scope_mode取 all_lots / explicit / unresolved。explicit必须至少有一个requirement_lots关联；all_lots在每个本次确认标段中生效；unresolved不参与“完整满足”结论。共通条款无需复制成多条，否则修改容易遗漏。

涉及“仅第一包适用”的文本不能因位于通用章节就设all_lots。source_locator保留原件file_id/version、block_id、可选page_index/bbox、表格sheet/cell等。无法定位页码时page_index=null，使用可验证的段落/表格定位；不得编造页码。

## 4. 一致性与版本

- 解析形成新analysis_run，旧结果不覆盖；人工修正通过结构化patch和审计保存，重跑时提供迁移建议。
- 选择形成新selection_revision，已取消选择标段的内容保留，默认从本次导出范围移除。
- facts只能以候选→用户确认→不可变版本的方式生效；修改产生新版本。
- 章节版本记录analysis/selection/requirements/outline/fact/material/prompt/model的输入指纹。
- 人工版本与AI候选并存；应用候选时检查 expected_content_revision。
- export_snapshot不可变；版本变化只产生新快照，不回写旧产物。
- 原件文件哈希用于去重和审计，不代表有访问权限，也不能跨租户泄露“已有该文件”。

## 5. 状态建议

项目lifecycle仅取 active/archived；阶段进度通过各对象状态推导，避免一个巨大项目枚举无法描述多标段并行进度。

任务：queued/running/waiting_input/succeeded/failed/cancelled/superseded。
解析：queued/parsing/extracting/validating/ready/partial/failed。
章节：empty/drafting/generated/edited/reviewed/stale；锁定为独立flag。
问题：open/resolved/accepted_risk；事实缺失与明确不满足使用不同kind。
导出：queued/building/validating/ready/failed/revoked；定稿与草稿为mode。

## 6. 依赖失效

澄清或事实变更→定位依赖边→标记相关章节、响应表、附件绑定及审核为stale→暂停旧快照定稿。没有明确依赖的潜在影响用semantic_review_required列入待核对。失效不会自动改写人工段落。过时worker结果存为历史候选，并且不能成为当前版本。

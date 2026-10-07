# 数据模型与迁移

所有表按实际租户模型加入 tenant_id / workspace_id（若存在）、created_by、时间戳及软删除规则；主键和类型遵循仓库约定。外键或应用约束必须防跨项目/跨租户引用。迁移提供 up/down 与索引，生产回滚方案需明确不可逆数据转换。

| 表 | 关键字段 | 约束 |
| --- | --- | --- |
| bid_projects | id, owner_scope, name, status, version | 权限与乐观锁 |
| bid_project_documents | project_id, role, knowledge_id, file_hash, parser_version, uploaded_at | 原件与澄清版本 |
| bid_packages | project_id, code, name, selected | 项目内 code 唯一 |
| bid_requirements | project_id, package_id nullable, type, mandatory, response_required, evidence_required, source_doc_id, source_locator, source_text, status | 共同要求用空 package_id；保留原文 |
| bid_outline_nodes | project_id, package_id, parent_id, position, title, origin, locked, version | 树序和来源 |
| bid_project_facts | project_id, package_id nullable, key, value_json, status, source_ref, confirmed_by, confirmed_at, version | 仅 confirmed 可用于硬事实 |
| bid_materials | project_id, source_knowledge_id, kind, subject_ref, scope, validity, review_status | 资料主体和效期 |
| bid_material_bindings | material_id, requirement_id/section_id, match_status, approved_by | 显式审核 |
| bid_sections | outline_node_id, content_ast, content_version, edit_state, generation_revision | 人工版本保护 |
| bid_section_sources | section_id, source_file_hash, source_version, source_locator, quote_snapshot, knowledge_id, chunk_id | 定稿可追溯 |
| bid_review_issues | project_id, requirement_id, section_id, severity, code, evidence, status, resolved_by | 问题可复核 |
| bid_jobs | project_id, kind, idempotency_key, state, progress, attempts, error_code | 队列可恢复 |
| bid_exports | project_id, revision, output_file_ref, manifest_hash, review_snapshot, created_by | 成册审计 |

避免把 HTML 当唯一正文格式。定义版本化 Document AST：heading、paragraph、table、image/evidence、page_break；保留编辑器原始内容的转换测试。来源 locator 支持页、章节、表格单元格或图片区域；无法确定时标 `unresolved`，不伪造页码。

迁移顺序：项目/文件/包段 → 要求/目录/事实 → 材料/章节/来源 → 审核/任务/导出。每次迁移均测空库升级和已有库升级。

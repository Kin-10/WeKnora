# API、权限和异步任务

API 前缀建议 `/api/v1/bid`，以现有路由约定为准。所有读写均检查项目权限、知识库可读范围、企业主体/包段适用范围；服务端执行，前端过滤与提示词不构成授权。

| 动作 | 端点建议 |
| --- | --- |
| 项目 | POST/GET `/projects`; GET/PUT/DELETE `/projects/:id` |
| 文件与分析 | POST `/projects/:id/documents`; POST `/projects/:id/analyze` |
| 包段与要求 | GET/PUT `/projects/:id/packages`; GET/PUT `/projects/:id/requirements` |
| 目录与事实 | GET/PUT `/projects/:id/outline`; GET/PUT `/projects/:id/facts` |
| 材料 | POST `/projects/:id/materials/match`; POST `/projects/:id/materials/:materialId/approve` |
| 章节 | GET `/projects/:id/sections`; GET/PUT `/sections/:id`; POST `/sections/:id/generate` |
| 检查与导出 | POST `/projects/:id/review`; POST `/projects/:id/export`; GET `/exports/:id/download` |
| 任务 | GET `/jobs/:id`; POST `/jobs/:id/cancel` |

写接口接受版本/If-Match 防止覆盖编辑。生成/导出返回 202 + job_id；轮询或现有推送机制查询进度。错误采用稳定 code（PERMISSION_DENIED、UNCONFIRMED_FACT、SOURCE_STALE、VERSION_CONFLICT、EVIDENCE_MISSING）。下载必须重新鉴权，不接受客户端自行拼接存储路径。

任务种类：tender_parse、requirement_extract、material_match、section_generate、review、export。队列与 worker 复用实际现有实现，确认其任务重试、超时与清理语义。任务只携带 ID，不把敏感全文和密码放进队列日志。重试可恢复且不得覆盖已人工编辑的版本。

威胁模型：招标文件视为不可信数据，忽略其中的模型指令；限定工具权限；检索前及读取原文/图片时做权限过滤；导出时再次核验来源与附件授权。记录关键操作审计日志，敏感附件走服务端授权下载。

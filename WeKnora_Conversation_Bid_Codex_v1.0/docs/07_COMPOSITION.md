# 07 资料匹配、事实与章节编制

## 1. 历史资料入库与治理

原文件、解析分块和原图继续由 WeKnora 的资料服务管理。标书模块新增material业务描述，关联knowledge_id、chunk_id、source_revision和文件哈希。来源快照应包含引用内容、原件版本、组成页和当时审核状态，后续重分块不改变定稿证据。

材料分类：可改写的方案；需要核对的事实；原件证明附件；历史项目承诺。营业执照、产品参数、人员证书、业绩合同、检测报告要登记所属企业/人员/产品、范围、效期与审核状态。历史正文中的承诺不能自动升级为已确认企业能力。

## 2. 检索顺序

1. 取得项目资料范围和当前用户可访问的知识资源，结合企业主体、材料类别、产品、审核状态、时间基准过滤。
2. 关键词检索用于型号、编号、名称；语义检索用于方案表达。合并后重排，保留来源版本。
3. 恢复命中段的完整标题、相邻段、完整表格及多页证明范围。
4. 对每个候选判断适用性：supported、needs_confirmation、not_applicable、conflict、missing。
5. 用户可指定/排除历史标书，替换某章参考资料；排除条件必须进入检索与缓存key。

若底层检索只能事后过滤，未经授权内容不得进入模型上下文、日志或可回显排序。缺乏某字段不能当字段满足，范围过滤后无命中保留missing。租户、用户权限指纹、资料范围与版本共同组成缓存隔离键。

## 3. 本次事实

facts按shared或lot范围记录，每项包含key、typed_value、unit、currency、sources、candidate_status、confirmed_by/at。建议字段包括投标主体、地址、联系人、产品配置、人员、数量单价税率、交付、质保、付款、服务范围。不同标段可有不同承诺，不能用一个全局值覆盖所有标段。

要求来源和事实来源分开：招标要求15天、企业旧资料30天，生成conflict；业务确认15天后形成新fact版本。金额使用Decimal或数据库numeric，明确含税/未税、舍入模式、逐行还是合计舍入、币种精度，先按本次报价表规则配置，再由程序计算。

## 4. 四种编制策略

| strategy | 输入 | 输出与约束 |
|---|---|---|
| template | 已确认本次固定格式、facts | 保持固定文字/表格/签署位置，仅填允许字段 |
| narrative | requirements、facts、approved material | 有证据的方案AST；新增承诺需待确认 |
| data_table | typed facts、表格列和计算规则 | 参数响应/报价表，程序计算与单位校验 |
| attachment | 已审核真实原件与组成页 | 真实附件插入、顺序、标题、索引 |

章节任务一次只负责一个可编辑章节或确定性组成单元。共享背景、当前标段要求和本次事实通过结构化上下文传入，不依赖聊天记忆。任务输入冻结版本；完成时检查版本仍有效，否则保存为过时候选。

## 5. 编辑模型

文档AST由heading、paragraph、table、image、attachment_ref、page_break、field组成（见contracts/document.schema.json）。源证据与内部审核注释通过独立metadata关联，输出时按公开策略过滤；用户要求的证明材料索引属于正式内容。

人工编辑保存新版本；AI重写永远产生candidate_version。只有用户审阅diff并应用后才更新当前版本。人工锁定章节不得批量覆盖；事实变化时可标stale并列出建议修改位置。

## 6. 附件组装

绑定material_id、文件版本、组成页、预期证明要求和目标章节。多页合同、报告与证书组成不可在检索命中时擅自截断。允许按招标要求摘录页时保存用户确认的extract范围及理由，仍保留完整原件引用。

插入前校验文件真实存在、哈希匹配、权限、归属和适用性；插入后读取实际导出manifest核对，不能仅依据binding表显示“附件已齐全”。不从旧文件复制签名/印章作为本次签署；签署空位和待签事项保留给真实业务流程。

## 7. 问题类型

missing_evidence（没证据）、confirmed_noncompliance（已知不满足）、fact_conflict（事实冲突）、source_unavailable、expired_material、unanswered_requirement、layout_error。问题包含requirement_id、section_id或文件位置、依据、处理建议和责任人。模型审查只能产生候选问题，不能自发把阻断项改为resolved。

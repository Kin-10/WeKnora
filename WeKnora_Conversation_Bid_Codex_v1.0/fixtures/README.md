# 合成样例与使用边界

全部资料为开发合成，未包含真实招标文件或真实证明附件。

- tender_multi_lot.txt：3包，共通条款、多包共享条款、歧义条款、固定格式与报价规则。
- source_blocks.json：手工标注的解析器适配输出样例，b01等标签只是fixture定位符，不表示实际解析器会返回这些ID。
- analysis.valid.json：结构与归属标注样例；ready表示提取完成，不表示歧义已解决或允许定稿。复合条款这里保留父条款，生产应拆子要求并保留关联。
- selection.expected.json：选择包二后应纳入/排除/待定的要求集合。
- tender_single_lot.txt：单包入口样例；clarification.txt：包二变更与未知范围澄清。
- adversarial.txt：上传文件内指令不可改变系统行为。
- fact_conflicts.json、materials.json：冲突和材料元数据，无真实资质图片，不能用于正式附件。
- document.valid.json：未确认事实下的内部草稿，不允许该fact_version用于正式章节生成或定稿。
- quote.expected.json：金额计算golden结果。
- negative_cases.json：产品实施后需落实的拒绝/恢复测试，不代表已经跑过这些产品行为。

P03/P08另准备经授权的真实DOCX、PDF、扫描件、表格和多页附件样本，验证解析与版式。合成文本样例不能替代真实解析器/Word导出联调。scripts/validate_pack.py验证包内结构、契约引用和样例一致性，不启动WeKnora、不调用模型、不验证产品实现。

# 09 持久任务、并发与恢复

## 1. 命令处理

身份鉴权→同租户同项目引用检查→幂等检查→expected_revision检查→业务前置条件→事务写业务/审计/事件/outbox→提交→后台投递。队列投递失败不能丢失任务，outbox重试；不把数据库事务跨越模型或对象存储网络调用。

文件上传先流式写临时对象并校验哈希，再建立持久对象引用和DB记录；DB失败则回收未引用临时对象。成功响应意味着对象可读取且有持久引用。并发重复上传由幂等键和项目内hash/version规则去重。

## 2. Worker协议

任务以job_id领取，租约与heartbeat防止永久running。每一步有checkpoint（范围、已产物、版本）；至少一次投递下，阶段结果写入使用唯一键和CAS防重复。外部调用可能超时且结果未知，记录attempt与provider请求ID；不能宣称exactly once模型执行。落库和产物发布必须幂等。

重试仅针对网络/限流/暂时依赖错误；文档损坏、权限不足、输入过期、确认缺失不盲重试。默认最多3次指数退避加随机抖动，实际参数可配置。取消在步骤边界检查；即使模型已返回，取消或superseded任务也不能发布当前结果。

## 3. 冻结输入与CAS

input_fingerprint为canonical JSON哈希，包含文件集合、analysis、selection、requirements、outline、facts、materials、prompt、model配置及parser版本。温度等影响生成的设置也记录。提交结果时再次检查依赖当前状态；版本落后则任务superseded，结果可保留但不应用。

卡片revision、project revision和content revision职责不同：project revision保护聚合命令；section content_revision保护正文保存；card revision用于UI回读，不作为唯一业务并发锁。示例契约中的expected_revision默认指项目聚合版本，章节接口另外必传expected_content_revision。

## 4. 等待人工输入

waiting_input不持有数据库锁、HTTP连接或worker租约。缺口处理/确认作为独立命令写入，再resume创建或继续具体任务。用户没有确认就不得基于超时默认承诺。一个标段待输入不阻塞其他无依赖标段的资料准备和候选草稿。

## 5. 状态恢复

浏览器刷新→项目snapshot→cards→events(after_seq)。worker重启→回收过期lease→按checkpoint恢复。消息删除→不影响项目真相；项目归档→禁止新写任务，已运行任务按策略完成保存或取消，不能发布到已归档项目。

## 6. 澄清与变更

源文件集合/标段范围/事实/模板变更都走新的revision。依赖图按边传播stale；旧confirmations保留审计但不再满足当前门槛。权限撤销会使相关绑定暂不可用，并使定稿下载重新检查；用户已经下载到本地的文件无法通过系统追溯撤回，产品不做相反承诺。

## 7. 性能与成本

解析按文档范围分批，章节生成并发受租户和模型预算限制；基于相同原件和解析版本可复用物理解析结果，但要重新授权。记录队列等待、各阶段耗时、失败率、token、导出耗时。首期验收采用阶段实测，不承诺固定百页解析秒数。可先设API非长任务p95<1秒作为测试环境目标，报告数据规模与硬件。

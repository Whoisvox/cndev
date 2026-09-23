# gap
1. 分析increase外推
- 打30个/ping请求，其中20个带token，10个不带token。由于我当前日志级别为WARN，因此20个带token的请求不会写日志。日志来自10个不带token，鉴权中间件和可观测中间件。
- 压测器自爆： 30（20*200, 10*401)
  原始计数器： 30
  increase[3m]： 0 - 新pod序列born at 10，首末一致
  status label: 401

# M4-3
## T1 LogQL
1. {app="logsvc"} 看到日志行20条，10条来自鉴权中间件(msg: "Authorization header require", 无status)，10条来自可观测中间件(401,msg:request)，其实总共是24条，4条是服务启动时打的4条info
2. {app="logsvc", level="WARN"} 20条，和上面一样
3. {app="logsvc"} |= "Authorization" 行过滤，10条，只有鉴权中间件的10条
4. - {app="logsvc"} | json | status="401" Query Inspector中total request time 40ms左右
   - {app="logsvc", status="401"} 100ms左右，确定有索引？，不是应该比上面的过滤更快才对吗
     tips: 我上面的查询时间范围是最近6小时，因为我最后一次测试是11点20左右的打30个请求那次，我把时间限制到11点20前后半小时，过滤和索引都差不多，40ms左右，偶尔出现过滤有0.1ms的data processing time
5. sum by (status) (count_over_time({app="logsvc"}| json [24h])) 计数一开始为4， 两个服务读取配置和打的info日志 后面是中间件打的10个401，后面是总计的14个
6. {app="logsvc"} | json | duration_us > 100 鉴权中间件的日志没有duration_us消失了，可观测中间件中duratuion <= 100的也消失了

## T2 detected_level（已用 API 纠正：它确实存在）
- 误判：在 Grafana Explore 的 label 浏览器里只看到一个 level，以为没有 detected_level。
- 实测反驳：直接查 Loki API 单条日志的 stream labels，level 和 detected_level **并存**：
  `{"app":"logsvc","level":"WARN","detected_level":"WARN","status":"401","path":"/ping",...}`
  Explore 左侧 label 浏览器只显示常用子集，看不到全部 stream label——「UI 说没有」不等于「没有」，信 API 不信 UI。
- 来源：level 是我在 Alloy 里 promote 的；detected_level 是 Loki 3.4+ 摄入时自动检测的。两者同语义、双来源。
- **决策（选项 C：都保留 + 文档声明）**：limits_config 里没有现成开关能关掉自动检测（查了 Loki 3.6 config 参考，确认无法简单关闭）。既然关不掉，就文档化：**level 为权威字段**（我自己 promote 的，可控、和代码一致），detected_level 仅作 Loki 自带的冗余参考。查询/告警一律用 level，不用 detected_level。
  - 代价：每条 stream 多一个 label（基数 +1），可接受。
  - 教训：「确认无法关闭后文档化」也是合法工程决策，不必为了消除冗余硬改。

## T3 Log Alert Trace（全链贯通 + 一个假恢复的教训）
**五环证据链**（2026-09-22）：
FAIL_RATE=100 → 30×500 → Loki 30 行 ERROR → ruler FIRING → AM 按 severity=ticket 分组路由到 webhook-ticket
→ alert-receiver `14:27:46 WARN firing`（severity=ticket 走 default 分支=WARN，page 才 ERROR，M2 的路由设计在日志侧复用）
→ `14:31:46 INFO resolved`。两套告警源（Prometheus 指标 / Loki 日志）汇入同一条通知链，实证完成。

**踩坑记录**：
- ruler API 路径：auth_enabled=false 时是 `/prometheus/api/v1/rules`（不带 fake 租户段）；我一开始按多租户版记的 `/prometheus/fake/...` 返回 404。手法：候选路径挨个 `curl -w "%{http_code}"` 试。
- ruler 加载不到规则（groups:[]）：sidecar 已把文件同步到 `/rules/fake/`，但 ruler 的 `storage.local.directory` 没配 → 它每分钟轮询一个空目录。修法：`loki.rulerConfig.storage.local.directory: /rules`（指向租户目录的父目录，ruler 自己往下找 fake/）。
- CrashLoopBackOff `mkdir /rulers: read-only file system`：directory 误写成 `/rulers`（多一个 s），而 loki 容器 `readOnlyRootFilesystem: true`，根文件系统只读，mkdir 被内核拒绝 → fail-fast 崩溃。**三个好消息**：① 只读根文件系统是正确安全姿态（拦住笔误也拦得住勒索脚本）；② Loki 选择响亮崩溃而非静默失效（对比那些 health:ok 却查无数据的坑）；③ 报错一行含完整因果链。改回 `/rules` 即愈——该目录是 emptyDir 挂载点，已存在，不触发只读检查。

**🔴 假恢复（本环节最值钱的一课）**：
resolved 不代表故障修好了。我收到 resolved 后隔天（09-23）实测带 token 打一发 → **仍返回 500**，FAIL_RATE=100 根本没关。
告警「恢复」的真正原因：**我停止了打流量**。`count_over_time({level="ERROR"}[5m])` 数的是窗口内的错误**行**，没有请求就没有新 ERROR 行，窗口滑过 → 计数归 0 → 告警 resolved。**系统烂着，告警说没事了。**

这和 M3 Q3「告警对沉默失明」是同一个洞的两种表现：

| | 指标告警(FastBurn) | 日志告警(ErrorBurst) |
|---|---|---|
| 零流量时 | 燃烧率 NaN + 闸门不通过 → 沉默 | count_over_time=0 → **假 resolved** |
| 本质 | 同一个盲区：没有流量，两套系统都看不见故障 |

两套独立告警系统共享同一个盲区 → 这就是 T4 `LogsvcNoTraffic` 必须存在的理由：它不是第三条普通告警，是给「另外两条都瞎了的场景」装的灯。（生产还有一层黑盒探测 blackbox probe 从集群外打真实请求，连「服务整个死了、连无流量告警都发不出」都兜住——stage5/6 范畴。）

**实验账单**：可用性预算剩余 −401.7（28d 可用性 59.7%），延迟预算剩余 0.78。三轮实验、三次跳水、一条只降不升的预算线——28d 滚动窗口「伤害记满一个窗口」纪律的三轮实感。synthetic 流量条款已补进 slo.md 第 5 节。

## T4 LogsvcNoTraffic（待部署）
- 立项理由：见 T3 假恢复——FAIL_RATE=100 但零流量那段时间，服务「Ready 但没人访问」，燃烧率告警和日志告警**同时失明**。这条告警专门守这个盲区。
- 规则（Prometheus alerting_rules.yml，**当前还没部署**，cm 里 NoTraffic 出现 0 次）：
  ```yaml
  - alert: LogsvcNoTraffic
    expr: sum(rate(http_requests_total{path="/ping"}[1h])) == 0
          and on() count(kube_pod_status_ready{namespace="godev",pod=~"logsvc.*",condition="true"} > 0) > 0
    for: 5m
    labels:
      severity: ticket
    annotations:
      summary: "logsvc 存活但 1h 零业务流量"
      description: "Pod Ready 却无 /ping 流量：要么服务真没用户，要么流量/采集断了，而所有基于错误率的告警此刻全部失明"
  ```
  设计点：① `== 0` 反向用闸门（M2 的 `>5` 是防小样本误报，这里反过来报「彻底没样本」）；② `and on()` 要求 Pod Ready——发版/缩容到 0 期间不误报；③ severity=ticket 不 page——零流量在 dev 是常态，在生产才需要人看。
- 验证：FAIL_RATE 关掉后，服务回到零流量稳态，等 1h 窗口 + for 5m，这条应该 FIRING（无需注入故障，当前就是活体样本）。
# 思考题(M3 三道 + M4-3 一道)

## M3-Q1 预算 −436/−401 然后呢?实验流量怎么办?
- 「冻结发布」不能机械执行:烧预算的是实验不是事故。处理三层:短期接受读数不回滚(滚动窗口只读,伤害记满 28d 自然滚出);中期 Grafana annotation 标注实验时段;长期让实验流量从源头不进 SLO。
- 方案已写进 slo.md 第 5 节:目标态选 **方案 B 独立路径 /internal/ping**(零标签代价,路由层天然分区,synthetic 可有独立 SLO);过渡期用方案 C(事后标注 + 四条纪律,含「实验完立刻关 FAIL_RATE」——T3 假恢复就是违反这条的现行犯)。

## M3-Q2 面板读 recording rule 的代价?
三个代价:① 新鲜度——5m 评估一次,面板最多落后 5 分钟,告警(1m 评估)可能比看板先知道;② **维度丢失(最重要)**——rule 里 sum() 不带 by 抹掉全部 label,「哪个 pod 在失败」查不了,要回原始序列;③ 没有历史——序列从规则部署时刻才开始存在,不回溯(和「桶 schema 变更」是兄弟教训:预计算序列的生命起点=规则部署时间)。
不可接受的场景:**事故响应下钻**。凌晨三点要回答「哪个 Pod、哪个状态码、何时开始」,recording rule 一个都答不了。所以生产标配:SLO 面板读 rule(口径一致)+ 原始下钻区直查原序列(能破案)——面板④⑤和⑥⑦的分工就是这个理论。

## M3-Q3 图和告警各自的盲区?
- 图的盲区:需要有人在看;尺度盲区(500 倍尖峰在 28d 轴上是一根针);**空洞盲区**(NaN 渲染成空白,空白≠健康);渲染会撒谎(smooth 插值画出过 2000、percentunit 差 100 倍、or 括号错位显示 1366600%)。
- 告警的盲区:只能看见被编码的东西(延迟违约/Pod 掉副本没写规则就不响);**对沉默失明**(闸门>5rps 时零流量永不触发,彻底死掉比半死不活更安静);永远滞后(1h 窗口);告警疲劳。
- 结论:告警管「没人看的时候」,图管「告警没编码的东西+响了之后破案」,两者读同一份 rule 但必须都存在。
- **M4-3 追加实证**:T3 假恢复给「告警盲区」添了最尖锐的一条——日志告警在无流量时不光沉默,还会主动发 resolved 撒谎。指标告警至少只是沉默(NaN 比较恒 false),不会说「好了」。

## M4-3 同一次实验 ErrorBurst(日志)和 FastBurn(指标)都响,值班先看哪个?
先看**指标告警(FastBurn)**。三个理由:
1. **可信度**:指标是全量计数(每个请求必然 Inc),日志是采样的(LOG_LEVEL 决定什么被记录)。ErrorBurst 依赖「ERROR 行被写出来且被采集」,LOG_LEVEL 一改就失明;FastBurn 只依赖计数器,不受日志策略影响。T3 实测过:ErrorBurst 会假 resolved,FastBurn 在故障期间(有流量时)不会自己闭嘴。
2. **定量**:FastBurn 的值就是燃烧率(500 倍 = 0.5/0.001),直接告诉你预算烧速和「还剩多少时间」;ErrorBurst 只说「ERROR 行数 > 5」,不含速率语义。
3. **口径**:FastBurn 和 SLO/看板同源(recording rule 单一事实源),值班看到的数和复盘的数是同一套。
但**不能抑制掉日志告警**:两者盲区互补——FastBurn 对「没进指标的错误」(panic 被吞、第三方库异常、指标埋点漏了的故障)失明,ErrorBurst 恰好能看见文本模式。正确姿势不是二选一,是 AM 的**inhibit_rules**(抑制规则):FastBurn firing 时抑制 ErrorBurst 的通知(同一个故障别让值班收两条),但 ErrorBurst 单独 firing 时(指标没编码的错误)照常送。这是 M2 前置知识里提过一嘴的那个词——抑制,现在有了用武之地。stage5 可落地。

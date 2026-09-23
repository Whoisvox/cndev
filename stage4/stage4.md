# Stage4 收官:SRE 核心 —— 从「有监控」到「会运营」

> 环境:真实集群 v1.32.9,ns `godev`,自建 Prometheus(helm chart prometheus-29.19.0)+ Grafana 13.1.1 + Alertmanager + Loki 3.6.11 + Alloy v1.18.0。被测服务 logsvc(2 副本,NodePort 30222,镜像演进到 v12)。
> 数据见 `stage4/info.md`,SLO 合同见 `stage4/slo.md`,看板见 `stage4/slo-dashboard.json`,告警接收器见 `stage4/cmd/alert-receiver/`。
> 收官日期:2026-09-23。

监控回答「现在发生了什么」;运营回答「我能容忍什么、何时动手、叫醒谁」。stage1 把想法写成靠谱代码,stage2 让代码自我表达,stage3 让服务在被杀的环境里保持体面——**stage4 给它装上决策系统:SLO、error budget、两套告警、日志聚合**。每个知识点都挂在一次真实事故上。

---

## 0. 里程碑总览(每一关的猎物)

| M | 主题 | 撞到的 bug / 挖出的机制 |
|---|---|---|
| M0 | SLI/SLO/error budget | 桶 schema 混杂伪造 22% 延迟;counter/histogram 缺口 9836;retention(15d)<窗口(28d) 悄悄改写 SLO;服务零流量 → SLO 是「关于过去的合同」 |
| M1 | Recording Rules | `--set-file` 把 `recording_rules.yml` 拆成 `recording_rules/yml`;预算公式写反(÷0→Inf);拼写 `availbility`/`avalbilty`;**空向量 ≠ 0**(`or vector(0)` 只包分子);health:ok 却查无数据 |
| M2 | 告警 + Alertmanager | 告警选择器 `app=`/`code=` 两个 label 都不存在;没用 recording rule/燃烧率;`alertmanagerFiles` key 在你的 chart 版本根本不存在;镜像陈旧致 `severity=""`;流量闸门防小样本误报 |
| M3 | SLO Dashboard | 默认时间范围 now-5m;阈值 percentage vs absolute;smooth 插值画出物理不可能的 2000;面板⑦ `or` 优先级错位显示 1366600%;NaN 空白 ≠ 健康 |
| M4-0 | 装 Loki | `replication_factor:3` vs 单实例 → 写入 500「at least 2 live replicas」;**`/ready` 说 ready 但写不进去**(ready≠usable) |
| M4-1 | Alloy 日志管道 | 「看不到 logsvc」其实是零流量(canary 刷屏误导);CRI 前缀;`mounts.varlog` 命门;标签基数决策 |
| M4-2 | 日志 vs 指标对账 | 30/31/10 三方不一致;`increase` **出生盲区**(序列 born at 10 → increase=0);中间件按 path 而非 status 判级 → 500 在 warn 下隐身;**recovery 包在 observability 外层 → panic-500 对日志和指标双重失明(SLO 对 panic 是瞎的)** |
| M4-3 | 日志告警 + ruler | ruler API 路径不带 fake;`storage.local.directory` 没配 → 轮询空目录;`/rulers` 笔误 + 只读根文件系统 → CrashLoop;**假恢复**(FAIL_RATE 没关,停流量 → 告警 resolved 但故障还活着) |

---

## 1. SLO 三件套(M0)

**SLI** = 坏事件/总事件的比例(永远是比例,不是绝对值)。**SLO** = 「某 SLI,在某窗口内,达到某目标」三要素齐全。**Error budget** = 1−SLO,是整个 stage4 的灵魂:

> 预算是**货币**,不是惩罚。发布消耗它,稳定运行积攒它。有余量→可发版;烧完→冻结发布还债。它把「开发想上线 vs SRE 想稳定」这场永远吵不完的架,变成一道查余额的算术题。

logsvc 的两个 SLO(数字全部追溯到 stage3 实测):
- **可用性 99.9%**:28d×24h×0.1% = 40.3 分钟「全坏等价时长」。5xx 算坏,401/404/405 不算(客户端错误,正确拒绝是正常行为),未来 429 必须算(服务端容量问题)。
- **延迟 99%@5ms**:阈值 5ms 不是拍的,是被三个约束逼出来的——桶边界(只能选桶上有的值)、schema 一致性(只有 le=0.005 在所有历史桶方案里都存在)、实测锚点(服务端 P99≈249µs,留 20 倍余量)。99.9% 在当前数据下已违约(0.29%>0.1%),SLO 不能签一份已经破的合同。

**预算归一化**:`(SLI − SLO目标) / (1 − SLO目标)` → 1=满,0=烧光,负=超支。不同目标的 SLO 从此能画在同一张图、用同一个告警阈值。

---

## 2. Recording Rules:SLO 的工程形态(M1)

为什么需要:① 成本(28d 表达式被 N 个消费者各重扫一遍 vs 算一次存序列);② **一致性(最重要)**——看板和告警若各算各的,会出现「看板红了告警没响」,复盘时两边数据对不上;③ 复杂表达式拆解(燃烧率告警由多个中间量组合)。

命名约定 `level:metric:operations`(`slo:logsvc_availability:ratio28d`)。**recording rule 改名 ≈ 指标换名**(M0 桶 schema 教训的同款)——趁没数据时改免费,攒了数据再改就是第二次 schema 事故。

同组内规则按书写顺序串行评估,后面的可引用前面刚算出的(budget_remaining 引用 ratio)。**`interval` 按数据新鲜度需求设**:28d 大窗口规则放宽到 5m,分钟级原料用 1m。

---

## 3. PromQL 三态语义(贯穿全 stage 的母题)

M1 发现「规则 health:ok 却查无数据」,根因是**空向量**:服务太健康,28d 内没有任何 5xx 序列,`sum(rate(...5xx...))` 作用于零条序列返回**空**(不是 0),于是 `1 − 空/x = 空`,recording rule 拿到空就什么都不写。

修法是分子加 `or vector(0)`,但**只包分子不包分母**,因为三种沉默语义不同:

| 状态 | 含义 | 修法 |
|---|---|---|
| **空**(序列不存在) | 没有载体/超保留期/从未发生 | 分子 `or vector(0)` 补成 0 |
| **0**(序列在但平坦) | 有载体无事件 | 正常值 |
| **NaN**(0/0) | 序列活着但分母为 0 = 无流量 | 分母**不**补——没流量 ≠ 健康,诚实让它空 |

这个三态在 stage4 反复出现:M3 面板⑦无流量时空白(不是 0 平线)、M4-1「看不到 logsvc」其实是零流量、M4-3 假恢复。**「沉默有三种,别当同一种」**是本 stage 最该带走的认识论。

**retention 是 SLO 合同的一部分**:窗口 28d 就必须保留 ≥28d,否则 SLO 数学上不可测量,而且保留期会**悄悄改写历史成绩**——M0 那 0.29% 超阈值请求(来自 stage3 CPU 节流时代)被 15d 保留期吃掉后,延迟预算从「已耗 29%」自动变成「剩 92%」。改 35d 后修复。

---

## 4. 燃烧率告警:为预算报警,不为症状报警(M2)

**燃烧率** = 当前错误消耗预算的速度,以「恰好按窗口烧完」为 1 倍速:`(1 − success_ratio) / (1 − SLO)`。50% 失败率 ÷ 0.1% 预算 = **500 倍**(实测 `value: 5.0038e+02`,分毫不差)。

**多窗口多燃烧率**:只看短窗口,一次 GC 停顿就半夜叫醒你;只看长窗口,烧两天才立案。解法是长短窗口**与门**:

| 档位 | 条件 | 含义 | 动作 |
|---|---|---|---|
| 快烧(page) | 1h≥14.4 **and** 5m≥14.4 | 2 天烧光 | 立即叫醒 |
| 慢烧(ticket) | 6h≥6 **and** 1h≥6 | ~5 天烧光 | 建单 |

长窗口确认「预算在烧」,短窗口确认「现在还在烧」。原料用 M1 的成功率序列,**比较方向是 `≥阈值`**(成功率口径,不是错误率)。

**流量闸门**(M2 收官补):`and sum(rate(...[1h])) > 5`。小样本下比例无统计意义——凌晨 3 个请求挂 1 个 = 66.7% 错误率 = 燃烧率 667 倍 = 为一个请求叫醒你。闸门让低流量时告警根本不参与计算。

---

## 5. Alertmanager:分组、路由、抑制(M2)

- **route 树**:顶层兜底 receiver + 按 `severity` 分子路由(page/ticket)。`group_by:[alertname]` 把同一告警的多实例合并成一条通知(stage3 的 50-Pod 放大在这里被治住)。
- **`url: <secret>`**:AM 从 0.25 起把 webhook url 当敏感字段,API 里永远打码——不是没配上,恰恰是配上了。看明文去读 ConfigMap。
- **抑制(inhibit_rules)**:M4-3 思考题的落点。同一次故障 FastBurn(指标)和 ErrorBurst(日志)都响时,用抑制规则让 FastBurn firing 时压掉 ErrorBurst 的通知(别让值班收两条),但 ErrorBurst 单独 firing(指标没编码的错误)照常送。**两者盲区互补,不能二选一。**

---

## 6. SLO 看板:三纪律 + 怎么读(M3)

**三纪律**:① 面板只读 recording rule(SLO 看板上出现不带 `slo:` 前缀的查询是坏味道);② 阈值即视觉(Stat 红黄绿,一眼定严重度);③ 时间轴对齐预算窗口(28d 预算用 28d 轴,分钟级燃烧率单独给 6h 轴 + `Relative time` 覆盖)。

**怎么读(30 秒协议,结论→原因)**:头部三个数(破产了吗)→ 预算趋势线(什么时候烧的)→ 燃烧率(烧多快)→ 流量两联(为什么烧)。凌晨被叫醒只看前两层。

**图会撒谎的四种方式**(全部实测撞过):
1. `smooth` 插值画出 2000(物理上限 1000)——告警看板的线不许美化,用 `linear`;
2. 阈值 `mode: percentage` 是相对数据范围的百分比,不是「99.9% 可用性」——用 `absolute`;
3. `or vector(0)` 漏括号:`a or b/c`(优先级 `/`>`or`)→ 健康时显示 0(看着对)、事故时显示 1366600%。**「平时看着对、出事才崩」比一直崩更危险**,因为它通过了所有日常目视检查;
4. unit 选错差 100 倍(`percent (0.0-1.0)` vs `percent (0-100)`)。

**NaN 空白 ≠ 健康**:无流量时错误率是 NaN → 图上空白。补救不在表达式(别用 clamp_min 把 NaN 画成 0,那是把「不可测量」渲染成「0% 错误率=很健康」,销毁了视觉线索),在面板 description 写「空白=无流量」。

---

## 7. Loki + Alloy:第三支柱(M4-0/1)

**日志 vs 指标的架构分野**:指标是「当前状态」→ 可 pull 快照(Prometheus);日志是「已发生的事件流」append-only → 必须 push(Alloy tail stdout 推给 Loki)。

链路:`logsvc stdout → /var/log/pods/.../*.log → Alloy(DaemonSet,每节点一个)tail → 解析+打标签+推送 → Loki → Grafana LogQL`。

**Loki 存储模型**:标签建索引(贵),正文压缩后靠 grep(便宜)。铁律:**标签只放低基数+高频过滤字段,其余留正文用 LogQL 现场提**。`trace_id`/`request_id`/`pod` 当标签 → 索引爆炸 → OOM。这是 stage3「DefBuckets 触底」「高基数 label」教训的日志版。

logsvc 的标签决策:`namespace/app/container`(常量,挂采集目标)+ `level/path/status`(变量,从正文 promote)。总流数 ≈ 各 label 值域的**乘法**(dev 可接受,生产服务一多就咬人)。

**两个 Loki 装机坑**:
- `replication_factor:3`(chart 默认,为分布式设计)vs 单实例 → 写入 500「at least 2 live replicas required, could only find 1」。改 1。
- **`/ready` 返回 ready 但写入 500**——ready 检查进程活着+schema 初始化,不检查 ring 配额。**对存储/消息类组件,验收 = 真写一条进去再读出来,不是看 Pod 状态。**(stage3「探针只测进程活着,不测能不能干活」在 Loki 上的重演)

**Alloy 命门**:`mounts.varlog: true`(读宿主机 /var/log,不挂则管道三段全绿但零数据、且无报错);`clustering.enabled: false`(DaemonSet 每节点读自己的文件,clustering 分片会漏采);CRI 前缀必须用 `stage.cri` 剥掉(否则 json 解析的是 `2026-... stdout F {...}` 脏行,全废)。

---

## 8. 日志 vs 指标对账(M4-2)

30 个受控请求(20×200 带 token + 10×401 不带),三方各执一词:

| 数据源 | 200 | 401 | 总 |
|---|---|---|---|
| 压测器自报(客户端真相) | 20 | 10 | **30** |
| Prometheus 原始计数器 | 20 | 10 | **30**(精确) |
| `increase[3m]` | — | — | **0**(出生盲区) |
| Loki 日志行 | 0 | 10 | **20**(10 auth WARN 无 status + 10 request WARN 带 status) |

**`increase` 的两种系统性误差**:① 外推过冲(09-15 真值 20 报 21);② **出生盲区**——Go 的 CounterVec 子序列在首次 Inc 前不存在,v12 新 Pod 的序列「出生即 10」,Prometheus 从未观测到 0 基线,`increase=末−首=0`。Pod 重启的 counter-reset 检测救不了(pod label 变 = 全新序列,不是旧序列掉零)。**对账数事件用原始累计值,不用 rate/increase**(stage3「+Inf=2,000,000 分毫不差」的直系应用)。

**日志为什么只有 20 行**:① 200 的 request 行是 INFO,被 `LOG_LEVEL=warn` 压制(有意降噪);② 每个 401 产生两行——auth 的 WARN(无 status)+ observability 的 request 行(带 status,改 level 逻辑后才在 warn 下存活)。**带 status 的行曾经死了,活下来的行没 status** → 这就是 M4-1「status label 为空」的真相。

**对账的目的不是让 10==30**,而是理解差额被哪个设计决策吃掉、那决策是不是你要的。改之前差额是「无意的」(500 也被吃了),改之后是「有意的」(只吃 200)。

---

## 9. 中间件顺序决定可观测性边界(M4-2 的核心 bug)

生产链原是 `withRecovery(withObservaility(withAuthenticator(...)))`——**recovery 在最外层**。mux 里 panic 时:panic 沿调用栈向上展开,**穿过 withObservaility(它 `next.ServeHTTP` 之后的 slog.Log 和 Inc 全被跳过)**,最外层 recovery 接住写 500。后果:

> panic 产生的 500,客户端拿到了,但**日志没有、指标没有**。而 slo.md 白纸黑字写「panic 兜住的 500 算坏事件」——实际上它进不了 `http_requests_total`,**SLO 对 panic 是瞎的**。

修法一行:recovery 挪到 observability **里面**。改完 panic 路径:recovery 写 500 到 statusRecorder → 正常 return → observability 照常打 ERROR 行 + 计 500。日志/指标/SLO 对 panic 全部复明。

**前置实验**:panic 能穿过 `http.TimeoutHandler` 吗?(它把 handler 扔进另一个 goroutine,而 panic 不能跨 goroutine recover。)实测能——TimeoutHandler 在子 goroutine recover 后通过 channel 传回、在调用方 goroutine 重新 panic,所以透传。

**接缝(seam)抽取**:把链组装抽成 `chain(stat, inner)`,mux 从参数进。测试 `chain(stat, panicStub)` 走的是**生产真实组装线**,谁改顺序测试立刻红。手抄一条平行链测的是复制品,newHandler 被改回去时照样绿——等于没测。

**测试三断言对应 bug 的三个受害者**:① rec.Code==500(客户端体验);② buf 里有 request 行且 ERROR(日志可复盘);③ `httpRequestsTotal{status=500}==1`(指标/SLO 不瞎)。**写测试前先列「这个 bug 伤害谁」,每个受害者一条断言。**

**让测试先红一次**:故意把 chain 顺序改错 → 测试 FAIL(`no "request" log line found`,只剩 `panic occur`)→ 改回 → 绿。一个从没红过的测试,你不知道它会不会抓 bug。这是给测试做的「生效验证」。

---

## 10. 日志告警 + Loki ruler(M4-3)

**指标告警 vs 日志告警**:

| | 指标告警 | 日志告警 |
|---|---|---|
| 成本 | 便宜(预聚合数字) | 贵(读+解压+grep 日志行) |
| 可靠性 | 恒定(计数器永远在) | 依赖日志策略(LOG_LEVEL 一改就失明) |
| 能力 | 只能报编码过的 | 能报文本模式(panic occur、OOM、第三方库错误串) |

SLO/燃烧率永远用指标(全量);日志告警的领地是「指标没有但文本里有」的东西。两边互为盲区补集,所以生产两套都要。

**LogQL 两类查询**:日志查询(返回行)`{app="logsvc"} | json | status="401"`;指标查询(返回数字)`sum(count_over_time({level="ERROR"}[5m]))`。**ruler 只认指标查询**——「日志出现某行」不能直接告警,「5m 内 ERROR 行数>N」才能。

**label 过滤 vs 解析过滤**:`{status="401"}` 走索引(只读这条 stream),`| json | status="401"` 是 grep(读所有行逐行解)。结果同,成本差几个量级(24 行小数据测不出,百万行/GB 级才显形——负结果也是结果,但要写清「在什么规模下差异才显形」)。**这就是「什么字段值得当 label」的最终答案形态。**

**Loki ruler 装机三连坑**:
1. API 路径:`auth_enabled:false` 时是 `/prometheus/api/v1/rules`(**不带 fake 租户段**),我按多租户版记的 `/prometheus/fake/...` 返回 404。手法:候选路径挨个 `curl -w "%{http_code}"` 试。
2. 规则不加载(groups:[]):sidecar 已同步文件到 `/rules/fake/`,但 ruler 的 `storage.local.directory` 没配 → 每分钟轮询空目录。修法:`loki.rulerConfig.storage.local.directory: /rules`(指向租户目录的父目录)。
3. `mkdir /rulers: read-only file system` → CrashLoopBackOff:directory 误写 `/rulers`(多一个 s),而容器 `readOnlyRootFilesystem: true`,mkdir 被内核拒绝。**三个好消息**:只读根文件系统是正确安全姿态(拦笔误也拦勒索脚本);Loki 选择响亮崩溃而非静默失效;报错一行含完整因果链。改回 `/rules`(emptyDir 挂载点,已存在,不触发只读检查)即愈。

**两个 ruler 别搞混**:values 顶层 `ruler:`(3339 行)是 helm **部署层**(镜像/副本/sidecar);`loki.rulerConfig:`(558 行)是 Loki **原生配置层**(会合并进 config.yaml 的 ruler 段)——`alertmanager_url`、`storage.local.directory` 放后者。

---

## 11. 假恢复:告警盲区的最尖锐形态(M4-3)

收到 `LogsvcErrorBurst resolved` 后隔天实测带 token 打一发 → **仍 500**,FAIL_RATE=100 根本没关。告警「恢复」的真因:**停止了打流量**。`count_over_time({level="ERROR"}[5m])` 数窗口内的错误行,没请求就没新行,窗口滑过 → 归 0 → resolved。**系统烂着,告警说没事了。**

这和 M3-Q3「告警对沉默失明」是同一个洞:

| | 指标告警(FastBurn) | 日志告警(ErrorBurst) |
|---|---|---|
| 零流量时 | 燃烧率 NaN + 闸门不通过 → 沉默 | count_over_time=0 → **假 resolved** |

指标告警至少只是沉默(NaN 比较恒 false,不会说「好了」),日志告警还会主动发 resolved 撒谎。**两套独立系统共享同一盲区** → `LogsvcNoTraffic` 告警(Ready 但 1h 零流量 → ticket)必须存在,它是给「另外两条都瞎了的场景」装的灯。生产还有一层黑盒探测(blackbox probe 从集群外打真实请求),连「服务整个死了、无流量告警都发不出」都兜住(stage5/6)。

---

## 12. 元教训(本 stage 的认识论收获)

1. **「写了≠生效」进化出 5 个新形态**:helm `--set-file` 把文件名拆成路径;`alertmanagerFiles` key 在 chart 版本里不存在(静默丢弃);规则 health:ok 但查询永远空;sidecar 同步了文件但 ruler 不知道目录;`/ready` 通过但写入 500。**验证顺序固化为四步:CM/生效配置 → 进程活着 → API 里有内容 → 业务动作(写一条/触发一次)。**
2. **权威会过期,集群不会**(stage3 教训延续,本 stage 我被纠正 4 次):`alertmanagerFiles` key、`/prometheus/fake` 路径、`replication_factor` 默认值、Loki 默认值。**凡是「X 该填什么 key」「Y 路径是什么」,`helm show values`/`kubectl explain`/`/config` 端点/挨个 curl 试,十秒裁决,别信记忆(包括我的)。**
3. **绿了≠测对了**:`-run` 过滤器只跑一个测试给了虚假的全绿(TestResponse 的 nil panic 被掩盖);无断言的测试永远绿;断言对象错了的测试(测 withRecovery 却以为在测 withObservaility)绿得是假的。**`go test ./...` 不带 `-run` 才算数;新测试要先红一次。**
4. **三态语义(空/0/NaN)是贯穿全 stage 的母题**:指标、看板、日志、告警处处是它。
5. **基数教训跨支柱复现**:stage3 指标桶/label → M4-1 日志 label,同一个「枚举有界才能进维度空间」。
6. **对账是免费保险**:写公式前先对账(+Inf=2M、30/31/10),预测不是为了全对,是为了让错的地方暴露新知识(increase 出生盲区就是这么挖出来的)。
7. **简化形态 vs 生产形态的取舍要显式**:单实例 Loki(RF=1、节点本地存储)、服务端指标近似客户端 SLI、合成流量污染 SLO——每次简化都要知道简化了什么、代价是什么、写进文档。

---

## 13. 命令 / 查询速查

**验证四步链**(任何配置改动后):
```bash
kubectl get cm <name> -n godev -o jsonpath='{.data.<key>}'   # 1. 生效配置
kubectl get pod <pod> -n godev -w                            # 2. 进程活着(看 startedAt/restartCount)
curl -s localhost:<port>/<api>/rules | jq                    # 3. API 里有内容
# 4. 业务动作:写一条日志/触发一次告警,看终点
```

**SLO 对账**(数事件用原始值,不用 increase):
```promql
sum(http_requests_total{path="/ping"})                      # 精确累计
sum by (status) (increase(http_requests_total{path="/ping"}[28d]))  # 趋势(注意出生盲区)
```

**Loki 写入试金石**(ready≠usable,必须真写):
```bash
NOW=$(date +%s)000000000
curl -X POST localhost:3100/loki/api/v1/push -H 'Content-Type: application/json' \
  -d "{\"streams\":[{\"stream\":{\"app\":\"probetest\"},\"values\":[[\"$NOW\",\"ok\"]]}]}"
# 204 = 写进去了;500 at least 2 live replicas = RF 配错
```

**LogQL**:
```logql
{app="logsvc", level="ERROR"}                          # label 过滤(走索引)
{app="logsvc"} | json | status="401"                   # 解析过滤(grep)
sum(count_over_time({app="logsvc",level="ERROR"}[5m])) # 日志→指标(告警用)
{app="logsvc"} | json | duration_us > 1000             # 数值字段比较(无该字段的行被丢弃)
```

**燃烧率**:`(1 − slo:...success_ratio<window>) / (1 − SLO目标)`,与门 + 流量闸门。

---

## 14. 交付物清单

- `slo.md`:两个 SLO 合同 + 第 5 节 synthetic 流量条款(方案 B 独立路径为目标态,方案 C 标注为过渡)
- `logsvc-slo.rules.yml` + values 里的 recording/alerting rules:7 条 recording + 3 条告警(FastBurn/SlowBurn/NoTraffic)
- `slo-dashboard.json`:7 面板 SLO 看板(头部结论/预算趋势/燃烧率/流量)
- `cmd/alert-receiver/`:Go webhook 接收器(v2,severity 解析修复)
- `cmd/server/main.go` v12:中间件顺序修复 + status 判级 + `chain()` 接缝 + `levelForStatus`
- `cmd/server/main_test.go`:TestObservabilityLevel(table-driven)+ TestPanicVisibleToObservability(三断言锁顺序 bug)
- Loki + Alloy 单实例日志栈,logsvc 日志可在 Grafana Explore 按 6 个 label 检索

**唯一待部署项**:`LogsvcNoTraffic` 告警(规则已写进 info.md T4,需 helm upgrade Prometheus values 落地——master 节点我无 SSH 权限,留给你)。

**stage4 计划内未做**:M5(OTel Tracing)。集群已有 otel-collector(但 CrashLoopBackOff 87 天)+ jaeger,可直接接——见收官消息的决策点。

# stage3 —— `logsvc` 部署进 Kubernetes:从镜像到生产级运维

> **目标**:把 stage2 那个 10.8MB 的镜像部署进真实的多节点 K8s 集群,亲手接上探针、经历重启风暴、证明发版零掉包、外置配置与凭据、做资源画像,并学会**正确地读**可观测数据。
> **产物**:`godev` 命名空间下双副本的 `logsvc` Deployment(探针、优雅关闭、ConfigMap/Secret、资源配额、GOMEMLIMIT 全配齐),一个自己写的并发压测工具,以及一组用对照实验换来的硬核结论。
> **环境**:4 节点 K8s v1.32.9(containerd)、本机 Harbor 仓库、已部署的 Prometheus/Grafana。

---

## 一、里程碑历程(每一关都由一个真实事故推动)

| 里程碑 | 主题 | **撞上的坑 / 学到的教训** |
|--------|------|------------------------|
| **M0** | 镜像进 Harbor + 首个 Deployment/Service | 本地 docker ≠ 节点的 containerd,镜像必须过仓库;`:latest` 默认 `Always` 会隐藏版本漂移 |
| **M1** | Deployment + 探针接入 | 🔴 两探针参数配成对称的,但**失败的代价不对称**(摘流量可逆 vs 重启不可逆) |
| **M2** | 探针调优 + 重启风暴实验 | 🔴 liveness 探了个 404 路径 → CrashLoopBackOff,7 次重启,Endpoints 剧烈抖动;**「时好时坏」比「彻底挂掉」恶劣得多** |
| **M3** | 滚动更新压测,证明零掉包 | 🔴 掉包不是 503 而是 **000**(连接重置);根因是 SIGTERM 与 kube-proxy 更新 iptables 的**竞态**;`preStop.sleep` 修复后 000=0 |
| **M4** | ConfigMap / Secret / `loadConfig` | 🔴 `slog.LevelDebug` 的异常探针日志**从未输出过**(默认最低级别是 Info)—— 精心准备的告警线索是死代码 |
| **M5** | 资源配额 / QoS / OOM / GOMEMLIMIT | 🔴 两种 OOM 形态(sandbox 创建失败 vs 运行期 OOMKilled);**OOMKill 会作废全部优雅关闭工作** |
| **压测专题** | 自建压测工具 + 三轮 CPU 对照 | 🔴 17.8% 失败是**压测机自产**(端口耗尽);CPU limit < 2 核对 Go 是**结构性节流** |
| **观测专题** | Prometheus 抓取 / 直方图 / 对账 | 🔴 抓 Service 导致两个 Pod 的计数器混在一条序列里;`DefBuckets` 量程触底,P99 是 `桶边界×q` 的假平线 |

---

## 二、镜像进集群

- **本地 docker 和节点的 containerd 是两套独立存储**,中间没有共享 —— 镜像必须推到节点可达的仓库。
- **`imagePullPolicy` 默认值规则**:tag 是 `:latest` 或省略 → `Always`;其它 tag → `IfNotPresent`。用 `latest` + `Always`,同一个 Deployment 在不同时间起的 Pod 可能跑着不同代码,而 `describe` 看不出任何区别。**用带版本号的 tag,或直接用 digest。**
- **tag 是可变指针,digest 才是内容身份证。** `image: repo@sha256:...` 让「YAML 写的」和「节点跑的」在密码学上等价(GitOps 流水线的标准做法)。
- **distroless 的代价开始收费**:没有 shell,`kubectl exec -it -- sh` 进不去;`preStop.exec` 里的 `sleep` 二进制也不存在。「进不去容器,就只能靠它自己往外说话」—— stage2 做的外部可观测性(日志/指标/探针)此时成了唯一手段。
- 解法:k8s 1.29+ 的原生 `lifecycle.preStop.sleep`(kubelet 计时,不需要容器内任何二进制)。**「这个特性不支持」的判断,`kubectl explain` + `--dry-run=server` 十秒可验证。**

---

## 三、探针:代价不对称 → 参数必须不对称

| | readiness | liveness |
|---|---|---|
| 失败动作 | 摘流量 | **重启容器** |
| 可逆性 | 可逆,下次成功自动加回 | 不可逆:连接断、状态丢、可能进 BackOff |
| 应配成 | **敏感**:`failureThreshold` 1~2,快摘 | **迟钝**:`failureThreshold` 3,多问几次 |

**两个杠杆别用错**:`failureThreshold` 表达的是「允许偶发失败」(容忍抖动,合理);`timeoutSeconds` 表达的是「一次探测慢到 X 秒也算健康」。对微秒级的 `/healthz` 给 10s 容忍,等于说「健康检查慢 10 秒还算健康」。**迟钝用 failureThreshold 实现,timeout 只给 1~2s**,且 `timeout << period`(kubelet 对同一探针串行,timeout≈period 时探测频率会退化)。

**最坏检测时间要会算**:`failureThreshold × periodSeconds`(+initialDelay)。写进 YAML 注释。

**探针之间没有任何先后顺序** —— kubelet 为每个探针起独立 worker,各跑各的周期。k8s 里真正提供「顺序」的是第三种探针 **startupProbe**:它成功之前 liveness/readiness 全部禁用,专治慢启动应用,把「启动期宽容」和「运行期严格」解耦。

**探针流量不进业务可观测**:
- 指标:探针进 `http_requests_total` 会稀释错误率、拉平分位数(探针是微秒级,淹没真实慢请求)
- 日志:一天上万条零信息量日志
- 但要留**失败日志**(WARN 级):探针失败 = k8s 即将采取动作,不是「值得留意」而是「已经出事」
- 原则:**日志成本随请求数线性涨,指标成本几乎恒定** —— 高频低价值流量砍日志、留指标

### 重启风暴实录(liveness 探 404 路径)

```
Normal   Killing    Container logsvc failed liveness probe, will be restarted
Warning  BackOff    Back-off restarting failed container logsvc
lastState: {"exitCode": 0, "reason": "Completed"}   ← 优雅关闭成功,k8s 照样算失败
```

- **退出码 0,k8s 仍判「失败」** —— 是否失败由触发原因决定,不看退出码
- 每个循环容器活 ~48s:**readiness 第 5s 就通过 → Pod 被加回 Endpoints 正常接流 ~43s → liveness 累计 3 次失败被杀**。Endpoints 不是「一直空」,而是**剧烈抖动**;你看到空,是因为观察的是退避末期的死寂
- **「彻底死掉」是好事故**(立刻告警、立刻排查);**「时好时坏」是坏事故**:间歇性 5xx、连接重置,错误率既非 0 也非 100%,像网络抖动,能耗掉一整天还被误判

### 放大到 50 个 Pod(如果 liveness 探了数据库)

1. **自杀式全挂**:数据库抖 30s → 50 Pod 同时判死 → 全部重启 → Endpoints 全空 → 服务 100% 中断
2. **恢复时间远超故障时间**:CrashLoopBackOff 退避指数增长(10s→20s→40s→…→300s 封顶),30s 的抖动换来 5 分钟+ 的中断,且退避期间人工无法干预
3. **雪崩正反馈**:50 Pod 同时冷启动 → 空连接池+缓存穿透的洪峰 → 打挂刚缓过来的数据库 → 循环放大
4. **控制面连带受害**:海量 Events/status 更新压 apiserver/etcd,波及全集群

> **铁律:Liveness 只探自己进程的死活,绝不掺外部依赖。** 因为 liveness 的动作是重启,而重启治不好数据库。**只有「重启能解决的问题」才配用 liveness。**

---

## 四、发版零掉包:完整链路

### 掉包的形态:000,不是 503

`/ping` 在关闭期间照常返回 200;readyz 的 503 是说给 kubelet 的,业务客户端**永远看不到**。客户端只会遇到两种结局:

| 客户端看到 | 含义 | 性质 |
|---|---|---|
| `000` / 连接重置 | 服务**已经不存在**,连拒绝的机会都没有 | 基础设施/时序问题 |
| `503` | 服务活着,**主动拒绝** | 应用的决定,客户端可重试 |

**「拒绝」和「消失」是两种完全不同的失败。** 000 比 503 难查一个数量级。压测前先想清楚「失败长什么样」,否则拿着 503 的通缉令会把真凶 000 放走。

### 根因:SIGTERM 与 iptables 更新的竞态

「让流量别再来」是一条异步多跳链:Pod 标记 Terminating → endpoints controller 摘 IP → **每个节点**的 kube-proxy 各自 watch → 各自重写 iptables/ipvs。而 kubelet 发 SIGTERM 是另一条线,**两者无先后保证**:

```
t=0      SIGTERM → 你的代码立刻 srv.Shutdown() 关 listener
t=0~2s   部分节点的 iptables 仍把新连接转给这个 Pod → RST → 000
t=2s+    规则同步完,流量去新 Pod
```

优雅关闭只解决「存量请求跑完」;**增量连接撞进「门已关、路牌未换」的窗口**,才是 000 的来源。

### 修法:关门前站在门口多迎几秒

```yaml
lifecycle:
  preStop:
    sleep:
      seconds: 3      # k8s 1.29+ 原生支持,kubelet 计时
```

- **preStop 在 SIGTERM 之前执行**,进程完全不知情 —— 「等 iptables 传播」这件纯 k8s 的事留在 k8s 配置里,二进制只管「收到 SIGTERM 就优雅关闭」
- 预算:`preStop + 优雅关闭超时 ≤ terminationGracePeriodSeconds`(默认 30s,超了 SIGKILL)
- **为什么不该写进代码**:drain 时长取决于 kube-proxy 模式、Service 数量、apiserver 负载 —— **凡是「值由部署环境决定」的参数,都不该编译进二进制**
- 实测:000 从 15 → **0**,滚动两轮稳定

### 完整链路(每一环都是踩着坑焊上去的)

```
readyz 翻 503(摘流) → preStop.sleep(等 iptables 传播) → SIGTERM →
优雅关闭排空存量 → exec-form ENTRYPOINT 保证信号直达 PID 1(stage2)
```

### 滚动更新参数

- `maxSurge` **向上**取整、`maxUnavailable` **向下**取整 —— 两个方向都往「更安全」偏:资源可超卖,可用性不能
- 2 副本 + 25%/25%:surge=1(最多 3 个)、unavailable=0(始终保 2 个可用)

---

## 五、容器死法尸检表(10 秒定死因)

| exitCode | reason | 谁下的手 | 信号 | 优雅关闭 |
|---|---|---|---|---|
| `0` | `Completed` | kubelet(liveness 失败/正常删除) | SIGTERM | ✅ 代码接住了 |
| `143` | `Error` | 同上 | SIGTERM | ❌ 进程**没处理**信号 |
| `137` | `OOMKilled` | **内核 OOM killer** | **SIGKILL** | ❌ 不可能 |

**OOMKill 会一次性作废整条关闭链路**:preStop 不执行、`Ready.Store(false)` 不执行、`Shutdown` 不执行、in-flight 全部连接重置。内核先斩,kubelet 后知。**内存 limit 配太紧 = 埋一颗定时炸弹,每次爆炸都是 100% 掉包的硬故障** —— M3 辛苦降为零的 000,一次 OOM 全部还回去。CPU 是可压缩资源(不够只是慢),内存不是(不够只能杀),**给内存留 headroom 的优先级远高于 CPU**。

---

## 六、资源、QoS 与调度经济学

- **调度看 requests,不看 limits。** requests 是「从集群里真的划走多少,别人再也拿不到」;limits 是天花板
- **超卖建立在「峰值不同时发生」的统计事实上**:按 limits 调度 = 假设所有服务同时打峰值 = 为几乎不可能的事件预留全部资源 → 节点利用率掉到 20~30%
- **requests = limits 等于主动放弃弹性红利**(你为一个日常 2m 的服务永久扣了 2 核,1000 倍预留)
- 两种 OOM 形态:**limit 低于运行时开销** → `Pending`/`FailedCreatePodSandBox`(runc 自己被 OOM,容器从未存在,无 RESTARTS);**limit 高于启动但低于工作集** → `Running` 后 OOMKilled 137
- **测峰值不测地板**:「不断下调直到起不来」测的是空载门槛;生产要 峰值×1.5~2。4Mi 能启动、一压测就 OOM,就是地板≠工作集
- Go 的 GC 默认堆翻倍才回收(`GOGC=100`),进程天然涨到工作集 2 倍 —— 不是泄漏,是设计

### GOMEMLIMIT

- Go 的 GC **不知道** cgroup memory limit 的存在;`GOMEMLIMIT` 是给 GC 装的软红线:逼近时激进回收(烧 CPU 换存活)
- 经 Downward API 注入,单位两端对齐:k8s 侧 `divisor: 1` 吐纯字节数,Go 侧恰好接受裸字节 —— **单位 bug 第 5 次上门,第一次被提前拦住**
- 取 limit × 0.9,给非堆留余量(cgroup 数的是进程 RSS 总量:堆+栈+mmap+运行时)
- **GOMEMLIMIT 救勤俭,不救贫穷**:它只能命令 GC 勤收「垃圾」,对活着的对象和 Go 堆之外的内存无能为力。工作集真超过 limit,死是必然,GC 勤快只决定死得早晚
- `SetMemoryLimit(x)` **返回旧值**(read-modify 型 API 的常见约定);嵌在 Printf 里打出来的是「改之前」—— 验证要「改完再读」;有副作用的调用不要嵌进日志语句
- 百分比法则有隐含的最小绝对值:16Mi 的 10% 是 1.6Mi,而非堆开销是「几 Mi 起步」的绝对量 —— **规模小到一定程度,百分比要换成绝对值思维**

---

## 七、CPU 节流与 GOMAXPROCS(三轮对照实验)

### 数据

| cpu limit | QPS | P50 | P99 | **P99/P50** |
|---|---|---|---|---|
| 256m | 1771 | 16.1ms | 295.9ms | **18.4×** |
| 1000m | 7352 | 6.39ms | 64.1ms | **10.0×** |
| 2000m | 13770 | 5.48ms | 30.4ms | **5.6×** |

### CFS 配额机制

内核把时间切成 100ms 周期,limit 决定每周期配额(256m → 25.6ms CPU 时间)。配额烧完,**整个 cgroup 冻结到下一周期**。注意配额是 CPU-time:`GOMAXPROCS=2` 时两个线程全速跑,1ms 墙钟烧 2ms 配额 —— **「程序只用 10ms」的直觉错在混淆了墙钟和 CPU 时间**。

### 结构性节流:Go 1.25 的 `GOMAXPROCS = max(2, ceil(limit))`

| limit | GOMAXPROCS | 配额 | 2 线程烧完要 | 节流占比 |
|---|---|---|---|---|
| 256m | 2(下限兜底) | 25.6ms | 12.8ms | 87% |
| 1000m | 2(下限兜底) | 100ms | 50ms | 50% |
| 2000m | 2(=ceil) | 200ms | 200ms | **0%** |

**limit ≥ 2 核时 `ceil` 让配额与线程数天然匹配,永不节流;< 2 核时下限 2 打破匹配,满载必被节流,配多少都一样。** 推论:延迟敏感的 Go 服务,CPU limit 要么 ≥2000m,要么干脆不配(只配 requests);真要配小就显式 `GOMAXPROCS=1` 接受单线程。

### 三条辨识法则

1. **节流下的「利用率不高」是天花板,不是需求** —— 0.17 核不代表「只需要 0.17 核」,只代表「最多被允许用这么多」。抬高天花板,需求立刻站直(QPS×3)
2. **节流的指纹是尾部离散度**:P99/P50 比值 >10 且利用率不高 → 怀疑节流。节流不让每个请求都慢,它让撞上冻结窗口的那批特别慢
3. **看节流指标,别看 CPU 使用率**:`rate(container_cpu_cfs_throttled_seconds_total[1m])`

---

## 八、可观测性在 K8s 下的正确使用

### 抓 Pod,永远不抓 Service

用 Service 名做 target(`instance="logsvc:8080"`),kube-proxy 每次 scrape 随机命中一个 Pod → **两个进程各自独立的计数器交替装进同一条序列** → 每次切换被误判成 counter reset,`rate()` 全是垃圾。监控要的是每个实例的独立视图,聚合是 PromQL(`sum by (...)`)的工作。修法:Pod template 加 `prometheus.io/scrape: "true"` 注解,走 kubernetes_sd 的 pod 发现。

### 采样密度是可观测性的地基

- `rate()`/`increase()` 需要窗口内 ≥2 个样本 → **窗口 ≥ 2× scrape_interval,一般取 4×**
- **持续时间短于采样间隔的事件,对监控系统不存在** —— 7 秒的压测在 1m 采样里可能一个点都没有。「监控没告警」永远不能证明「没发生」
- **改了配置必须验证生效**:以为 scrape_interval 改成 15s,实际还是 1m —— 「写了配置」和「配置生效」之间隔着一次部署

### Histogram 的两堂课

- **量程触底指纹**:99%+ 数据挤在第一个桶时,`histogram_quantile` 输出退化为 `桶边界 × q` 的**纹丝不动的平线** —— 平线不是延迟稳定,是仪表分辨率不够
- **DefBuckets(5ms~10s)是给几十毫秒级服务设计的**;微秒级服务必须换表,在真实延迟区间加密桶。注意插值:分位数是桶内线性插值猜的,压在桶边界上的结果要警惕
- **桶 = 序列成本**:桶数 × label 组合数 = 时间序列数,加密要克制

### rate/increase 是估计,Counter 原始值才精确

- `increase()` 会**线性外推**到窗口两端:采样越稀、流量越突发,虚高越大(200w 被估成 250w)
- **峰值之和 ≠ 和的峰值**:分别取两个 Pod 各自的最大值再相加必然偏大;要总量就 `sum(increase(...))` 一条曲线取一个峰
- **精确对账用原始 Counter**(压测前后相减)—— 实测:histogram `+Inf` 累计 = **2,000,000,分毫不差**

---

## 九、延迟的两种真相:服务端时间 ≠ 用户等待时间

```
客户端 P50 = 5.9ms
├─ 客户端调度/处理 + 网络 RTT(~0.1-0.3ms)
├─ NodePort DNAT + conntrack + iptables
├─ 内核 TCP 接收、buffer 拷贝
├─ 高负载下 goroutine 唤醒延迟
├─ ▶ 服务端 handler ≈ P50 19µs / P99 249µs ◀  ← histogram 只量这一段
├─ 响应 flush(write 系统调用,在计时范围外)
└─ 回程
```

- histogram 的 `start := time.Now()` 打在 handler 被调用瞬间 —— **在那之前排的所有队它一无所知**;纯内存操作的 /ping 量出 20~250µs 完全合理
- **差值(~5.6ms)= 排队 + 路径**。系统过载时两者剧烈背离:服务端指标一切正常,用户在超时,因为瓶颈在队列里、在观测点之外
- **SLI 必须定义在客户端侧或入口侧**(网关/LB/RUM),不能只用服务自报的延迟

---

## 十、压测方法论

1. **压测端必须先证明自己不是瓶颈**,数据才有资格被解读。实录:17.8% 失败全是压测机自产
   - `connect: cannot assign requested address` = 临时端口耗尽(本机 28231 个端口 ÷ 10 万不复用的连接)
   - 根因链:`resp.Body.Close()` 不排空 body → 连接不能复用 → 每请求新建 TCP;修复后 15313 → 160
   - 残余 160:`MaxIdleConnsPerHost` 默认 **2** —— Go 客户端高并发调下游的经典坑;自建 Transport 调大 + 共享 `http.Client` 后 → **0**
2. **永不静默丢弃**:错误要按字符串归类计数(`err` 里写着 connection refused / timeout / 端口耗尽,指向三种不同故障);状态码要按码计数(任何响应都算 success 的工具会把事故报成健康)—— stage1 第 1 条原则,这次直接换来一次根因定位
3. **P50 和 P99 一起看分布形状**:P50 低 P99 高 = 少数请求踩坑(找异常路径);一起高 = 系统性饱和(找容量)。只取一个点就退化成单个数字
4. **对照实验一次只动一个变量**,否则 QPS 变化归因不清
5. **实验做不出现象时,先怀疑设计再怀疑力度**:无状态服务加压涨的是内存「流速」不是「水位」,OOM 实验在它身上设计不出来 —— 不是压力不够
6. **负结果也是结果**:「GOMEMLIMIT 没能救活 8Mi」证明了它的边界,比对照成功理解更深
7. L4 LB **按连接**均衡:长连接场景分布取决于连接数,200 条连接出现 65/35 偏斜很正常

---

## 十一、元教训(跨项目可迁移)

1. **单位 bug 第 5 次上门,第一次被拦住** —— divisor=1(字节)对接 GOMEMLIMIT(裸字节),两端单位先查约定再接线。前 4 次的学费没白交
2. **权威会过期,集群不会**:两次被实测纠正 ——「distroless 用不了 preStop」(1.29 有原生 sleep)、「GOMAXPROCS 读节点核数」(Go 1.25 起读 cgroup)。**凡是「不支持/会怎样」的断言:`kubectl explain`、`--dry-run=server`、30 秒对照实验,胜过任何人的记忆**
3. **验证「生效」而不是「写了」**:LevelDebug 死代码、LOG_LEVEL 未生效、scrape_interval 未生效 —— 同一族事故三次。可观测系统自己也要被观测
4. **「没报错」≠「没问题」的 K8s 版**:`kubectl apply` 成功 ≠ 配置生效;退出码 0 ≠ k8s 认为容器成功;监控没告警 ≠ 没发生
5. **配置里凡是环境决定的值都不进二进制**;凡是靠代码顺序才成立的行为都要用注释锁住意图
6. **「利用率不高」在有 limit 的世界里不能推出「资源给多了」** —— 先看节流,再下结论

---

## 十二、命令与查询速查

```bash
kubectl get pod/deploy/svc/ep -n godev -o wide
kubectl describe pod <pod> -n godev              # Events:探针失败、BackOff、OOM 全在这
kubectl logs <pod> -n godev --previous           # 上一个容器的日志(OOM 后必看)
kubectl get pod <pod> -o jsonpath='{.status.containerStatuses[0].lastState}'   # 尸检
kubectl rollout restart deploy/logsvc -n godev
kubectl top pod -n godev                          # 需要 metrics-server
kubectl explain pod.spec.containers.lifecycle.preStop.sleep
kubectl apply -f x.yaml --dry-run=server          # 验证集群是否接受,不实际创建
```

```promql
histogram_quantile(0.99, sum(rate(http_request_duration_seconds_bucket[1m])) by (le, pod))
rate(container_cpu_cfs_throttled_seconds_total[1m])          # 节流秒数
rate(container_cpu_cfs_throttled_periods_total[1m])
  / rate(container_cpu_cfs_periods_total[1m])                # 节流周期占比
http_requests_total                                           # 精确对账用原始值,别套 rate
```

---

## 十三、最终成果

双副本 `logsvc` 运行于 `godev`,25k QPS、P99 249µs(服务端)、压测零失败:

✅ 探针不对称配置(敏感 readiness / 迟钝 liveness),探针流量不进业务指标
✅ 经历并解释完整的 CrashLoopBackOff 重启风暴(含 Endpoints 抖动)
✅ 发版零掉包链路:readyz 摘流 → preStop.sleep → 优雅关闭 → exec-form,压测 000=0
✅ ConfigMap/Secret 外置配置与凭据,`loadConfig` 有测试
✅ 资源画像:requests/limits 由实测峰值定,GOMEMLIMIT 经 Downward API 注入
✅ 自建压测工具:状态码+错误归类计数、p50/p95/p99、连接复用正确
✅ 三轮 CPU 对照实验 + GOMEMLIMIT 边界实验,数据全部记录在案
✅ Prometheus 抓 Pod、scrape 密度、直方图桶分辨率、rate/increase 的估计本质

---

**⬅️ 上一站**:[stage2](../stage2/stage2.md) —— 可观测的生产级 HTTP 服务
**➡️ 下一站**:stage4 —— Prometheus/Grafana/Alertmanager 深入:SLI/SLO/error budget、告警设计、Loki/Tempo 日志与追踪

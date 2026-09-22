# stage2 —— `logsvc` 可观测的生产级 HTTP 服务

> **目标**:从「能跑的 HTTP 服务」走到「生产级服务」——可观测、可关闭、可探活、扛得住异常、能被测试、能装进容器。
> **产物**:一个带结构化日志、Prometheus 指标、健康检查、优雅关闭、panic 恢复、分层超时的服务,以及一个 **10.8MB** 的 distroless 容器镜像。

---

## 一、里程碑历程

| 里程碑 | 主题 | **撞上的坑 / 学到的教训** |
|--------|------|------------------------|
| **M0** | 最小 `net/http` 服务 | 🔴 `"/ping"` 匹配**所有**方法 → `DELETE /ping` 也回 200(接口在撒谎)。Go 1.22+ 用 `"GET /ping"` 才能自动返回 **405** |
| **M1** | `log/slog` 结构化日志 + 中间件 | 🔴 `duration` 是**裸纳秒数字**,下游不知道单位;`slog.SetDefault` 会**接管标准库 `log`**,把 `log.Fatal` 打成 INFO |
| **M2** | 优雅关闭(`context` 首次登场) | 🔴 把「server starting」放在**阻塞的** `ListenAndServe` 之后 → 永远不会执行;`go run` **谎报退出码为 1** |
| **M3** | `/healthz` + `/readyz` | 🔴 就绪标志用普通 `bool` → **`-race` 抓到 DATA RACE**,必须用 `atomic.Bool` |
| **M4** | Prometheus `/metrics` | 🔴 把**微秒**喂进名为 `_seconds` 的直方图 → 所有观测值溢出到 `+Inf`,桶全是 0 |
| **M5** | 超时 + panic 恢复 + 中间件串联 | 🔴 `ReadHeaderTimeout: 5` 是 **5 纳秒**,服务全线拒连;超时/中间件的**顺序**决定日志准不准 |
| **收尾** | Dockerfile / Makefile / 可测架构 | 🔴 `/srv` 撞系统目录;`~` 在 Dockerfile 里**不展开**;测试依赖外部服务 = **假测试** |

---

## 二、HTTP 服务的骨架

### 中间件(装饰器模式)
```go
func withX(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // 前置处理
        next.ServeHTTP(w, r)
        // 后置处理
    })
}
```
「洋葱皮」结构:在**不改业务代码**的前提下,给所有请求统一加日志、指标、认证、限流。

### 抓取状态码:包装 `ResponseWriter`
`http.ResponseWriter` **没有任何方法能读回已写出的状态码**(它是「只写的水管」,写出去的不留副本)。解法是自己套一层:

```go
type statusRecoder struct {
    http.ResponseWriter        // 嵌入:自动继承 Header()/Write()
    status int
}

func (rec *statusRecoder) WriteHeader(code int) {
    rec.status = code               // 抄下来
    rec.ResponseWriter.WriteHeader(code)  // 再转发
}

rec := &statusRecoder{ResponseWriter: w, status: 200}  // ← 默认值必须是 200!
```

**为什么初始值是 200**:handler 若只调 `Write` 而不调 `WriteHeader`,Go 会**隐式发送 200**,但你的 `WriteHeader` 钩子根本没被触发。默认 200 才如实反映这个隐式行为。

**关键点**:嵌入 `http.ResponseWriter` 后,只需重写**你想插手的那一个方法**,其余自动透传。

---

## 三、可观测性

### 日志(`log/slog`)—— 回答「这一条请求发生了什么」
```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
slog.SetDefault(logger)

slog.Info("request",
    "method", r.Method,
    "path", r.URL.Path,
    "status", rec.status,
    "duration_us", time.Since(start).Microseconds())
```
- **结构化(JSON)才能被机器处理** —— 采集、按字段过滤、聚合告警
- ⚠️ **`slog.SetDefault` 会接管标准库 `log`,统一按 INFO 输出** → 服务内应统一用 `slog`,别混用 `log`,否则致命错误会被降级成 INFO,永远不会触发告警

### 指标(Prometheus)—— 回答「整体现在什么状况」
```go
prometheus.NewCounterVec(CounterOpts{Name: "http_requests_total"}, []string{"method","path","status"})
prometheus.NewHistogramVec(HistogramOpts{Name: "http_request_duration_seconds",
    Buckets: prometheus.DefBuckets}, []string{"method","path"})

http.Handle("GET /metrics", promhttp.Handler())
```

**四个黄金信号**(Google SRE):

| 信号 | 含义 | 指标类型 |
|------|------|---------|
| **Latency** 延迟 | 请求耗时分布 | Histogram |
| **Traffic** 流量 | QPS | Counter |
| **Errors** 错误 | 失败比例 | Counter 按 status 拆分 |
| **Saturation** 饱和度 | 资源有多满 | Gauge |

**三种指标类型**:Counter(只增不减)、Gauge(可增可减)、Histogram(分桶,用于算 p50/p99)。

**Histogram 的输出不是一个数字**,而是三组序列:
- `_bucket{le="X"}` —— 耗时 ≤ X 的**累计**个数
- `_sum` —— 观测值总和 · `_count` —— 观测次数
- 平均值 = `_sum / _count`;分位数由 Prometheus 用 `histogram_quantile()` **根据桶插值**算出

⚠️ **Prometheus 约定:所有时间一律用「秒」(float)**。`DefBuckets`(0.005~10)也是秒。

⚠️ **基数爆炸**:别把用户 ID、含动态参数的路径塞进 label。用**路由模板**(`/user/{id}`)而非原始路径(`/user/12345`),否则扫描流量能把 Prometheus 内存撑爆。

---

## 四、优雅关闭与 `context`

### 为什么需要
K8s 发布/缩容时发 `SIGTERM`,进程**默认立刻死**。此刻正在处理的请求全部变成 502/连接重置 —— 一次正常发版制造了一场"故障",白白消耗错误预算。

### 标准结构
```go
srv := &http.Server{Addr: ":8080", Handler: handler}

go func() {                                    // 监听放 goroutine(它是阻塞的)
    err := srv.ListenAndServe()
    if errors.Is(err, http.ErrServerClosed) {  // ← 这个「错误」代表关闭成功!
        return
    }
    slog.Error("server failed", "err", err); os.Exit(1)
}()

ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()
<-ctx.Done()                                   // main 阻塞等信号

stat.Ready.Store(false)                        // ① 先摘流量:/readyz 返回 503
shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
srv.Shutdown(shutdownCtx)                      // ② 停止接新请求 + 等存量跑完(有上限)
```

**关闭顺序很重要**:先把 `/readyz` 翻成未就绪 → K8s 停止导流 → 再 `Shutdown` 处理存量。这样新流量在关闭前就被引走,做到**发版零 500**。

### `context` 的本质
> **`context` 是「取消信号」和「截止时间」在整条调用链上传递的标准方式。**
> A 启动了 B、B 又调了 C,上游说「不用干了」,信号能顺着 `ctx` 一路传下去让大家收手。

- `context.WithTimeout` = 给操作装「闹钟/刹车」
- ⚠️ **传一个已被 cancel 的 ctx 给 `Shutdown`,它会立刻返回,优雅关闭名存实亡** —— 所以关闭要用**全新的** `WithTimeout`

---

## 五、健康检查:存活 ≠ 就绪

| 探针 | 它在问 | 失败时 K8s 的动作 |
|------|--------|------------------|
| **Liveness** `/healthz` | 「你还活着吗?」 | **重启**容器 |
| **Readiness** `/readyz` | 「你现在能接客吗?」 | **摘除流量**(不重启) |

**两条铁律:**
1. **Liveness 只探自己进程的死活,绝不掺外部依赖。** 否则数据库一抖,所有 Pod 被判定"死亡"并重启 → **小故障被放大成重启风暴**。
2. **Readiness 才可以包含依赖。** 连不上数据库就别接流量,但别重启我。

**并发安全的就绪标志**:
```go
type ServeStat struct { Ready atomic.Bool }   // ❌ 普通 bool = 数据竞争
stat.Ready.Store(true) / stat.Ready.Load()
```
`atomic.Bool` **不能被值拷贝**(`go vet` 会报),所以 `ServeStat` 只能用指针传递。

---

## 六、生产加固

### 1. 服务器超时(默认全是 0 = 永不超时!)

| 字段 | 卡住的阶段 | 典型值 |
|------|-----------|--------|
| `ReadHeaderTimeout` | 读完请求头(**专治 Slowloris**) | 5s |
| `ReadTimeout` | 读完整个请求 | 10s |
| `WriteTimeout` | 写完响应 | 10s |
| `IdleTimeout` | keep-alive 空闲 | 60s |

**Slowloris 攻击**:攻击者每秒只发一个字节的请求头,永不发完。没有 `ReadHeaderTimeout` 时,几千个这种连接**零成本拖垮你的服务**。

### 2. 超时是「套娃」,内层必须比外层短
```
handler 内 context 超时  <  TimeoutHandler(请求级)  <  WriteTimeout(连接级)  <  优雅关闭超时
   最优雅、能取消工作          能返干净的 503          钝刀兜底
```
**两种超时的本质区别:**
- **连接级**(`WriteTimeout`):在 TCP 连接上设 deadline,到点**直接撕连接**。它**不通知 handler**(handler goroutine 还在傻跑),客户端拿到**空回复**。它是保护服务器的**钝刀**。
- **请求级**(`http.TimeoutHandler` / `context`):能返回**干净的 503 + 提示**,能通过 ctx **真正叫停下游工作**。

⚠️ 若两者数值相同,钝刀会先撕掉连接,优雅的 503 根本来不及发出去。

### 3. panic 恢复
```go
func withRecovery(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        defer func() {
            if err := recover(); err != nil {
                slog.Error("panic occur", "err", err)
                w.WriteHeader(http.StatusInternalServerError)
            }
        }()
        next.ServeHTTP(w, r)
    })
}
```

**关于 panic 必须搞清的三件事:**
1. **panic 不会跨 goroutine 传播** —— 它只在自己那个 goroutine 的栈上上抛,到不了 `main`。
2. **但任何 goroutine 里未被 recover 的 panic,杀死的是整个进程**(所有 goroutine 一起死)。
3. **`net/http` 已在每个请求 goroutine 顶上埋了 `recover()`** —— 所以 handler panic 时服务没崩,是它救的。但它的兜底很糙:**客户端拿到空连接而非 500**、日志级别不对、指标记不到。所以仍需自己写 `withRecovery`。

⚠️ **致命 caveat**:`net/http` 和你的 `withRecovery` **都只能兜住请求自己那条 goroutine**。你在 handler 里 `go func(){...}()` 开的新 goroutine 一旦 panic,**进程当场死**。规矩:**手动开的 goroutine 必须自己 recover**。

### 4. 中间件顺序决定行为
```go
withRecovery( withObservaility( http.TimeoutHandler(mux, 2*time.Second, "request timeout\n") ) )
//  最外层:兜住一切      rec 包住 TimeoutHandler,才能记录到它写的 503
```
- **`withRecovery` 放最外层** —— 它是最后一道防线,必须能兜住**所有**内层(包括其它中间件)的 panic
- **`TimeoutHandler` 放 observability 里层** —— 否则 observability 的 `rec` 看不到超时时写的 503,会错误地记成 200

---

## 七、可测架构(把逻辑从 `main` 里解放出来)

### 让 `main()` 变薄
```go
func newHandler(stat *ServeStat) http.Handler {
    mux := http.NewServeMux()            // 不用全局 DefaultServeMux
    mux.HandleFunc("GET /ping", pingHandler)
    mux.HandleFunc("GET /readyz", stat.readyzHandler)
    // ...
    return withRecovery(withObservaility(http.TimeoutHandler(mux, 2*time.Second, "...")))
}
```
> **凡是有逻辑、需要测的东西(路由装配、依赖注入),都要抽成独立函数,让 `main` 和测试都能调用。**

`newHandler` **接收 `*ServeStat` 参数**就是依赖注入 —— 正因如此,测试里才能拿到那个 stat 并 `Store(false)` 来测 503,而不需要真的跑一遍优雅关闭。

### 用 `httptest` 测 HTTP
```go
handler := newHandler(stat)
req := httptest.NewRequest("GET", "/ping", nil)
rec := httptest.NewRecorder()
handler.ServeHTTP(rec, req)      // ← 交给 mux!路由才会生效
```

**两种姿势,别混用:**
- **A(推荐)**:`NewRequest` + `NewRecorder` + `handler.ServeHTTP` —— 纯内存、无网络、毫秒级,**且带完整路由**
- **B(集成)**:`httptest.NewServer(handler)` 起真实服务器于随机端口,用 `http.Get(ts.URL+path)` 访问

⚠️ **要测路由就必须把请求交给 mux**(`handler.ServeHTTP`),而不是直接调具体 handler —— 选 handler 是 mux 的职责。

### 测什么、不测什么
**值得测**(有逻辑分支 / 坏了会出事):
- `readyz` 状态机(true→200 / false→503)
- 路由契约(`POST /ping`→405、未知路径→404)
- 失败路径(panic→500、超时→503)

**不值得花力气**:
- 无逻辑的直通(`healthz` 无脑返 200)
- **别测标准库和第三方库** —— 那是它们的责任

> **覆盖率是虚荣指标,「能挡住事故」才是目标。**

**只断言你自己产出的内容**:`/metrics` 的 body 由 Prometheus 生成且随数据变化,逐字比对又脆又没意义 —— 断言 `status==200` + `strings.Contains(body, "http_requests_total")` 就够;要精确验证数值再用 `prometheus/client_golang/prometheus/testutil`。

---

## 八、容器化

```dockerfile
# ----- Builder -----
FROM golang:1.25 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download                  # ← 先下依赖,让这一层能被缓存复用
COPY ./cmd/server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /app/logsvc ./cmd/server

# ----- Runtime -----
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /app/logsvc /usr/local/bin/logsvc
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/logsvc"]
```

**每一行都对应一个真实事故:**

1. **多阶段构建** —— builder 用 ~1GB 的完整工具链,运行阶段只拷一个二进制 → **最终 10.8MB**
2. **`CGO_ENABLED=0`** —— 编出**完全静态**二进制。忘了它,distroless/scratch 里没有动态链接器,启动直接报 `no such file or directory`
3. **`distroless` + `nonroot`** —— 没有 shell、没有包管理器,攻击面极小;非 root 运行
4. **`-ldflags="-s -w"`** —— 去符号表和调试信息,体积更小(代价:没法用 delve 调试)
5. **⚠️ `ENTRYPOINT` 必须用 exec 形式(JSON 数组)** —— 这**直接决定优雅关闭灵不灵**:
   - `ENTRYPOINT ["/app"]` → 你的二进制是 **PID 1**,`SIGTERM` **直达** → 优雅关闭生效 ✅
   - `ENTRYPOINT /app` → `/bin/sh` 是 PID 1,**默认不转发 SIGTERM** → 优雅关闭收不到信号,30 秒后被 `SIGKILL` 强杀 ❌
6. **层缓存** —— 先 `COPY go.mod go.sum` + `go mod download`,再拷源码。依赖没变时构建飞快
7. **路径坑** —— 产物**别叫 `/srv`**(它是 FHS 标准目录,已存在 → 二进制被塞进目录里,`ENTRYPOINT` 执行到目录报错);**Dockerfile 里 `~` 不会展开成家目录**,会变成一个字面名为 `~` 的目录。**容器里永远用绝对路径。**

---

## 九、贯穿始终的教训:单位 bug 出现了 **4 次**

| 出现在 | 症状 |
|--------|------|
| M1 日志 | `duration` 是裸纳秒数字,下游不知道单位 |
| M4 指标 | 微秒喂进 `_seconds` 直方图 → 全部溢出 `+Inf` |
| M4 修复后 | 字段叫 `duration_ms` 值却是 `.Microseconds()` |
| M5 超时 | `ReadHeaderTimeout: 5` = **5 纳秒** → 全服务拒连 |

> **编译器一次都拦不住** —— 因为 `time.Duration` 底层就是 `int64`,`5` 是完全合法的字面量。**编译器只查类型,不查语义单位。**

**由此固化的两条肌肉记忆:**
1. **`time.Duration` 永远写 `5 * time.Second`,绝不写裸数字。**
2. **字段名承诺的单位,必须和值严格一致**(`duration_ms` 就得是毫秒)。Prometheus 生态一律用**秒**。

---

## 十、其它高价值教训

- **「没报错」≠「成功」**:`w.Write` 返回 `nil` 只代表**成功交给了内核缓冲区**,不代表客户端收到了。这和 stage1 的「`-race` 绿 ≠ 没有 race」是同一个道理 —— **别把「我这端没报错」当成「用户体验没问题」**。
- **`Write` 失败时无法补救**:状态码和 header 早已发出,改不了也重发不了。此时唯一正确的动作是 **`log` 下来让失败可见**,而非试图修改响应。
- **接口要诚实**:`"/ping"` 匹配所有方法,让 `DELETE /ping` 也回 200,等于对客户端、监控、安全扫描撒谎。**只接受声明的方法,其余明确 405。**
- **验退出码要在真实交付物上验**:`go run` 会 fork 子进程,`Ctrl+C` 时它自己也被 SIGINT 打断,于是**报告退出码 1**,盖过你真实的 0。生产里是二进制直接被拉起,**测试就要按生产的样子测**。
- **`ListenAndServe` 阻塞且一旦返回必然出错** —— 「starting」日志要打在它**之前**;它返回就等于服务挂了。
- **`os.Exit` 会跳过所有 `defer`** —— 能自然 `return` 到 `main` 结尾就别用它。
- **测试必须 hermetic(自给自足)** —— 依赖「外面碰巧有个服务在 8080」的测试是**假测试**,在 CI 里永远失败。
- **`t.Fatalf` vs `t.Errorf`** —— 前置条件失败要用 `Fatalf`(立即停止);用 `Errorf` 会继续执行,导致后面 nil 解引用崩溃。**拿到 error 后绝不能碰返回值。**
- **golangci-lint 中写错的配置键会被静默忽略** —— revive 没有 `disable` 键,禁用单条规则要用 `issues.exclude-rules`。
- **门禁放 CI,不放 Makefile** —— `docker-build` 不必依赖 `test`;本地保持快速迭代,「测试通过才准发布」由 CI 流水线强制。
- **日志文案拼写也要准** —— 日志是用来 `grep` 的,`reponse` 拼错了,事故时按 `response` 搜根本搜不到。
- **把调试路由「升级」成测试** —— 与其留个 `/boom` 手动 curl,不如写成单元测试,把能力**永久焊死**在 `make test` 里。

---

## 十一、命令速查

```bash
make build          # go build -o bin/logsvc ./cmd/server
make run            # 本地起服务
make test           # go test -race ./...
make lint           # golangci-lint run
make vet            # go vet ./...
make docker-build   # docker build -t $(IMAGE) .   IMAGE ?= logsvc:dev
make docker-run     # docker run --rm -p 8080:8080 $(IMAGE)

make docker-build IMAGE=logsvc:v1    # ?= 允许外部覆盖

# 端到端验证
curl -i localhost:8080/ping          # 200 pong
curl -i -X POST localhost:8080/ping  # 405
curl -s localhost:8080/metrics | grep http_requests_total
docker stop <容器>                    # 发 SIGTERM,日志应出现 "shutdowned gracefully"
```

---

## 十二、最终成果

一个 **10.8MB** 的 distroless 镜像,内含:

✅ 方法受限路由(405) ✅ `log/slog` JSON 结构化日志 ✅ Prometheus 指标(四个黄金信号)
✅ `/healthz` + `/readyz` 分离 ✅ SIGTERM 优雅关闭(容器内已验证) ✅ panic 恢复
✅ 四层服务器超时 + 分层请求超时 ✅ 可测架构(`newHandler` 依赖注入)
✅ `make lint` / `make test -race` 全绿

---

**⬅️ 上一站**:[stage1](../stage1/stage1.md) —— Go 工程基础 CLI
**➡️ 下一站**:stage3 —— 部署进 Kubernetes,把 `/healthz`/`/readyz` 接上真实探针,验证滚动更新零掉包

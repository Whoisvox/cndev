# stage1 —— `logstat` 日志分析 CLI

> **目标**:用一个滚雪球式成长的 CLI 项目,打牢 Go 工程基础(而非只学语法)。
> **产物**:一个能并发统计多个日志文件 HTTP 状态码分布、有测试、有 lint、有正确退出码的命令行工具。

---

## 一、里程碑历程(每一关都由一个真实 bug 推动)

| 里程碑 | 做了什么 | **撞上的坑 / 学到的教训** |
|--------|---------|------------------------|
| **M0** | 读命令行参数 | 参数校验要在最前面 |
| **M1** | `bufio.Scanner` 逐行读文件 | 🔴 **漏了 `scanner.Err()` 检查** —— 循环正常结束不代表没出错,可能是读到一半 I/O 失败 |
| **M2** | `map[string]int` 统计状态码 | 🔴 **格式不对的行被静默丢弃** → 加 `Skipped` 计数器,让丢弃**可见、可数** |
| **M3** | 排序输出 Top-N | 🔴 **计数相同的行顺序随机** → 加次级排序键(状态码字典序),保证**确定性输出** |
| **M4** | `flag` 包加 `--top` 参数 | 🔴 **`--top -1` 直接 panic**(切片越界)→ 边界要**两头都守**,无意义的输入宁可拒绝也别悄悄纠正 |
| **M5** | 重构出 `CountStatus(io.Reader)` + table-driven 测试 | 🔴 **库函数里调 `log.Fatal`** —— 让 error 返回值形同虚设、无法测试、跳过所有 `defer` |
| **M6** | 并发处理多文件(goroutine + channel) | 🔴 三连坑:worker 里 `log.Fatalf` 拖垮全局 / 共享 `bool` 造成**数据竞争** / `received` 忘记自增导致永远退出码 1 |
| **M7** | `golangci-lint` + `Makefile` + 二进制 | `gocritic` 抓出 `len(x) <= 0` 应写 `== 0` |

---

## 二、Go 语言与标准库

### 核心语法/类型
- **struct 与方法**:`Result{Total, Skipped, Counts}` 聚合返回值,比多返回值更清晰、更好扩展
- **map**:`countmap[key]++` 对不存在的键也安全(零值 0);遍历顺序**随机**,这是排序确定性问题的根源
- **slice**:`statCountSlice[:n]` 切片操作前必须校验 `n` 的上下界
- **`defer`**:`defer file.Close()` 紧跟 `os.Open` 之后;注意 `os.Exit` 会**跳过所有 defer**

### 接口:`io.Reader` 是可测性的关键
```go
func CountStatus(r io.Reader) (Result, error)   // ✅ 接收接口
// 而不是 func CountStatus(path string)          // ❌ 绑死文件系统
```
接收 `io.Reader` 后,测试可以直接喂 `strings.NewReader(...)`,不需要造真实文件;还能用 `iotest.ErrReader(...)` **人为制造读取错误**来测错误分支。

### 错误处理
```go
if err := scanner.Err(); err != nil {
    return Result{}, err          // 库函数:返回错误,让调用者决定
}
```
**铁律:库函数返回 error,只有 `main` 有权决定退出。**

### 并发三件套
```go
ch := make(chan Result)
var wg sync.WaitGroup

for _, p := range paths {
    wg.Add(1)
    go func(p string) {
        defer wg.Done()          // ← 第一行就 defer,任何提前 return 都不会死锁
        // ... 出错就 log.Printf + return,不 Fatal
        ch <- result
    }(p)
}

go func() { wg.Wait(); close(ch) }()   // ← 「关闭三角」:独立 goroutine 负责收尾

for r := range ch {                     // ← channel 关闭后循环自动结束
    // 主协程串行合并,天然无竞争
}
```
**「关闭三角」模式**:workers 发送 → 一个独立 goroutine `wg.Wait()` 后 `close(ch)` → 主协程 `range` 消费。这是 Go 里最常用的扇入(fan-in)骨架。

### 测试
```go
tests := []struct {
    name    string
    input   io.Reader
    want    Result
    wantErr bool
}{...}

for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        got, err := CountStatus(tt.input)
        if (err != nil) != tt.wantErr { ... }
        if !reflect.DeepEqual(got, tt.want) { ... }
    })
}
```
- **table-driven**:加一个 case 只是加一行数据,不是复制一段代码
- **`testing/iotest`**:`iotest.ErrReader(errors.New("boom"))` 专门用来测**错误路径**
- **`t.Run`**:子测试独立命名,失败时能精确定位是哪个 case

---

## 三、SRE 工程原则(可迁移到任何语言/项目)

1. **永不静默丢弃数据。** 丢弃可以,但必须**可见、可计数**(`Skipped` 计数器)。生产中「悄悄少了一部分数据」是最难查的故障。

2. **故障要隔离在最小范围。** 一个坏文件不该拖垮整批处理 —— worker 里用 `log.Printf` + `return`,而不是 `log.Fatalf`。

3. **库返回错误,`main` 决定退出。** 在库函数里 `os.Exit`/`log.Fatal` 会:让错误无法处理、让代码无法测试、跳过所有 `defer`(资源泄漏)。

4. **输出必须确定。** 排序遇到相同权重时要有稳定的次级键,否则同样的输入两次运行输出不同 —— 这会让 diff、快照测试、人工比对全部失效。

5. **边界两头都要守。** 校验 `--top` 时不只要防止超过上限,也要拒绝负数。**无意义的输入宁可明确拒绝,也不要悄悄"纠正"。**

6. **共享可变状态就是数据竞争 —— 哪怕写的是同一个值。** 多个 goroutine 写同一个 `bool`,即使都写 `true`,也是货真价实的 race。改用「计数 + 主协程串行判断」消除共享。

7. **`-race` 绿 ≠ 没有 race。** race detector 只能抓到**实际发生了**的竞争。窗口窄的竞争跑一百次可能都遇不到。要验证并发安全,得**主动制造让竞争必然发生的时序**(加延迟、加压力)。

8. **验退出码,不只看 stdout。** 一个「打印了正确结果但退出码是 1」的程序,在 CI 和脚本里就是失败。`received++` 忘写那次,只有查退出码才能发现。

9. **`echo $?` 拿到的是管道最后一个命令的退出码**,不是你关心的那个命令的。测退出码要避开管道、包装器。

10. **把团队规范固化进配置。** `.golangci.yml` 让「代码风格」从口头约定变成**机器强制**,不再靠 code review 时人肉盯。

---

## 四、命令速查

```bash
make build     # go build -o bin/logstat .
make test      # go test -race ./...
make lint      # golangci-lint run
go vet ./...   # 标准库自带的静态检查

./bin/logstat --top 5 access.log other.log
echo $?        # 有文件失败时应为 1
```

---

## 五、代码状态

- **`CountStatus(io.Reader) (Result, error)`** —— 纯函数式的可测核心,不碰文件系统、不退出进程
- **并发结构** —— `main` 里 worker goroutine → channel → 主协程串行合并
- **失败语义** —— 单个文件失败:打日志 + 跳过 + 最终以**非零退出码**反映

---

**➡️ 下一站**:[stage2](../stage2/stage2.md) —— 把这套能力做成可观测的生产级 HTTP 服务。

# Stage5:平台工程 —— 从「我会部署」到「系统自己对」

> 环境:真实集群 v1.32.9(4 节点,master 192.168.5.241 无 Claude SSH,helm 操作由用户执行),harbor 192.168.5.174:80,ns `godev`。
> 前置:stage1–4 全部完成。helm 已熟(prometheus/loki/alloy/grafana 四套 chart 装过、values 排过、升级验证过)。
> 开题日期:2026-09-23。

## 0. 这个 stage 的灵魂

stage4 你练出了一条**验证四步链**:CM 生效配置 → 进程活着 → API 有内容 → 业务动作。每次改配置你都要人肉走一遍,而且它只能验证「现在对不对」,验证不了「和应该的样子对不对」。

**GitOps 就是把这条链自动化,并加上第五步:对账(reconcile)。**

- 期望状态(desired state)全部进 git,版本化、可审计、可回滚;
- 控制器(ArgoCD)持续对比 git 里的期望 vs 集群里的现实,漂移了要么报告要么自动纠正;
- 变更 = 一次 commit + 一次 sync,谁改的、改了什么、为什么改,git log 全知道。

**开题实证(M0 考古发现)**:stage4 记忆里那个 CrashLoopBackOff 87 天的 otel-collector,现在集群里**不存在**——何时被删、谁删的、为什么删,集群给不出任何答案(events 早过期,没有 CR 残留)。这就是没有 GitOps 的世界:**基建悄悄烂掉没人知道,悄悄消失也没人知道**。同样的事若发生在 ArgoCD 管理下,删除是一次带作者和 message 的 commit,或者是一次会被立刻发现的 drift。

另一个考古发现:`opentelemetry-operator-system` ns 里**双 operator 并存**——`opentelemetry-operator-controller-manager`(0.150.0,134d,疑似裸装/裸 helm)与 helm release `opentelemetry-stack` 的 operator(0.144.0,127d)。两次安装,旧的没清理,两个 controller 抢同一批 CRD。这是「手动部署无对账」的第二个活教材,M3 处理。

## 1. 里程碑总览

| M | 主题 | 交付物 |
|---|---|---|
| M0 | 考古 + 全集群资产盘点 + repo 设计 | info.md 盘点表、git repo 骨架、设计决策记录 |
| M1 | ArgoCD 安装 + 第一个 Application | alert-receiver 被 GitOps 管理,亲手制造一次 drift 看它自愈 |
| M2 | 收编存量(adopt) | prometheus/loki/alloy/grafana/jaeger values 进 git;顺路落地 LogsvcNoTraffic + inhibit_rules |
| M3 | Tracing 复活(补 stage4 M5) | 清理双 operator,otel-collector 以 GitOps 对象身份重建,logsvc 插桩,trace→log→metric 三支柱贯通;slo.md 方案 B(/internal/ping)一起做 |
| M4 | CI/CD | push 代码 → 构建镜像 → 改 git 里的 tag → ArgoCD 自动 sync(把 stage4「同 tag 不拉新镜像」的坑用不可变 tag 治死) |
| M5 | controller-runtime Operator | 写一个真 Operator(reconcile loop),理解 ArgoCD 本身就是个 controller |
| (可选) | Terraform | 裸金属集群没有 IaaS API,Terraform 降级为「读文档+本地练习」,不硬凑 |

依赖关系:M1 需要 M0 的 repo;M2 需要 M1 的 ArgoCD;M3/M4 需要 M2 的收编完成。M5 相对独立,放最后。

## 2. 贯穿母题(从 stage1–4 继承,本 stage 换新形态)

- **「写了≠生效」→「生效了≠和 git 一致」**:验证四步链升级为五步,第五步是 ArgoCD 的 sync status。
- **权威会过期,集群不会**:M0 考古已经又赢一次(collector 消失)。GitOps 是这条教训的制度化——让 git 成为唯一权威,集群只是它的投影。
- **对账是免费保险**:stage3 对账指标(+Inf=2M),stage4 对账日志(30/30/20),stage5 对账整个集群(git vs live)。
- **单一事实源**:stage4 是 recording rules,stage5 是 git repo。
- **tag 是指针不是内容**:stage3/stage4 两次被可变 tag 咬,M4 用 CI 产不可变 tag 终结它。

---

# M0:考古 + 盘点 + repo 设计

**目的**:GitOps 的第一课不是装 ArgoCD,是**搞清楚现在有什么**。你无法收编你数不出来的东西。M2 的 adopt 会以这张盘点表为清单,漏盘的资源就是未来的 drift。

## T1 全集群资产盘点(动手)

产出一张表(写进 `stage5/info.md`),覆盖所有 ns 的:**workload(deploy/sts/ds)、来源(helm release / 裸 apply)、归谁管(平台 or 业务)**。

手法提示:
- helm 来源:`kubectl get secrets -A -l owner=helm` 剥掉 `.v<N>` 后缀去重(release 名 = secret 名去掉 `sh.helm.release.v1.` 前缀);
- 裸 apply:workload 上没有 `app.kubernetes.io/managed-by: Helm` 注解的就是;`kubectl get deploy -A -o custom-columns=...` 批量看;
- 别忘了非 workload 的「配置资产」:你手写的 ConfigMap(logsvc 的 config、loki 规则 CM `logsvc-log-alerts.yaml`)、Secret、NodePort svc、harbor 里的镜像清单——M2 收编时它们全要进 git;
- 两个 otel operator 分别什么来历(查各自 deploy 的 label/annotation,判断谁是 helm 装的谁是裸装的)。

**验收**:表里每一行都能回答「它是哪来的、改动它要去哪里改」。数出来 helm release 应该是 8 个(见 info 里我的考古记录),裸 apply 的至少有 logsvc、alert-receiver、若干 CM——如果你找出更多,说明我盘漏了,以你的为准。

## T2 repo 结构设计(决策,写设计稿)

GitOps 的 repo 长什么样是有讲究的。你要产出 `stage5/repo-design.md`,拍板这几个 ⚖️:

1. **单 repo 还是多 repo**(平台基建 vs 业务应用)?
2. **目录结构**:按 ns 分?按应用分?chart 和 values 分开放还是放一起?
3. **helm chart 用上游的还是 vendored(拷进 repo)**?——「chart 是指针(版本可漂移)还是内容(冻结在 repo 里)」,和镜像 tag 是同一个问题的 helm 版;
4. **裸 apply 的 manifest 收编成什么**:继续裸 YAML?还是趁机 Kustomize 化?(前置知识:kustomize 的 base/overlay 模型,一句话:helm 是模板引擎,kustomize 是补丁引擎,两者可叠用);
5. **git 托管在哪**:候选 A——master 上建 bare repo + SSH 访问(零新组件,ArgoCD 配 ssh key 即可);候选 B——装 Gitea(有 Web UI,接近生产 GitLab 体验,但它自己谁来管?鸡生蛋:第一个不由 GitOps 管理的 GitOps 基础设施)。我的倾向:**A 起步**(M0–M2 用 bare repo,减少活动部件),M4 做 CI/CD 时如果需要 PR 流程再评估 Gitea——但你写理由,可以推翻我。

**验收**:设计稿每个决策有「选择 + 理由 + 代价」,像 slo.md 那样。特别关注:目录结构要能让 M2 收编时「一个 release 一个目录」直接对号入座,别设计一个收编时要大改的结构。

## T3 考古报告(动手,可选但推荐)

collector 是怎么消失的,还能抢救出多少线索:
- master 上 `history | grep -iE "otel|collector"`(你的 shell 历史是集群没有的那份记忆);
- `helm list -A | grep -i otel` + `helm history opentelemetry-stack -n opentelemetry-operator-system`(如果 collector 曾是这个 release 的一部分,history 里会有卸载/升级记录);
- operator 的日志(127d 的 Pod,日志可能轮转过,能捞多少捞多少);
- `kubectl get events -A` 肯定已过期(默认 1h),验证这一点本身也是结论。

**验收**:一段话写清「找到的线索 + 找不到的原因」。找不全没关系——**「集群对 87 天前发生的事失忆」这个事实本身,就是 T2 设计稿里『为什么需要 git』一节的论据**。

## M0 前置知识清单

- GitOps 三原则:期望状态版本化、自动拉取应用、持续对账(区别于 CI/CD 的 push 模型:GitOps 是集群**拉**,防火墙不用为部署开口子);
- ArgoCD 核心对象预告(动手在 M1,概念先混脸熟):Application(git 路径 ↔ 集群目标)、sync policy(automated + prune + selfHeal)、App of Apps;
- kustomize base/overlay(T2-4 决策需要);
- helm release 的存储形态(secret `sh.helm.release.v1.<name>.v<N>`,T1 直接用到)。

## M0 之后的路标(预告)

M1 第一个被 GitOps 管理的对象选 **alert-receiver**(不是 logsvc):它最简单(一个 Deployment + 一个 svc,无 PVC 无 chart),而且它是 stage4 的产物,你对它了如指掌——第一次 sync 出问题你能立刻分辨是 ArgoCD 的问题还是 manifest 的问题。收编顺序永远是**从简单到复杂、从边缘到核心**,生产 adopt 同理。

另外一件事不用等 M2:**LogsvcNoTraffic(stage4 唯一遗留)现在就手动部署掉**——规则全文在 stage4/info.md T4,加进 master 的 prometheus values + helm upgrade,十分钟的事,而且当前零流量稳态就是活体验证样本(1h 窗口 + for 5m 后应 FIRING)。M2 收编时它会以「已经在集群里」的状态被 adopt——你会亲眼看到 adopt 时 git 和 live 的第一次 diff。

# T2 repo 设计稿(初学者版:每个决策先讲前置知识,再给推荐)

> 这份文档回答一个问题:**「git 里那个 repo,到底装什么、怎么摆」**。
> 它为什么重要:GitOps 里 repo 就是「期望状态」本身,ArgoCD 逐目录对账。repo 结构 = 你集群的目录页。结构设计错了,以后每次收编/查找/回滚都要付利息。
> ⚖️ = 决策点。每个都给了推荐+理由+代价,你可以推翻,但像 slo.md 一样要写自己的理由。

## 决策 1:单 repo 还是多 repo? ⚖️
同意

**前置知识**:生产公司常见拆法——infra repo(平台组件:监控、ingress、cert-manager)和 app repo(业务代码+部署清单)分开,因为**权限边界不同**(业务开发能改 app repo,只有平台组能改 infra repo),CI 触发条件也不同(app push 触发构建,infra 改动走审批)。

**推荐:单 repo,目录区分平台/业务。**
理由:你一个人,权限边界是伪需求;单 repo 让「全集群期望状态」有一个入口,ArgoCD 只需对接一个 git 地址;多 repo 的学习成本(跨 repo 引用、版本对齐)留到真有第二个协作者再付。
代价:以后真拆 repo 时,Application 的 path 要改一次(可接受,ArgoCD 里就是改个字段)。

## 决策 2:目录结构 ⚖️
同意

**前置知识**:两种主流流派——
- **按应用**(app-of-apps 友好):`apps/<name>/` 一个应用一个目录,里面放它的一切;
- **按集群/环境**(多集群公司用):`clusters/prod/`、`clusters/staging/` 各自引用应用。

你只有一个集群,「按集群」维度是空的,直接用「按应用」。

**推荐结构**(对着 T1 盘点表画的,收编时一行对一个目录):

```
k8s-gitops/
├── bootstrap/                  # ArgoCD 自己 + root Application(鸡生蛋问题的答案,见决策 5)
├── platform/                   # 平台组件(收编自 helm values / 裸 apply)
│   ├── prometheus/
│   │   ├── Chart.yaml          # 依赖声明:仓库+版本(见决策 3)
│   │   └── values.yaml         # 你 master 上那份的 git 化
│   ├── loki/
│   ├── alloy/
│   ├── grafana/
│   ├── metrics-server/
│   ├── opentelemetry-operator/ # 0.159.0 那个裸装的,M3 收编
│   └── tempo/                  # M3 新建:生下来就在 git 里
├── apps/                       # 业务应用
│   ├── logsvc/
│   │   ├── base/               # deploy+svc+cm(见决策 4)
│   │   └── kustomization.yaml
│   └── alert-receiver/         # M1 第一个收编对象
│       ├── base/
│       └── kustomization.yaml
└── README.md                   # 这份结构的说明+收编状态表(哪些已进 git 哪些还裸奔)
```

理由:三个顶层目录对应三种生命周期——bootstrap(装一次)、platform(低频、谨慎)、apps(高频、随便折腾);ArgoCD 里一个目录一个 Application,粒度清晰。
代价:jaeger/kraft/redis 这些「别人的」**不进 repo**(T1 已标注),repo 只承诺覆盖「归你管的」——README 里写清边界,不然半年后你自己都疑惑为什么 redis 不在里面。

## 决策 3:helm chart 进不进 repo(vendored)? ⚖️
同意

**前置知识**:helm 装 chart 有两种引用方式——
- **指针**:values.yaml 在 git 里,chart 本体在远端仓库(prometheus-community),装的时候 `helm repo add` 现拉。风险:上游同版本号重推(罕见但发生过)、网络断了装不了、**你没法审计「当时装的到底是什么」**;
- **内容(vendored)**:把 chart 整个拷进 repo。优点:git 里的就是全部真相,和「镜像 tag 是内容不是指针」同一个哲学;缺点:repo 变大,升级 chart 变成一次显式的 vendor 更新。

helm 官方对此的折中是 **Chart.yaml 依赖声明**:repo 里只放一个 Chart.yaml 写明「prometheus chart 29.19.0 来自 prometheus-community」+ 你的 values.yaml。chart 本体不进 git,但**版本被钉死**(29.19.0 不是 latest),指针变成了「带校验和的指针」。

**推荐:Chart.yaml 钉版本,不 vendor chart 本体。**
理由:chart 上游版本号是不可变的(语义化版本+仓库托管,重推同版本号属于事故),钉住版本后「指针漂移」风险已基本封死;vendor 进来几 MB×6 个 chart 的 YAML 对你的 repo 是纯噪音。你的 values.yaml 才是你写的东西,git 该管的是它。
代价:离线安装时 helm 还是要能连 chart 仓库(你的集群本来就能拉公网镜像,不构成新约束)。**例外**:如果某个 chart 你魔改过 chart 本体(你目前只改过 values,没有),那个必须 vendor。

## 决策 4:裸 manifest 收编成什么形态? ⚖️
同意

**前置知识**:kustomize 一句话模型——`base/` 放原始 YAML(deploy/svc/cm),`kustomization.yaml` 是目录清单,可选 `overlays/` 做环境差异补丁(dev 1 副本/prod 3 副本)。**helm 是模板引擎(生成 YAML),kustomize 是补丁引擎(修改 YAML)**,kubectl 原生支持(`kubectl apply -k`),不用装任何东西。ArgoCD 对两者都原生识别:目录里有 Chart.yaml 走 helm,有 kustomization.yaml 走 kustomize,都没有就裸 apply 目录里所有 YAML。

**推荐:logsvc/alert-receiver 用「目录裸 YAML」起步,不急着 Kustomize 化。**
理由:你只有一个环境,overlay 维度是空的——为不存在的多环境预付抽象成本,正是 stage4「简化形态要显式」的反面教材(这里是过度设计要显式)。目录里直接放 deploy.yaml/svc.yaml/cm.yaml,ArgoCD 裸 apply,最简路径。
代价:将来要 dev/staging 两环境时,把目录内容挪进 `base/` + 加 overlay,一次小重构。**预埋**:目录结构现在就叫 `base/`(如决策 2 所示),挪的时候不用改名。

## 决策 5:git 托管在哪? ⚖️
同意

**前置知识**:ArgoCD 连 git 的三种方式:HTTPS(token)、SSH(key)、或本地目录(仅测试)。还有个**鸡生蛋问题**:ArgoCD 管一切,那 ArgoCD 自己的安装清单谁管?答案叫 **bootstrap**:repo 里留一个 `bootstrap/` 目录,ArgoCD 装好后**第一个 Application 指向它自己**——从此 ArgoCD 的升级/配置也走 GitOps(自己管自己,这叫 self-management,是 GitOps 成熟度的标志之一)。

候选:
- **A. master 上 bare repo + SSH**:master:/data/gitops/k8s-gitops.git,你从工作机 push,ArgoCD 用 ssh key 拉。零新组件。
- **B. 集群里装 Gitea**:有 Web UI/PR/issue,接近公司 GitLab 体验。但:① 多一个要维护的服务(它自己也要 PVC、备份、升级);② 鸡生蛋更尖锐——Gitea 是「装 ArgoCD 之前就必须存在」的基础设施,它永远无法被 ArgoCD 管理它自己的诞生;③ M4 做 CI 时 Gitea Actions 确实是加分项。

**推荐:A 起步。**
理由:活动部件最少,而 stage5 每个里程碑已经各有新东西要学(M1 ArgoCD、M3 Tempo、M4 CI、M5 Operator),git 托管不该也占一个学习位。bare repo 还有个隐性教学价值:你会被迫理解 git 的本质(push/fetch/分支都是文件),而不是被 Web UI 惯着。
代价:没有 PR/code review 流程(单人项目伪需求)、M4 的 CI 要另想办法触发(bare repo 可以用 post-receive hook 触发构建——这本身是好教材)。如果 M4 走到一半你觉得 hook 太原始,再装 Gitea 不迟,repo 迁移就是一条 `git push --mirror`。

## 决策 6(我加的):Secret 怎么进 git? ⚖️
同意

**前置知识**:logsvc-secret(API_TOKEN/DB_PASSWORD)、harbor 拉镜像凭证都是 Secret。**明文进 git 是事故**(git 历史永久保留,推过一次就删不干净)。业界方案:SOPS+age(文件级加密,git 里存密文)、SealedSecrets(集群内 controller 解密)、External Secrets(从 Vault 拉)。

**推荐:M1 阶段 Secret 不进 repo,继续手动 kubectl apply;M2 收编到 Secret 时用 SOPS+age。**
理由:SOPS 是「加密的 YAML 直接进 git」,ArgoCD 原生支持(配一次解密插件),单人集群不需要 Vault 那种重型方案。但 M1 先别碰——第一个 Application 就带上加密链路,出问题时你分不清是 ArgoCD 的问题还是 SOPS 的问题(和「第一个收编对象选最简单的 alert-receiver」同一个理由)。
代价:M1→M2 之间 repo 覆盖不完整,README 收编状态表里标「Secret:manual」即可。
⚠️ **顺带的现行事犯**:`grafana-cloud-auth` 那个 secret 里是**真实的 Grafana Cloud API key**(T1 扫描发现)。它现在躺在集群里没进 git 没问题,但你要确认它是否还需要——如果不用了,删掉;如果要用,M2 时走 SOPS。别让它成为第一个「不小心 commit 进 repo」的凭据。

## 汇总(推荐版一屏)

| 决策 | 推荐 | 一句话理由 |
|---|---|---|
| 1 单/多 repo | 单 repo | 一个人,权限边界是伪需求 |
| 2 目录结构 | bootstrap/platform/apps 三分 | 对应三种变更频率 |
| 3 chart | Chart.yaml 钉版本,不 vendor | 版本号不可变,values 才是你的 |
| 4 裸 manifest | 目录裸 YAML,预埋 base/ 命名 | 单环境别预付 overlay 抽象 |
| 5 git 托管 | master bare repo + SSH | 活动部件最少,M4 hook 触发 CI |
| 6 Secret | M1 不进 git,M2 用 SOPS+age | 第一个 Application 别带加密链路 |

**验收**(M0-T2 的完成定义):你对每个 ⚖️ 写一行「同意/不同意+理由」(不同意就改推荐结构);然后在 master 上把 bare repo 建出来、工作机 clone、把 T1 盘点表里「归你管」的资源按决策 2 的目录摆成**空骨架**(只有目录和 README,内容 M1/M2 才填)——骨架 push 上去,M0 收官。

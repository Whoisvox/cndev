# stage5 工作日志

## M0 考古记录(Claude 预盘,2026-09-23,以你的复核为准)

- 全集群 82 Pod 全部 Running,**otel-collector 已不存在**(stage4 记忆里的 CrashLoopBackOff 87 天消失了),无任何 OpenTelemetryCollector CR。
- helm releases(8 个):`jaeger`(gmicrosvc-demo)、`prometheus`/`loki`/`alloy`/`grafana`(godev)、`kraft`、`metrics-server`(kube-system)、`opentelemetry-stack`(opentelemetry-operator-system)。
- **双 operator 并存**:ns `opentelemetry-operator-system` 里 `opentelemetry-operator-controller-manager`(0.150.0,134d,deploy 无 helm 痕迹,疑似裸装)与 `opentelemetry-stack-opentelemetry-operator`(0.144.0,127d,helm release)。
- jaeger 单副本 Running 134d(restart 1),在 gmicrosvc-demo ns。
- 节点:master + node1/2/3,v1.32.9,Ubuntu 24.04。SC 有 openebs-hostpath/openebs-device 等 6 个。

## T1 资产盘点表(Claude 扫描代填,2026-09-24;「归谁管」列是建议值,你来改)

### A. helm releases(7 个)——改动入口:master:/data/k8s/helmCharts/ 下的 values + helm upgrade

| release | ns | chart 产物(节选) | 归谁管 |
|---|---|---|---|
| prometheus | godev | prometheus-server(STS+PVC 8Gi)、alertmanager(STS+PVC 2Gi)、pushgateway、kube-state-metrics、node-exporter(DS)、recording/alerting rules、AM 路由 | 平台-观测 |
| loki | godev | loki(STS+PVC 20Gi)、canary(DS)、两个 memcached 缓存、ruler 配置 | 平台-观测 |
| alloy | godev | alloy DS(日志采集管道) | 平台-观测 |
| grafana | godev | grafana 13.1.1 | 平台-观测 |
| jaeger | gmicrosvc-demo | jaeger all-in-one 1.53.0 | 别人的(M3 再评估去留) |
| kraft | kraft | kafka broker×3 + controller×3(6 PVC)+ kafka-exporter | 别人的 |
| metrics-server | kube-system | metrics-server | 平台-系统 |

### B. 裸 apply workload(kubectl,无 helm 指纹)

| 资源 | ns | 镜像 | 归谁管 |
|---|---|---|---|
| deploy/logsvc(2 副本) | godev | harbor godev/logsvc:v12 | **业务(M1/M2 收编)** |
| deploy/alert-receiver | godev | harbor godev/alert-receiver:v2 | **业务(M1 第一个收编)** |
| deploy/opentelemetry-operator-controller-manager | opentelemetry-operator-system | harbor library/opentelemetry-operator:0.159.0(generation=3,kubectl apply) | **平台-观测(M3 收编)** |
| deploy/cert-manager ×3 | cert-manager | v1.19.2 | 平台-系统(不收,别人的地盘) |
| deploy/ingress-nginx-controller | ingress-nginx | v1.13.0(daocloud 镜像) | 平台-系统(不收) |
| metallb(controller+speaker DS)、calico、coredns、kube-proxy、openebs ×5 | kube-system/metallb-system/openebs | 集群底座 | 平台-底座(不收,动了会死) |
| kubernetes-dashboard ×2 | kubernetes-dashboard | v2.7.0 | 别人的 |
| kafka-ui/mongo/mysql/pg/rabbitmq/mariadb/redis ×3 套 | default | 各种 latest 镜像 | 别人的(历史遗留,不碰) |
| redis(6 副本 STS)+redis-trib | redis | redis:6.2.3 | 别人的 |

### C. 配置资产(非 workload,M2 收编的重点)

| 资源 | ns | 内容 | 归谁管 |
|---|---|---|---|
| cm/logsvc-config | godev | 9 个 env key(LISTEN_ADDR、超时×6、LOG_LEVEL、FAIL_RATE) | 业务 |
| secret/logsvc-secret | godev | API_TOKEN、DB_PASSWORD | 业务(**进 git 的方式见 repo-design 决策 6**) |
| cm/logsvc-log-alerts(loki_rule=1) | godev | LogsvcErrorBurst 规则 | 业务 |
| secret/harbor-local174(dockerconfigjson) | godev | harbor 拉镜像凭证 | 平台 |
| secret/grafana-cloud-auth | opentelemetry-operator-system | **Grafana Cloud 的 API key + OTLP endpoint**(来历:疑似 otel 试验残留,集群无任何 elastic/otel CR 在用;⚠️ 是真实凭据,别提交进 git,M0 结束时问自己还要不要) | 待查 |
| ingress/prometheus(prometheus.demo.local → 192.168.0.220) | godev | 地址已失效(metallb 现在的池不含它) | 僵尸候选 |
| PVC:prometheus-server 8Gi / storage-loki-0 20Gi / storage-prometheus-alertmanager-0 2Gi | godev | openebs-hostpath | 随 helm 收编 |

### D. NodePort 端口占用表(godev 相关)

| 端口 | svc | 来源 |
|---|---|---|
| 30222 | logsvc | 裸 |
| 31969 | alert-receiver | 裸 |
| 31181 | grafana | helm |
| 31725/31363 | loki(3100/9095) | helm |
| 32338 | alertmanager | helm |
| 31609 | alloy(UI 12345) | helm |

### E. harbor 镜像清单(匿名只能看 project 列表,godev project 存在;repo 明细需登录查,待补)

### 扫描顺带发现
- **孤儿 CRD 家族**:elastic ×10(集群无任何 elastic CR)、`applications.core.local.local`(2026-04-27,组名就是乱打的)、grafana agent/alloy 旧版 CRD、gateway-api——都是装过又删的残留。CRD 是 helm uninstall 默认**不删**的(怕连带删掉 CR 数据),这是 helm 的保守设计。M0 不处理,记账。
- LogsvcNoTraffic 仍未部署(prometheus-server CM 里 0 次出现)。

## T2 repo 设计稿 → 见 repo-design.md(初学者版,每个决策带前置知识教学;⚖️ 处我给了推荐+理由,你可推翻) 

## T3 考古报告(我来写)

### 09-23 清理 otel operator 的「半删」现场(Claude 复核记录)

**做对的**:留下的是 0.159.0(harbor 镜像,新版),删掉的是 helm 装的旧版 0.144.0;operator 日志显示 4 个 controller 全部正常 Starting workers。

**没删干净的**(helm uninstall 卡在 `uninstalling` 状态,release secret 还挂着):
| 遗留物 | 详情 | 风险 |
|---|---|---|
| 2 个 webhook configuration(cluster-scoped) | `opentelemetry-stack-...-mutation`/`-validation`,指向已死的 svc(endpoints 无) | failurePolicy=Ignore 所以不阻塞,但全集群**每次 Pod 准入**都多一次徒劳调用 |
| clusterrole ×5 + clusterrolebinding ×3 | `opentelemetry-stack-*`,其中一个 binding 指向已不存在的 SA | 权限面垃圾,审计时说不清归谁 |
| svc ×2 + leader-election role/binding | ns 内死 svc | 小 |
| release secret(status=uninstalling) | helm 的「账」没结清 | `helm list` 里永远挂着 pending-uninstall |

**待验证 🟡**:CRD 是 2026-05-12 建的,operator 却跳到 0.159.0——升级时 CRD 有没有一起更新?operator 与旧 CRD 不兼容时可能静默不认识新字段。验证:`kubectl explain opentelemetrycollector.spec` 看有没有新版字段 + operator 启动日志有无 CRD 警告。

**补刀结果(09-24,Claude 复核)**:按清单执行的删除全部生效——webhook/clusterrole/clusterrolebinding/死 svc/leader-election role/release secret 全清,helm release 从 8 个回到 7 个,`opentelemetry-stack-*` 前缀的 cluster 级资源归零。**三件漏网小鱼**(手动删除清单只覆盖了给定的命令):
1. secret `opentelemetry-stack-opentelemetry-operator-controller-manager-service-cert`(死 operator 的 webhook 证书);
2. secret `grafana-cloud-auth`(来历不明,M0-T1 盘点时查它属于谁);
3. `delete-resources-sa/role/rolebinding`(清理时的临时脚手架)。
教训:**「删干净」的验收标准不是「执行完命令」,是「按前缀 grep 集群归零」**——`kubectl get <kind> -A | grep opentelemetry-stack` 扫一遍,剩下的就是清单没覆盖的。

## T4 LogsvcNoTraffic 部署完成(09-25,stage4 唯一遗留清零)
Q1：为什么selfHeal开，prune关？
*防止集群内对应资源状态更改时，始终以当前git版本为准，将集群版本保持和git版本一致*
*当git里文件预期外删除时，连带集群内的资源也一起删除，该资源应该留下来等待核验，而不是直接删掉*

- 部署方式:替换材料 `stage5/no-traffic-alerting-rules.yml`(live CM 原样导出+追加新规则,避免手抄),用户在 master 替换 values + helm upgrade。
- 部署前预演:表达式两条原料实测(rate=0 ✓、ready pod=2 ✓),完整表达式返回 0 = 满足触发。
- 验收(Claude 复核通过):/api/v1/rules 三条规则,FastBurn/SlowBurn inactive,**LogsvcNoTraffic firing**;alert-receiver 01:17:04 收到 WARN firing(severity=ticket → webhook-ticket 路由,stage4 M2 的路由树第三次实战)。
- 语义确认:它会一直 firing 到有流量为止——设计行为(零流量在 dev 是常态,ticket 级只是灯亮不吵人)。**stage4 假恢复的盲区从此有灯。**

## M0 收官检查

- [x] T1 盘点表(归谁管列已确认为建议值)
- [x] T2 repo 设计稿(六个 ⚖️ 全部「同意」签收;实操并入 M1)
- [x] T3 考古报告(半删现场+补刀记录;helm history 线索已随 release secret 删除关闭——先考古后清理的顺序教训)
- [x] T4 LogsvcNoTraffic(见上)
- 待办移交 M1:建 bare repo(改在 harbor 机 192.168.5.174,理由见下)、ArgoCD 安装、alert-receiver 收编(`kubectl diff` 已验证本地 YAML 与 live **零漂移**,可直接 adopt)、清理 ns 里三件漏网小鱼、grafana-cloud-auth 处置。

## M1
1. **为什么selfHeal 5s, git变更要个120s?**
*一个是argoCD watch k8sAPI，和所有controller一样用informer，长连接推送，事件驱动，几乎实；一个是argoCD去轮询git repo，通过定时git fetch比对conmmit hash，有轮询周期，默认是180s，在cm的timeout.reconciliation和timeout.reconciliation.jitter(随机抖动，防止所有app同一秒一起fetch，惊群)时，现在主流都用webhook回调，轮询做兜底*

2. **为什么drift(不改git)后，第一次变更是kubectl scale，从1->0，第二次是被argoCD的selfHeal掰回，但deploy events里两次都是deployment-controller?**
*deploy的manageFileds字段的manager可以看到，角色从kubectl-client-side-apply ==> argocd-controller ==> kube-controller-manager*
--- 不过我不明白为什么最后manager不是deploymnet-controller?
*kube-controller-manager是个go进程，里面跑了几十个controller线程(deployment/replicaset/node/job...)，manageFileds记进程身份，不是进程内的哪个goroutine；而events里的"deployment-controller"是这个进程自报的组件名*

3. **为什么prune先关?**
防止git意外指空或者git被删，导致的集群也同步删除，关掉prune意味着集群会一直留着该资源知道手动清理，相当于第二次机会。生产的关键资源(STS+PVC)还会单独加sync-options: Prune=false注解显示地永久豁免。

## T0 收编alloy进argoCD
把alloy收编到argocd，第一次踩坑是application的模式被显示设置成了directory模式而非Helm模式，该模式会将目录下所有yaml当manifest渲染，Chart.yaml被无视，而values.yaml里没有apiVersion/kind这些，不是k8s资源，被静默跳过，不报任何错，零manifest -> 零资源 -> 恒绿灯。后面采用多源application，一个自己的values.yaml，一个helm官方的chart，固定一个chart版本，通过$values接线。

**adopt撞孤儿CRD**
第二个坑是历史坑，crd之前低版本的storeVersions还存在etcd,新版本的crd的version里必须要包含它，如没有，则k8s的apiserver会拒绝更新，以保护etcd中存在的低版本数据可以被正确解析，即使低版本的crd集群里零cr存在。
解决办法是到values.yaml里取消创建application时候连带创建crd，不应该让namespace-scope的app资源拥有crd这种属于cluster-scope的越界级别。

## T1 收编prometheus loki grafana(缺pvc)
*argo UI建multi-source helm app不可靠，需要手动走kubectl*
## Use own Tr, MaxIdleConnsPerHost > 200, but cpu.limit=256m
** grafana中，container_cpu_cfs_periods_total，依次为5c/s 0 1 1 5 1，container_cpu_cfs_throttled_period_total,依次为3c/s 0 0 0 3 0，container_cpu_cfs_throttled_seconds_total，依次为0.25 0 0 0 0.25 0， 不知道是不是我采样频率太低，时间范围是23:20到23:32，只有6次样本**
root@vox-174:stage3# go run ./cmd/stress/main.go -c 100 -n 1000 -url=http://192.168.5.242:30222/ping -token token
并发数: 100
每并发请求: 1000 次
总请求数: 100000
总耗时: 56.45109379s
========================================
成功: 100000, 失败: 0
Successs QPS: 1771.44
P50: 16.099362ms
P95: 120.921018ms
P99: 295.902004ms

状态码分布:
  HTTP 200: 100000 次
root@vox-174:stage3# 

## After moderate cpu.limit from 256m to 1000m
root@vox-174:stage3# go run ./cmd/stress/main.go -c 100 -n 1000 -url=http://192.168.5.242:30222/ping -token token
并发数: 100
每并发请求: 1000 次
总请求数: 100000
总耗时: 13.601201213s
========================================
成功: 100000, 失败: 0
Successs QPS: 7352.29
P50: 6.391528ms
P95: 42.98998ms
P99: 64.147838ms

状态码分布:
  HTTP 200: 100000 次

## stress cause CrashLoopBackoff, i don't know why
** it should be gc again and agin for the GOMEMLIMIT=0.72M, but the Mem in grafana up to 3.9M it start to fall, and rise again to 4M, and fall agian to 3.73M**
root@vox-174:stage3# kubectl get po -o wide -n godev -l app=logsvc -w
NAME                      READY   STATUS             RESTARTS      AGE   IP              NODE    NOMINATED NODE   READINESS GATES
logsvc-5589ddb8bd-7kv7l   0/1     CrashLoopBackOff   3 (15s ago)   9h    10.244.135.37   node3   <none>           <none>
logsvc-5589ddb8bd-7kv7l   0/1     Running            4 (22s ago)   9h    10.244.135.37   node3   <none>           <none>
^Croot@vox-174:stage3# kubectl get po -o yaml -n godev -l app=logsvc
apiVersion: v1
items:
- apiVersion: v1
  kind: Pod
  metadata:
    annotations:
      cni.projectcalico.org/containerID: 9504803d2e16e8ca34461492b73da83b497de1b51bbffa1fe85c483bc33d1bd7
      cni.projectcalico.org/podIP: 10.244.135.37/32
      cni.projectcalico.org/podIPs: 10.244.135.37/32
      kubectl.kubernetes.io/restartedAt: "2026-07-31T09:27:08+08:00"
    creationTimestamp: "2026-08-05T15:55:33Z"
    generateName: logsvc-5589ddb8bd-
    labels:
      app: logsvc
      pod-template-hash: 5589ddb8bd
    name: logsvc-5589ddb8bd-7kv7l
    namespace: godev
    ownerReferences:
    - apiVersion: apps/v1
      blockOwnerDeletion: true
      controller: true
      kind: ReplicaSet
      name: logsvc-5589ddb8bd
      uid: 973aa00b-ed58-4f3f-bd39-87a1d502eec8
    resourceVersion: "72441543"
    uid: 05be8dd0-af79-42d5-81f0-9a69239c9c44
  spec:
    containers:
    - env:
      - name: GOMEMLIMIT
        valueFrom:
          resourceFieldRef:
            divisor: "1"
            resource: limits.memory
      envFrom:
      - configMapRef:
          name: logsvc-config
      - secretRef:
          name: logsvc-secret
      image: 192.168.5.174:80/godev/logsvc:v8
      imagePullPolicy: IfNotPresent
      lifecycle:
        preStop:
          sleep:
            seconds: 3
      livenessProbe:
        failureThreshold: 3
        httpGet:
          path: /healthz
          port: 8080
          scheme: HTTP
        initialDelaySeconds: 10
        periodSeconds: 15
        successThreshold: 1
        timeoutSeconds: 2
      name: logsvc
      ports:
      - containerPort: 8080
        protocol: TCP
      readinessProbe:
        failureThreshold: 2
        httpGet:
          path: /readyz
          port: 8080
          scheme: HTTP
        initialDelaySeconds: 5
        periodSeconds: 5
        successThreshold: 1
        timeoutSeconds: 2
      resources:
        limits:
          cpu: "1"
          memory: 8Mi
        requests:
          cpu: 128m
          memory: 8Mi
      terminationMessagePath: /dev/termination-log
      terminationMessagePolicy: File
      volumeMounts:
      - mountPath: /var/run/secrets/kubernetes.io/serviceaccount
        name: kube-api-access-p2xxx
        readOnly: true
    dnsPolicy: ClusterFirst
    enableServiceLinks: true
    imagePullSecrets:
    - name: harbor-local174
    nodeName: node3
    preemptionPolicy: PreemptLowerPriority
    priority: 0
    restartPolicy: Always
    schedulerName: default-scheduler
    securityContext: {}
    serviceAccount: default
    serviceAccountName: default
    terminationGracePeriodSeconds: 30
    tolerations:
    - effect: NoExecute
      key: node.kubernetes.io/not-ready
      operator: Exists
      tolerationSeconds: 300
    - effect: NoExecute
      key: node.kubernetes.io/unreachable
      operator: Exists
      tolerationSeconds: 300
    volumes:
    - name: kube-api-access-p2xxx
      projected:
        defaultMode: 420
        sources:
        - serviceAccountToken:
            expirationSeconds: 3607
            path: token
        - configMap:
            items:
            - key: ca.crt
              path: ca.crt
            name: kube-root-ca.crt
        - downwardAPI:
            items:
            - fieldRef:
                apiVersion: v1
                fieldPath: metadata.namespace
              path: namespace
  status:
    conditions:
    - lastProbeTime: null
      lastTransitionTime: "2026-08-05T15:55:34Z"
      status: "True"
      type: PodReadyToStartContainers
    - lastProbeTime: null
      lastTransitionTime: "2026-08-05T15:55:33Z"
      status: "True"
      type: Initialized
    - lastProbeTime: null
      lastTransitionTime: "2026-08-06T01:31:42Z"
      message: 'containers with unready status: [logsvc]'
      reason: ContainersNotReady
      status: "False"
      type: Ready
    - lastProbeTime: null
      lastTransitionTime: "2026-08-06T01:31:42Z"
      message: 'containers with unready status: [logsvc]'
      reason: ContainersNotReady
      status: "False"
      type: ContainersReady
    - lastProbeTime: null
      lastTransitionTime: "2026-08-05T15:55:33Z"
      status: "True"
      type: PodScheduled
    containerStatuses:
    - containerID: containerd://8148287a6f85a5c495652c5fc94b1b29234eb8796cc15c10b92272a141bc7640
      image: 192.168.5.174:80/godev/logsvc:donotdrainbody
      imageID: 192.168.5.174:80/godev/logsvc@sha256:5af5202c02ce731f4a172dac1224d0bd19677e2b08d5c8706407b77c85213e23
      lastState:
        terminated:
          containerID: containerd://8148287a6f85a5c495652c5fc94b1b29234eb8796cc15c10b92272a141bc7640
          exitCode: 137
          finishedAt: "2026-08-06T01:31:41Z"
          reason: OOMKilled
          startedAt: "2026-08-06T01:31:33Z"
      name: logsvc
      ready: false
      restartCount: 4
      started: false
      state:
        waiting:
          message: back-off 40s restarting failed container=logsvc pod=logsvc-5589ddb8bd-7kv7l_godev(05be8dd0-af79-42d5-81f0-9a69239c9c44)
          reason: CrashLoopBackOff
      volumeMounts:
      - mountPath: /var/run/secrets/kubernetes.io/serviceaccount
        name: kube-api-access-p2xxx
        readOnly: true
        recursiveReadOnly: Disabled
    hostIP: 192.168.5.244
    hostIPs:
    - ip: 192.168.5.244
    phase: Running
    podIP: 10.244.135.37
    podIPs:
    - ip: 10.244.135.37
    qosClass: Burstable
    startTime: "2026-08-05T15:55:33Z"
kind: List
metadata:
  resourceVersion: ""
root@vox-174:stage3# 
## replicates=2 
root@vox-174:stage3# go run ./cmd/stress/main.go -c 200 -n 10000 -url=http://1
92.168.5.242:30222/ping -token token
并发数: 200
每并发请求: 10000 次
总请求数: 2000000
总耗时: 1m53.55018447s
========================================
成功: 2000000, 失败: 0
Successs QPS: 17613.36
P50: 8.529562ms
P95: 28.099139ms
P99: 48.319463ms

状态码分布:
  HTTP 200: 2000000 次
root@vox-174:stage3# 

# Custom Buckets, and set scrape_interval=15s
root@vox-174:stage3# go run ./cmd/stress/main.go -c 200 -n 10000 -url=http://192.168.5.242:30222/ping -token token
并发数: 200
每并发请求: 10000 次
总请求数: 2000000
总耗时: 1m18.574589348s
========================================
成功: 2000000, 失败: 0
Successs QPS: 25453.52
P50: 5.963178ms
P95: 20.367116ms
P99: 32.346188ms

状态码分布:
  HTTP 200: 2000000 次
root@vox-174:stage3# 

## in grafana
increase()请求数 podA 130w podB 70w,误差变小了，抓取周期15s对于我当前的极快返回的简单接口还是存在较大误差

直接算count的请求数 996267+103733

sum(increase(xxx[2m]))，先是增加到250w，然后2m内慢慢回落到110w()

但是1m还是没有值，很奇怪
Buckets: []float64{.0005, .001, .0025, .005, .01, .02, .05, .1, .25, .5, 1}
p99 一个498us 一个497us，还是0.0005的0.99，我的一个请求真有这么快吗？

[]float64{.00001, .00002, .00025, .0005, .001, .0025, .005, .01},
为了对比我换成了上面的，缩小桶左边的精度，这次p99全是249us了，


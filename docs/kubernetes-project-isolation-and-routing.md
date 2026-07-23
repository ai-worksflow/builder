# Kubernetes 项目隔离与统一域名路由契约

状态：Proposed
适用范围：生产 Sandbox、Preview 和 Generated Application 运行时
细化决策：`AIC-DEC-008`（生产沙盒和 Preview 以 Kubernetes Pod/Namespace 为参考目标）

仓库中的可执行本地纵向切片位于 [`deploy/k8s/`](../deploy/k8s/README.md)：固定版本的 Kind、Cilium、Envoy Gateway、两个项目 Namespace 和真实 NetworkPolicy 探针已打通。该切片属于 `implemented-local` 证据，不替代目标生产集群资格。

## 1. 目标

生产环境使用 Kubernetes 作为底层编排系统，并满足以下不变量：

1. 每个 Worksflow 项目拥有一个独立 Kubernetes Namespace。
2. 所有外部 HTTP/HTTPS 和 WebSocket 流量只经过平台统一 Gateway。
3. 平台资源使用三级或四级域名访问，不为每个项目创建独立公网入口。
4. Hostname 只是路由选择输入，不是租户身份或授权凭据。
5. Gateway 只能转发到控制面已登记的 exact Namespace、Service 和 Port，不能接受用户提供的任意 upstream。
6. Namespace 删除、路由撤销和运行时清理必须可重试、可对账，未知结果不能静默重建第二套资源。

本契约不把 Namespace 描述为强安全沙箱。运行不可信用户代码时，仍必须组合 RuntimeClass、NetworkPolicy、Pod Security、资源配额和节点隔离。

## 2. 目标拓扑

~~~text
Internet
   |
Wildcard DNS / TLS
   |
LoadBalancer
   |
Gateway (worksflow-gateway namespace)
   |
Route Resolver / AuthZ
   |
   +----------------------+----------------------+
   |                      |                      |
wf-p-<project-key-a>  wf-p-<project-key-b>  worksflow-system
   |                      |                      |
Service -> Pod         Service -> Pod        API / Controllers
~~~

- `worksflow-system`：平台 API、项目运行时控制器和后台 worker。
- `worksflow-gateway`：唯一公网 Gateway、TLS 和入口级策略。
- `wf-p-<project-key>`：项目 Namespace；包含该项目的 workload、ClusterIP Service、ServiceAccount、NetworkPolicy、ResourceQuota 和 LimitRange。
- 项目 workload 不创建 `LoadBalancer`、`NodePort` 或独立公网 Ingress/Gateway。

## 3. Namespace 契约

### 3.1 命名与权威映射

Namespace 使用不可变、不可猜业务含义的项目路由键：

~~~text
wf-p-<project-key>
~~~

`project-key` 必须是控制面生成的 lowercase DNS label，建议为固定长度的随机或 hash 派生值。不得直接使用用户输入的项目名、组织名或可修改 slug。完整 Namespace 名不得超过 Kubernetes DNS label 限制。

数据库持有唯一权威映射：

~~~text
Project ID -> Cluster ID -> Namespace -> Namespace UID -> lifecycle generation
~~~

Controller 在每次 mutation 和 reconcile 时都校验 Namespace UID 与 generation，防止 Namespace 删除重建后旧 Operation 误写新对象。

### 3.2 每个项目的最低隔离基线

Namespace 创建成功的判定必须至少包括：

- `ResourceQuota`：限制 CPU、内存、临时存储、PVC 和对象数量。
- `LimitRange`：为每个容器强制 requests/limits 的允许范围和默认值。
- default-deny ingress/egress `NetworkPolicy`。
- 仅允许来自统一 Gateway 的业务入站流量。
- 仅允许到平台明确代理、DNS 和合同声明外部目标的出站流量。
- Pod Security `restricted` 基线；拒绝 privileged、host PID/IPC/network、hostPath 和新增 Linux capabilities。
- 项目专用 ServiceAccount；默认不挂载 Kubernetes API token。
- 只读 root filesystem、non-root、seccomp 和受控 RuntimeClass。
- Secret 由平台 Secret Broker 按 workload identity 注入，不进入 HTTPRoute、镜像、环境回执或用户终端。

NetworkPolicy 只有在目标集群 CNI 确认支持并通过资格测试时才算生效。运行不可信构建或用户代码的 Pod 应进入专用 node pool，并采用 gVisor、Kata 或等价隔离 RuntimeClass；高风险租户需要升级到虚拟控制面或独立集群。

### 3.3 RBAC 边界

- 终端用户和项目 workload 不直接访问 Kubernetes API。
- 平台 Project Runtime Controller 只获得受控 Namespace 生命周期和项目资源所需权限。
- Gateway Controller 不获得项目 Secret 读取权限。
- 项目 ServiceAccount 不得读写 Route、Gateway、Namespace、RBAC、NetworkPolicy 或 ResourceQuota。
- cluster-scoped CRD、Webhook、StorageClass 和 Node 不属于项目自治范围。

## 4. 域名契约

假设平台注册域为 `example.com`，建议固定以下域名层次：

| 用途 | Hostname | 层级 | 生命周期 |
|---|---|---:|---|
| 平台控制台 | `builder.example.com` | 三级 | 稳定 |
| 平台 API | `api.example.com` | 三级 | 稳定 |
| 已发布项目资源 | `<route-key>.apps.example.com` | 四级 | 与 DeploymentRevision 绑定 |
| 临时 Preview/Sandbox | `<grant-key>.preview.example.com` | 四级 | 短期、可撤销 |

一个项目有多个资源时，使用一个 opaque `route-key` 表示 exact project/resource/deployment tuple，或在单个 DNS label 内使用受控编码；不要继续增加 DNS 层级。例如：

~~~text
api--7m3k2p.apps.example.com
web--7m3k2p.apps.example.com
~~~

编码后的单个 label 必须满足长度限制。用户可修改 slug 只能作为展示信息，不能改变资源身份；如产品需要可读域名，应创建带占用校验的 alias，并继续解析到同一个 immutable route identity。

### 4.1 为什么固定为单个动态 label

DNS 和 Gateway 可接受更深的 hostname，并不代表 TLS 通配证书能覆盖任意深度。`*.apps.example.com` 只应承诺覆盖一个动态 label，例如 `foo.apps.example.com`，不能依赖它覆盖 `bar.foo.apps.example.com`。因此所有平台动态资源都保持在固定的四级形状。

生产证书建议分开管理：

- `*.apps.example.com`
- `*.preview.example.com`
- 平台三级域名的精确 SAN 或独立证书

通配证书使用 DNS-01 和专用、最小权限 DNS 凭据签发。证书 Secret 只存在于 `worksflow-gateway`，不得复制到项目 Namespace。

### 4.2 DNS

为两个动态区创建通配 DNS 记录，统一指向 Gateway LoadBalancer：

~~~text
*.apps.example.com     -> Gateway public address
*.preview.example.com  -> Gateway public address
~~~

项目创建/删除不要求修改公网 DNS。DNS 命中不代表路由存在；未知、过期或已撤销 hostname 必须由 Gateway 返回无信息泄漏的 `404`。

## 5. Gateway API 路由模型

新生产实现采用 Kubernetes Gateway API，不新增依赖 legacy Ingress API 的控制面。

### 5.1 资源所有权

- Shared `Gateway` 位于 `worksflow-gateway`，由平台基础设施控制器拥有。
- 每个 listener 只接受带平台托管标签的 Namespace 中的 `HTTPRoute`。
- `HTTPRoute` 位于目标项目 Namespace，backend 只引用同 Namespace 的 `ClusterIP Service`。
- 项目控制器是 Route 的唯一 writer；用户 workload 和终端用户没有 Route 写权限。
- Admission policy 校验 hostname、parentRef、backendRef、port、owner label 和项目权威记录完全一致。

参考形状：

~~~yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: route-<route-key>
  namespace: wf-p-<project-key>
  labels:
    worksflow.dev/managed: "true"
    worksflow.dev/project-id: <project-id>
spec:
  parentRefs:
    - name: public
      namespace: worksflow-gateway
  hostnames:
    - <route-key>.apps.example.com
  rules:
    - backendRefs:
        - name: app-<deployment-key>
          port: 8080
~~~

Gateway listener 使用 Namespace selector 限制 attachment；项目 Namespace 只有在隔离基线完成后才获得对应 label。若实现选择把 Route 集中放在 Gateway Namespace，则跨 Namespace backend 必须使用项目 Namespace 中的 exact `ReferenceGrant`，且同样由平台控制器唯一管理。

### 5.2 权威路由表

Gateway 转发前必须从平台权威记录得到以下 exact binding：

~~~text
hostname
  -> project ID
  -> cluster ID / namespace / namespace UID
  -> route generation
  -> service UID / port
  -> release or candidate digest
  -> visibility and authorization policy
  -> lifecycle state / expiry
~~~

不得通过拆分 hostname 来计算 Namespace、Service 或 Port，也不得接受 query、header 或项目应用响应对 upstream 的覆盖。Gateway 只连接 allowlisted cluster-local Service，并在转发前校验 Route/Service 当前身份。

### 5.3 HTTP、WebSocket 与头部边界

- HTTP 和 WebSocket/HMR 使用同一个资源 origin 和同一授权决策。
- Gateway 重写并固定 `Forwarded`/`X-Forwarded-*`，丢弃客户端伪造值。
- 平台 Cookie、内部 capability、Authorization 和控制头不得转发给用户应用，除非 DeploymentContract 明确声明平台托管认证适配器。
- 用户应用不得为父域设置 Cookie；Gateway 丢弃 `Domain=example.com`、`Domain=apps.example.com` 或 `Domain=preview.example.com` 的 `Set-Cookie`。
- Preview 响应保留独立 iframe CSP、`frame-ancestors` 和 no-store 策略。
- 请求体、header、连接数、idle timeout 和 WebSocket 帧大小均有平台级上限。

## 6. 认证和可见性

发布资源显式选择以下一种策略：

- `public`：无需登录，但仍受速率限制、WAF 和 DeploymentRevision 状态约束。
- `project-member`：Gateway 的 external authorization 校验平台 Session、Project membership 和 route identity。
- `capability`：仅用于短期 Preview；使用高熵、短 TTL、可撤销且与 exact Session epoch/port 绑定的 grant。

域名可枚举或不可枚举都不改变授权要求。Preview 的 `grant-key` 不进入普通 access log；日志只记录 route ID、project ID、结果、延迟和经过脱敏的请求元数据。

## 7. 生命周期与一致性

建议由 Project Runtime Controller 使用持久 Operation 驱动：

1. 在数据库建立 project/cluster/namespace generation 的 desired state。
2. 创建 Namespace，并读取、持久化其 UID。
3. 应用并验证隔离基线。
4. 创建 workload 和 ClusterIP Service，等待 exact digest ready。
5. 建立 hostname route authority，再创建 HTTPRoute。
6. 等待 Gateway `Accepted=True`、`ResolvedRefs=True` 和数据面探测成功。
7. 原子发布对外状态；此前 hostname 返回 `404` 或 `503`，不能转发到旧/未知 backend。

删除顺序相反：先撤销平台 route authority 和 grant，再删除 HTTPRoute，等待入口不可达，然后清理 workload/PVC/Secret，最后删除 Namespace。任何未知 Kubernetes API 结果都进入 reconcile，不用新的 operation key 重复创建。

Preview 必须沿用 Release Controller 的 exact-Bundle single-flight 和 Session epoch fencing。相同 ReleaseBundle 的不确定结果继续占用同一逻辑 preview identity，直到对账得到显式终态。

## 8. 可观测性

每次路由请求至少关联：

- request ID / trace ID
- route ID 和 route generation
- project ID（不记录用户可控名称）
- namespace UID、service UID 和 deployment digest
- authorization policy/result
- Gateway/HTTPRoute observed generation

Namespace、Route、Service 和 Deployment 的事件进入统一审计流。平台告警至少覆盖 route authority 与集群资源漂移、Gateway attachment 失败、证书到期、跨 Namespace 网络探测成功、配额耗尽和异常出站。

## 9. 验收门禁

只有以下验证全部通过，目标集群才能标记为 production-qualified：

1. 两个项目使用相同 Service 名时不会互相路由。
2. 修改 Host、SNI、path、header 或 query 不能选择任意 Namespace/Service/Port。
3. default-deny 下跨项目 Pod、Service、DNS 和 Secret 访问均失败。
4. Route backend 只能选择已登记的 Service/Port，不能直接指定 Pod IP 或任意 cluster address；数据面到该 Service endpoints 的必要连接不受此表述影响。
5. 过期 Preview grant、旧 Session epoch、旧 Namespace UID 和旧 route generation 全部失败。
6. HTTP、WebSocket/HMR、断线重连和大请求执行相同授权与资源限制。
7. 通配 TLS 只覆盖预期四级域名；更深域名、错误 SNI 和未知 Host 均失败。
8. 项目删除后先断流，再清理 Namespace；并发请求不会命中另一个项目的新资源。
9. NetworkPolicy 在实际 CNI 上通过跨 Namespace 黑盒测试，而不是只检查对象存在。
10. Namespace 配额、Pod Security、RuntimeClass、Secret 注入和专用节点池通过故障注入与审计。

仓库内 fake controller、Compose Nginx 或 YAML render 只能形成 `implemented-internal` 证据，不能替代真实 Gateway、CNI、DNS、TLS、RuntimeClass 和目标集群的外部资格回执。

## 10. 分阶段落地

### Phase A：合同与控制面

- 冻结 ProjectRuntime、RouteAuthority 和 lifecycle generation 数据模型。
- 实现 Kubernetes Provider 接口和 fake conformance tests。
- 为 Namespace/Route/Service mutation 接入持久 Operation、lease 和 reconcile。

### Phase B：目标集群基线

- 安装并锁定 Gateway API 实现、支持 NetworkPolicy 的 CNI、证书和 DNS controller。
- 建立 `worksflow-system`、`worksflow-gateway`、专用 node pool 和 RuntimeClass。
- 配置两个通配 DNS 区与独立 TLS 证书。

### Phase C：项目运行时

- Project Runtime Controller 创建 Namespace 隔离基线。
- Release Controller 创建 workload、Service 和 exact HTTPRoute。
- 统一 Gateway 接入平台 AuthZ、rate limit、WAF、日志脱敏和 WebSocket。

### Phase D：外部资格

- 在真实目标集群执行第 9 节矩阵和故障注入。
- 固化 Gateway/CNI/RuntimeClass/certificate issuer 版本、配置 digest 和证据。
- 通过后才允许 Production Promotion；Preview 与 Production 分开出具回执。

## 11. 参考资料

- [Kubernetes Multi-tenancy](https://kubernetes.io/docs/concepts/security/multi-tenancy/)
- [Kubernetes Resource Quotas](https://kubernetes.io/docs/concepts/policy/resource-quotas/)
- [Kubernetes Gateway API hostnames](https://gateway-api.sigs.k8s.io/docs/concepts/hostnames/)
- [Gateway API HTTPRoute](https://gateway-api.sigs.k8s.io/reference/api-types/httproute/)

# Local Kubernetes vertical slice

This directory implements the project Namespace and unified Gateway contract as
a repeatable local deployment. It installs pinned kind/kubectl/Cilium CLI,
Kubernetes 1.35 with Cilium 1.19.6,
Envoy Gateway 1.8.2, two restricted project Namespaces, quotas, default-deny
NetworkPolicies, HTTP/HTTPS listeners, exact HTTPRoutes, and a non-root probe.

Local routes use ports 28080/28443:

- `project-a.apps.worksflow.localhost`
- `project-b.apps.worksflow.localhost`
- `4f0c2e8a.preview.worksflow.localhost`

Run `make k8s-deploy`, `make k8s-verify`, `make k8s-status`, or
`make k8s-down`. Production must replace kind, local NodePort mappings, local TLS, and
`.localhost` with a supported cluster, LoadBalancer, wildcard DNS, DNS-01
certificates, qualified CNI/RuntimeClass and durable controllers.

Kind 使用固定 NodePort 映射，因此路由在部署命令退出后仍可访问；Envoy 将 Kind 节点地址写入 Gateway 状态，顶层 Gateway 和四个 Listener 都必须为 `Programmed=True`。生产环境应将该 NodePort 入口替换为真实 LoadBalancer。

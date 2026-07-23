# `ai-worksflow/templates` 准入审计

状态：`candidate / blocked`

审计日期：2026-07-19

目标仓库：`https://github.com/ai-worksflow/templates.git`

本文记录 Stage 0 对用户指定模板仓库的精确审计事实。它不是
TemplateRelease 批准，也不能替代平台准入流水线生成的 17 项证据、SBOM、
漏洞结果和 DSSE 签名。

## 审计结论

当前仓库可以作为前后端框架来源，但不能直接进入 `approved`
TemplateRelease。平台必须保留其 `candidate` 状态，直到上游或平台维护的受控
镜像仓库补齐机器 Manifest、许可证、锁文件、供应链证明及可复现验证证据。

阻塞原因：

1. `main/TEMPLATE_INDEX.json` 声明的 repository identity 是
   `https://github.com/jfcwrlight/templates.git`，与实际审计来源
   `https://github.com/ai-worksflow/templates.git` 不一致。
2. 分支中的 `template.json` 是旧的目录描述格式，不满足平台
   `template-manifest/v1`；它没有规范的服务、固定工具链镜像、argv 命令、端口、
   健康检查、扩展/保护路径、环境 Schema 和锁文件 digest。
3. `react-shadcn-template` 虽有 `package-lock.json`，但没有可由本次准入绑定的根
   License/SPDX 证明；`python-fastapi-template` 没有提交 Python lock 文件。
4. 两个分支都没有平台要求的 exact-subject SBOM、漏洞报告、secret scan、容器
   构建证明、contract smoke receipt 和 DSSE/transparency-log 签名。
5. 上游 `VALIDATION_SUMMARY.md` 是有用的候选信息，但不是平台独立执行、绑定
   exact subject hash 的证据，因此不能提升为批准事实。
6. `main` 的 GitHub Actions workflow 在 main push 上只执行
   `validate-main-index`；模板 job 因 branch `if` 全部跳过。该 index job 只解析四个
   JSON 并相信 `validation.status == ready`，不会 checkout/验证索引列出的 exact
   template commit。在 `pull_request` merge ref 上，按 `github.ref_name` 与模板分支名
   比较的 jobs 也不能形成对应模板的必跑证据。工作流同时使用 `actions/*@vN`、Node
   major 和 Go `stable` 等浮动工具链标识，也没有输出平台所需的 exact-subject SBOM、
   provenance 或签名。因此绿色或 skipped Actions 都不等于已完成准入。

## 已固定候选

平台 tree digest 定义为：

```text
sha256(git ls-tree -r --full-tree -z <exact-commit> 的原始字节)
```

| 角色 | 分支 | exact commit | candidate tree digest |
|---|---|---|---|
| Index | `main` | `1edacd73910415c0e0e0429e60e09714a873776d` | `sha256:27bc44f3e5f8a5c5cb4effa51ad1933c187386eae578824c263d234e6f4d3f36` |
| Web | `react-shadcn-template` | `72664c5dc5cced39bc185f2f7e08dc6652a80ee3` | `sha256:07a72161d1dddfd73514ac572d5ca3f22cfe5c4cc494f9a1e8a38285a3c0458d` |
| API | `python-fastapi-template` | `1721440b33563b45192ffbb15da724d11f5f158f` | `sha256:a2c5d0be6b5f4d18bdde462f98c66440215131cb4ebfea5d5a4e7658d4ae8610` |

这些坐标只证明本次审计查看了哪个 Git tree，不证明 tree 安全、可构建或已批准。
2026-07-19 的远端复核中，三个分支头仍与表中坐标一致，远端没有 tag，
GitHub 也没有发布 Release；`main` 的 `TEMPLATE_INDEX.json` 仍声明
`jfcwrlight/templates.git`，阻塞结论未变化。

## 进入 approved 的修复清单

- 在受控 repository identity 下发布 exact commit，不允许索引指向另一个 owner。
- 为每个分支提供符合 `template-manifest/v1` 的 Manifest；命令必须是 argv，禁止
  shell 拼接，工具链 OCI image 必须固定 digest。
- Web 保留并固定 `package-lock.json` digest；Python 增加 `uv.lock` 或等价完整
  lock，并固定 registry policy。
- 提供 SPDX license expression、根 License 内容 digest 和第三方许可证清单。
- 在隔离 runner 中独立执行 install、lint、typecheck、unit test、build、startup、
  health、contract smoke、container build、secret scan、SBOM 和 vulnerability gate。
- 每项 evidence 必须绑定同一个 subject hash，并由平台签发 DSSE bundle；请求者与
  最终评审者必须是不同身份。
- 只有全部 gate 通过后创建不可变 TemplateRelease；随后才能把 exact Web/API
  releases 组合成 FullStackTemplate。

## 与实现的关系

仓库中的 `backend/internal/templates`、`backend/internal/templateauthority`、
`backend/internal/templateoperator` 和 migration
`000055_template_artifact_authority_receipts` 已把上述规则落实为独立 Artifact
Authority 边界：它重新读取 exact Git/OCI/SPDX bytes，验证 DSSE threshold 与 RFC6962
inclusion/checkpoint/SET，随后原子写入 immutable Receipt → attempt/v2 → release/v2 →
policy/v2。旧 v1 release 仍可审计读取，但不能再供新项目选择；调用方不能注入 evidence、
signer、trust root、policy、decision 或可信时间。

标准后端镜像包含 operator-only `template-authority` 命令；配置/部署/输入契约见
[`template-artifact-authority-operator.md`](template-artifact-authority-operator.md)。读 API
仍不提供准入或策略写操作。Constructor 只能解析 Registry 中所有 component policy
仍为 `approved` 且与 exact Authority Receipt 绑定的 FullStackTemplate。

这段代码链路不是对本审计目标的批准：目标 Registry、KMS/Secret Broker、原生
Sigstore 服务和 verifier image 仍待环境资格化，而且本页列出的上游材料尚未补齐。因此
数据库中不得为上述两个候选 commit 人工写入 approved policy，也不得用测试 Authority
fixture 冒充 Golden Release。

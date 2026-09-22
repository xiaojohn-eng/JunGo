# 公开源码的秘密扫描

本仓库由经过检查的源码建立新历史；个人运行状态、账号配置、订阅、配对数据、设备日志、原始测试报告与旧二进制制品不属于公开源码。不要将本地 `.state`、应用数据、签名文件或真实订阅配置加入 Git。

## 初次发布检查

2026-09-22 使用 Gitleaks **8.30.1** 的默认规则扫描公开源码目录，默认扫描报告有 15 个结果，全部位于下列 mihomo 上游公开示例与测试文件。通过 HTTPS 从 **MetaCubeX/mihomo v1.19.31** 下载对应原文并逐字节比较，确认本地文件一致：

| 文件 | 上游原文 | SHA-256 |
| --- | --- | --- |
| `third_party/mihomo/docs/config.yaml` | [v1.19.31 docs/config.yaml](https://github.com/MetaCubeX/mihomo/blob/v1.19.31/docs/config.yaml) | `47f28f65758fdd1525b864fbee34f9d9165ef7709bd5776f2badc8c01da790cf` |
| `third_party/mihomo/transport/openvpn/config_test.go` | [v1.19.31 transport/openvpn/config_test.go](https://github.com/MetaCubeX/mihomo/blob/v1.19.31/transport/openvpn/config_test.go) | `0c652ff75e9644ef94822227966079f036bf0cc55d12ecf673801b4435e3bdf5` |

这些固定值已经由上游公开，只供配置说明和测试；**不得将其中的示例密钥用于真实部署**。项目设备身份在本地运行时生成，不使用这些示例作为生产凭据。

## 精确例外的边界

[`.gitleaks.toml`](../.gitleaks.toml) 继承全部默认规则，仅为上述固定示例配置 4 个 allowlist。每项都指定检测规则，采用 **AND** 条件，同时满足：

1. 精确匹配上述某个文件路径；
2. 精确匹配已核实的完整示例字符串。

字符串使用逐字符 Unicode 转义的正则表示，两端锚定，没有通配符。9 个不同示例对应 10 个规则/字符串组合；其中同一个 age 示例同时由 age 与 generic API key 规则检测，所以必须限定两个检测规则。没有按整个 `third_party`、所有测试文件、整个文件路径或提交进行豁免。扫描时使用 `--ignore-gitleaks-allow`，不接受任意源码注释绕过检查。

加入例外后重新扫描完整公开目录，结果为 **0 个未豁免结果**。同时执行了两个独立的反向验证：

| 反向验证 | 结果 |
| --- | --- |
| 在允许路径中，仅修改某个示例的一字符 | 扫描失败，发现 2 个结果 |
| 将原始示例内容复制到其他路径 | 扫描失败，发现 14 个结果 |

因此现有路径里的新值，以及其他文件中的同值，都继续受默认规则检查。测试样本仅在临时目录生成，验证后删除，没有提交任何额外密钥样本。

## 重现与维护

```sh
gitleaks dir --config .gitleaks.toml --ignore-gitleaks-allow --redact --no-banner .
```

升级上游时，应重新核对公开来源及文件摘要，逐项审查新结果；不要扩大为目录豁免。扫描能发现已知模式，不能替代人工审查。生产凭据必须继续留在本地受限配置或部署平台的秘密存储中；任何未确认来源的结果都应阻止公开发布。原始扫描报告不发布，CI 输出应保持 `--redact`。

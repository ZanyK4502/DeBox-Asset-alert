# 多链项目币监控改造：生产基线

冻结时间：2026-07-27T14:14:28+08:00
基线性质：只读生产盘点；未修改生产配置、生产数据库或业务数据。

## 1. 代码与部署

- Git 分支：`go-migration`
- 生产代码提交：`8e1fdd671107aa7ba262225cca7e77f5b9460d20`
- 远端 `origin/go-migration`：与生产代码提交一致
- Railway 工作区：`My Projects`
- Railway 项目：`debox-asset-alert`
- Railway 项目 ID：`0b7304e1-da4c-4cf6-b7e5-95b9cf1784a4`
- Railway 环境：`production`
- Railway 环境 ID：`21ed695c-8b48-425d-87d0-999dd1a30a07`
- Railway 服务：`web`
- Railway 服务 ID：`a5711e99-dd5d-4226-9aec-699985d22a6e`
- 当前成功部署 ID：`06fe0a2a-dbe1-4f3f-b22c-8177a665bd12`
- 当前部署完成时间：2026-07-27T04:37:31+08:00
- 当前部署区域：`sfo`
- 生产地址：`https://web-production-5aef3.up.railway.app`

冻结标签 `multichain-market-baseline-20260727` 应始终指向上述生产代码提交。基线报告本身可以位于其后的文档提交中，不改变生产代码内容。

## 2. 生产健康与公开接口

冻结时只读检查结果：

| 路径 | 状态 |
| --- | --- |
| `/api/health` | HTTP 200，`ok=true` |
| `/api/ready` | HTTP 200，`status=ready` |
| `/api/plans` | HTTP 200 |
| `/api/chains` | HTTP 200 |
| `/static/index.html` | HTTP 200 |
| `/static/app.js` | HTTP 200 |
| `/static/i18n.js` | HTTP 200 |
| 未认证 `POST /api/market/query` | HTTP 401，认证边界正常 |

生产静态资源与本地生产提交内容一致。生产容器使用 CRLF、本地工作区使用 LF；统一换行后的 SHA-256 如下：

| 资源 | 统一换行后的 SHA-256 |
| --- | --- |
| `static/index.html` | `3169a9f5722df29603a01ba888e3da016523bf625035a4cc4eb7e9e0429dd38f` |
| `static/app.js` | `39cdb2cf0e2676fc781354aa27ddcee79dc842eb9d7eb1eb00198e9dc3467f12` |
| `static/i18n.js` | `9e1f807f77ac4991fc491c6036ff6e9d67ee3d360163ff42a22a525d4a522495` |

## 3. 当前产品能力边界

钱包资产监控公开支持以下六条 EVM 链：

| 链键 | Chain ID | Gas 代币 |
| --- | ---: | --- |
| `bsc` | 56 | BNB |
| `ethereum` | 1 | ETH |
| `base` | 8453 | ETH |
| `polygon` | 137 | POL |
| `arbitrum` | 42161 | ETH |
| `optimism` | 10 | ETH |

当前项目币市场监控仍只完整支持 BNB Chain。后续多链改造必须在保留现有 BNB 项目、交易池、规则语义和事件历史的前提下，将同一能力扩展至上述六条 EVM 链。

当前套餐市场权限：

| 套餐 | 项目数 | 池模式 | 行情查询 | 组合规则 | 群通知 |
| --- | ---: | --- | --- | --- | --- |
| 免费版 | 0 | `query` | 是 | 否 | 否 |
| 标准版 | 1 | `main` | 是 | 否 | 否 |
| 专业版 | 5 | `multiple` | 是 | 是 | 是 |

## 4. 生产配置基线

以下变量名已在 Railway `production/web` 配置；本报告不记录任何变量值或密钥：

```text
APP_ENV
APP_HOST
APP_NAME
CHAIN_ID
CHAIN_KEY
CHAIN_NAME
CHAIN_RPC_URL
DATABASE_URL
DEBOX_BOT_API_KEY
DEBOX_BOT_API_SECRET
DEBOX_BOT_RECEIVE_MODE
DEBOX_BOT_USER_ID
DEBOX_NOTIFICATION_CHAT_ID
DEBOX_NOTIFICATION_CHAT_TYPE
MARKET_COLLECTOR_ENABLED
MARKET_RULE_ENGINE_ENABLED
MARKET_WEBHOOK_AUTO_REPAIR
NODIT_API_KEY
NODIT_CU_PER_SECOND
NODIT_MONTHLY_CU_LIMIT
NODIT_WEBHOOK_SIGNING_KEYS_JSON
PAYMENT_MODE
PAYMENT_RECIPIENT_ADDRESS
PUBLIC_APP_URL
SUBSCRIPTION_DAYS
SUBSCRIPTION_PRICE
SUBSCRIPTION_TOKEN_ADDRESS
SUBSCRIPTION_TOKEN_DECIMALS
SUBSCRIPTION_TOKEN_SYMBOL
```

此外还有 Railway 自动注入变量。冻结时尚未配置 CoinGecko 相关变量；这是后续多链代币搜索能力的新配置，不属于当前生产基线。

## 5. 数据库基线

审计方式：使用生产 PostgreSQL 公网连接，在 `default_transaction_read_only=on`、`REPEATABLE READ, READ ONLY` 事务中执行，只读取元数据与聚合计数。
数据库：`railway`
版本：PostgreSQL 18.4（Debian 18.4-1.pgdg13+1）
`public` 表数量：41

### 5.1 关键业务数据

- `subscriptions`：5
  - 免费版 / 已过期：3
  - 标准版 / 生效：1
  - 专业版 / 生效：1
- `permanent_plan_allowlist`：5
  - 专业版：4
  - 标准版：1
- `market_projects`：2
  - 生效：1
  - 已归档：1
- `market_project_pools`：60
  - 发现：57
  - 主池：2
  - 已选择：1
- `market_pools`：173
- `market_rules`：0
- `market_rule_events`：0
- `combination_rules`：0
- `market_combination_rules`：0
- `watch_rules`：0
- `nodit_webhook_subscriptions`：4，全部生效
- `orders`：4
- `user_preferences`：1
- `notification_groups`：0
- `daily_summary_targets`：0

### 5.2 市场池支持状态

- PancakeSwap / `supported`：159
- Uniswap / `quotes-only`：5
- SquadSwap / `quotes-only`：2
- BabySwap、Biswap、DinosaurEggs、Nomiswap / `quotes-only`：各 1
- 另有 3 个以地址形式记录的协议标识 / `quotes-only`：各 1

### 5.3 市场事件

冻结时 `market_events` 共 15,181 条：

| 事件类型 | 数量 |
| --- | ---: |
| `token_transfer` | 14,644 |
| `pool_initialized` | 286 |
| `sell` | 88 |
| `buy` | 80 |
| `holder_increase` | 39 |
| `holder_decrease` | 15 |
| `liquidity_removed` | 13 |
| `liquidity_added` | 5 |
| `holder_rank_entered` | 3 |
| `holder_rank_exited` | 3 |
| `migrated` | 2 |
| `trading_stopped` | 2 |
| `market_price_above` | 1 |

以下采集类计数会随生产运行继续增长，只用于时间点对照，不作为后续严格相等断言：

- `webhook_inbox`：6,977
- `market_scanned_blocks`：4,047
- `market_snapshots`：3,380
- `market_holder_snapshots`：760
- `market_holders`：22
- `market_chain_cursors`：1

### 5.4 数据源健康

以下六项生产健康记录均为 `healthy`：

- DexScreener：`pair_quotes`
- DexScreener：`pool_discovery`
- Nodit：`log_scanner`
- Nodit：`pool_metadata`
- Nodit：`rpc`
- Nodit：`webhook_status`

## 6. 数据库迁移基线

已应用迁移共 10 个，最新为 `0010_remove_complimentary_grants.sql`。

| 迁移 | SHA-256 |
| --- | --- |
| `0001_python_baseline.sql` | `e4a8a9a7d709371bccc161f77744c2760d63072d75698d19c4fe876aa2e2db1b` |
| `0002_aggregated_notifications.sql` | `06c5a948c442efb0fe1fe1b5868b18592f8e0e1941db27f47ca7dd25f1f71fb2` |
| `0003_aggregation_cleanup_indexes.sql` | `a9569f2a9a27c0e333e91993f9640365e0d43f9b090cee2ce56434fa64a82307` |
| `0004_order_billing_cycles.sql` | `8b72c5209489402554faa3a24015e7af0c9d57e420133a9e4dbdbeb6e4a6e4b5` |
| `0005_daily_summary_targets.sql` | `aafb4efff896db3b07ff2832dd4b53586e9aad2a2ce6593cc888aeaa1f144efd` |
| `0006_market_monitoring_domain.sql` | `ab3110774920ac946d4e2d2ad7e2da59d30222d82c9dff114a698453b0290655` |
| `0007_market_collection_recovery.sql` | `9ffeb92c99bf3b723b874fdef52f7c1dc224d4ab0d0d6d958cc43cc261951e03` |
| `0008_market_rules_notifications.sql` | `9af662c5d98df708ec081369c4176c19d7366e8d615b515d05de6a81a55a98a5` |
| `0009_permanent_plan_allowlist.sql` | `a6ea69a8d6fd54c595b9236a064687350a5eaef490021995555ed974e10ae3e9` |
| `0010_remove_complimentary_grants.sql` | `9ca6cbe65071f0b022417bc2b9df4963cfbbf3769ddf4d57414e564b503c681d` |

## 7. 回归验证基线

冻结时以下命令全部通过：

```text
go test ./...
go test -race ./...
go vet ./...
go build ./...
node tests/h5_contract_test.mjs
```

H5 合约检查结果：

- DOM 引用：149
- API：21
- 翻译键：294

工具链：

- Go 1.26.5 windows/amd64
- `CGO_ENABLED=1`
- Node.js v20.18.1
- Race detector 已使用本机 GCC 成功运行

## 8. 后续改造不可破坏的约束

1. 保留现有 2 个项目币项目、60 条项目与池关联及全部历史事件。
2. 保留 4 个生效中的 Nodit Webhook，不得重复创建或静默失效。
3. 保留 5 条永久套餐白名单及其 4 专业版、1 标准版权益。
4. 保留现有 5 条订阅记录及生效/到期语义。
5. 新迁移只能从版本 10 向前追加，不得修改已应用迁移文件。
6. BNB Chain 现有项目币监控行为必须继续兼容。
7. 钱包监控现有六链能力、套餐限制、通知和日报不得退化。
8. 生产密钥不得写入代码、报告、日志或聊天。

## 9. 回滚锚点

- 代码回滚锚点：Git 标签 `multichain-market-baseline-20260727`
- 生产部署回滚锚点：Railway 部署 `06fe0a2a-dbe1-4f3f-b22c-8177a665bd12`
- 数据结构回滚依据：本报告中的迁移版本、迁移哈希和关键数据统计

本步骤没有创建生产数据库备份，因为未执行任何数据库写入。进入最终生产迁移前，必须再创建一次可恢复的数据库备份，并在备份完成后才允许执行新迁移。

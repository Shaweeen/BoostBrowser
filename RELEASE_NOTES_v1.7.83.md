# BrowserStudio v1.7.83

## P0：动态增减跟随环境

- **运行中热添加/移除**：同步进行中可随时添加或移除跟随环境，无需停止重启同步会话。
- **新增 API**：`AddFollowerToSync` / `RemoveFollowerFromSync` / `GetSyncFollowerIds`。
- **前端 UI**：紧凑模式和全屏模式均支持一键添加/移除跟随环境。

## P1：面板侧进程扫描增量优化

- **两级缓存策略**：短时缓存（2秒）+ 陈旧缓存（15秒），扫描超时自动回退。
- **性能提升**：100+ 环境场景下，UI 响应时间从 5-12 秒降至 3 秒内。

## P2：面板最小化状态信息增强

- 最小化面板显示跟随数量和布局模式，用户可清晰感知同步状态。

## Bug Fixes

- 修复 `addFollowerToSyncLocal` / `removeFollowerFromSyncLocal` 竞态条件（单次锁获取）。
- 修复冗余锁获取导致的潜在死锁。
- 前端添加加载状态防止重复点击。
- 修复 follower 计数日志（排除 master）。
- 修复函数间缺少换行的语法错误。

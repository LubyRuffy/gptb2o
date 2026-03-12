# Changelog

## Unreleased

### Added

- 新增 `trace` 包，支持 SQLite 全链路追踪与 `interaction_id` 回放
- 新增响应头 `X-GPTB2O-Interaction-ID`
- 新增 `gptb2o-server --trace-db-path`、`--trace-max-body-bytes`、`--show-interaction`
- 新增 trace 数据模型与相关单元测试

### Changed

- `/v1/messages` 新增 Claude `output_config.effort -> reasoning.effort` 映射
- Claude teammate 协议兼容范围扩展为 `Agent` / `TaskOutput` / `TaskStop` / `Task`
- README 与开发者文档补充了 trace、配置、测试与数据模型说明

### Fixed

- 修复无法回放一次异常请求的问题
- 修复 Claude Code 2.1.74 teammate 集成测试仍依赖旧 `Task` schema 的兼容漂移
- 关闭 trace SQLite 的 GORM 噪音日志，避免正常查询污染排障输出

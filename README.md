# gorm-oceanbase

[GORM](https://gorm.io) 的 OceanBase 数据库驱动，支持 **MySQL 兼容模式** 与 **Oracle 兼容模式**。底层使用
[go-oceanbase](https://github.com/cocosip/go-oceanbase)（`database/sql` 驱动），一个 `Dialector` 通过 DSN 或配置选择模式。

## 快速上手

```go
import (
    "github.com/cocosip/gorm-oceanbase"
    "gorm.io/gorm"
)

db, err := gorm.Open(oceanbase.Open("oceanbase://user:pass@127.0.0.1:2881/db?mode=mysql"), &gorm.Config{})
```

结构化配置：

```go
cfg, err := oceanbase.ParseDSN("oceanbase://user:pass@127.0.0.1:2881/db")
if err != nil {
    return err
}
db, err := gorm.Open(oceanbase.New(oceanbase.Config{
    DSNConfig: &cfg,
    Mode:      oceanbase.ModeOracle,
}), &gorm.Config{})
```

`Open`/`New` 不建立连接，DSN 与配置错误由 `Initialize` 返回。模式优先级：`Config.Mode`（非零）> DSN/`DSNConfig` 中的模式 > 默认 MySQL。
需要自动探测租户兼容模式时使用 DSN 参数 `mode=auto`。

> 当前版本处于开发中，支持范围以 [docs/development.md](docs/development.md) 的验收状态为准。
> 需要真实数据库的集成验收尚未执行，请勿据此声明生产可用。

## 文档

- [设计方案](docs/design.md)：架构、配置、双模式行为、错误翻译与测试策略
- [开发计划与进度](docs/development.md)：任务分解、依赖与验收状态

## 许可证

见仓库许可证文件。

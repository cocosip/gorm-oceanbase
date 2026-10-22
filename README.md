# gorm-oceanbase

[GORM](https://gorm.io) 的 OceanBase 数据库驱动，支持 **MySQL 兼容模式** 与 **Oracle 兼容模式**。底层使用
[go-oceanbase](https://github.com/cocosip/go-oceanbase)（`database/sql` 驱动），一个 `Dialector` 通过 DSN 或配置选择模式。

> 当前进度：M1（MySQL 模式）代码与无库单测完成，真实租户集成验收尚未执行。
> 支持范围以 [docs/development.md](docs/development.md) 的验收状态为准，未验收项不声明生产可用。

## 安装

```bash
go get github.com/cocosip/gorm-oceanbase
```

开发基准为 `gorm.io/gorm` v1.31.2；更低 GORM 版本需通过版本矩阵验收后再声明支持。

## 快速上手

```go
import (
    "github.com/cocosip/gorm-oceanbase"
    "gorm.io/gorm"
)

db, err := gorm.Open(oceanbase.Open("oceanbase://user:pass@127.0.0.1:2881/db?mode=mysql"), &gorm.Config{})
```

结构化配置（与 DSN 二选一）：

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

`Open`/`New` 不建立连接，DSN 与配置错误由 `Initialize` 返回，因此可以安全地在配置阶段构造。

## 模式选择

优先级如下，零值表示"未指定"：

```
Config.Mode（非零） > DSN 显式 mode / DSNConfig 非零 Mode > 默认 MySQL
```

- 需要自动探测租户兼容模式时使用 DSN 参数 `mode=auto`。探测在临时连接上读取
  `ob_compatibility_mode`，只接受非 NULL 的 `MYSQL`/`ORACLE`；未知值、NULL、超时与权限错误都返回初始化错误。
- 一个 GORM `DB` 实例只能对应一种兼容模式；不支持混用模式的租户。
- 当前版本 M1 仅交付 MySQL 模式。检测到 Oracle 模式会返回 `ErrUnsupportedMode`，不会套用 MySQL 方言。
- 外部 `Conn` 不参与探测，必须显式给出具体 `Mode`。

## 时间与 MySQL 档案

- **内部连接**（DSN / DSNConfig）：驱动 `Session.ParseTime` 会被强制开启，GORM 的普通
  `time.Time`/`*time.Time` 字段才能正确扫描。DSN 显式写 `parseTime=false` 会返回配置错误，不会被静默改写。
- **外部连接**（`Config.Conn`）：调用者自行保证 `ParseTime=true`，本驱动不修改也不关闭该连接池。
  连接池所有权始终归调用者；初始化失败时驱动也不会关闭它。
- **MySQL 档案**：`CompatibleMysqlVersion` 取值范围 5 / 8，决定 MySQL 客户端结果与转换语义。
  优先级为 `Config.CompatibleMysqlVersion` > DSN 显式 `compatibleMysqlVersion` > `DSNConfig` 非零值 > 默认 **8**。
  档案 5 会截去 TIME 结果的小数，DATETIME/TIMESTAMP 的行为需按档案分别验证。
- Oracle 模式不注入 MySQL 档案。

## 字符串与时间类型默认值

对齐 `gorm.io/driver/mysql` v1.6 的行为：

- 普通 `string` 字段未指定 `size` 时为 `longtext`；主键、带默认值或带 `INDEX`/`UNIQUE` tag 的字段为
  `varchar(191)`（utf8mb4 索引长度）；设置 `Config.DefaultStringSize` 后统一使用该长度。
- 长度 65536 至 2²⁴ 使用 `mediumtext`，更长为 `longtext`，其余正长度使用 `varchar(n)`。
- `datetime` 默认精度 3（毫秒），可用 `DefaultDatetimePrecision` 调整或 `DisableDatetimePrecision` 关闭。
- 默认 `NowFunc` 按最终模式安装：MySQL 毫秒截断；用户自定义的 `NowFunc` 不会被覆盖。

## 错误翻译

配合 `gorm.Config{TranslateError: true}`，MySQL 模式把驱动 `*dberrors.Error` 的服务器错误码映射为 GORM 错误：

| 错误码 | GORM 错误 |
| --- | --- |
| 1062 | `gorm.ErrDuplicatedKey` |
| 1451 / 1452 | `gorm.ErrForeignKeyViolated` |

未命中的错误原样返回，不做模糊匹配。

## 文档

- [设计方案](docs/design.md)：架构、配置、双模式行为、错误翻译与测试策略
- [开发计划与进度](docs/development.md)：任务分解、依赖与验收状态

## 许可证

MIT，见 [LICENSE](LICENSE)。

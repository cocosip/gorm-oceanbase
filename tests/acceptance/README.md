# 真实租户验收程序

`cmd/gorm-obtest` 是独立命令，通过本仓库的 GORM Dialector 连接 OceanBase。
同一份 JSON 配置可选择 MySQL、Oracle 或依次执行全部目标。已有 `tests/integration`
继续保留；此命令提供类型矩阵、字段元数据、独立对象清理和可保存的 JSON 报告。

本轮仅完成离线开发和检查，**尚未执行真实数据库验收**。

## 离线查看和检查

在仓库根目录运行，无需环境变量，不建立连接：

```powershell
go run ./cmd/gorm-obtest -list
go run ./cmd/gorm-obtest -config tests/acceptance/config.example.json -check
go run ./cmd/gorm-obtest -config tests/acceptance/config.example.json -mode oracle -check
go build -o bin/gorm-obtest.exe ./cmd/gorm-obtest
```

## 连接配置与执行

使用可创建表、索引、约束及 Oracle 序列/类型的测试 schema。程序创建随机前缀的测试对象，
写入测试数据，并在每个用例结束后清理自己成功创建的对象；不清空业务表，不自动创建数据库。
强制终止进程或断网可能留下对象，其名称记录在报告的 `objects` 中。

配置只引用环境变量名，不写入密码或 DSN。示例包含四个目标，分别测试两种模式下的
普通 GORM 执行与 `PrepareStmt` 缓存执行。`prepare_stmt` 是 GORM 缓存开关，
不等于底层驱动的客户端参数插值开关。OBJECT/集合需要底层服务器预处理路径。

```powershell
$env:OB_MYSQL_DSN = 'oceanbase://user:password@127.0.0.1:2881/test_db?mode=mysql'
$env:OB_ORACLE_DSN = 'oceanbase://user:password@127.0.0.1:2881/TEST_SCHEMA?mode=oracle'

# 连续执行配置中的所有目标
go run ./cmd/gorm-obtest -config tests/acceptance/config.example.json -report report-all.json

# 只选择一种模式，或只选择一个目标
go run ./cmd/gorm-obtest -config tests/acceptance/config.example.json -mode mysql -report report-mysql.json
go run ./cmd/gorm-obtest -config tests/acceptance/config.example.json -target oracle -report report-oracle.json
```

每次使用新的报告路径，程序拒绝覆盖已有文件。可以删除不需要的目标，或将 MySQL 目标的
`mysql_profile` 改为 5（TIME 的小数按该档案截断），省略时使用 8；目标配置覆盖 DSN 的 MySQL 档案。
Oracle 目标不能设置 MySQL 档案。明确的 DSN 模式必须与目标模式一致。为固定无时区时间的验收基准，程序使用 UTC 解析。
未配置 DSN 会报配置错误，不会把整套真实测试标记为通过。

`timeout` 是用例预算，`cleanup_timeout` 是每个清理动作的独立预算；取消后仍尝试清理。
连接建立和网络读取超时不会超过用例预算。`-check` 只校验 JSON 结构、模式、目标和用例名称，
不读取 DSN 环境变量，也不验证租户权限、网络、凭据或服务器能力。

在配置中加入 `cases` 数组可以只运行指定用例，例如：

```json
"cases": ["types.uint64", "types.oracle_number", "schema.fields", "schema.migration"]
```

## 覆盖内容

确切名称及适用模式以 `-list` 输出为准。当前包含 106 个独立用例：

| 范围 | 内容 |
| --- | --- |
| Go 数值 | bool；int/int8/int16/int32/int64、uint/uint8/uint16/uint32/uint64；最小值、零、最大值；float32/float64 及大值 |
| 精确小数 | DECIMAL/NUMBER(38,9)，使用 `types.Number`，避免 float64 丢失有效位 |
| 字符串、二进制 | 普通/定长限制/长字符串、Unicode、SQL 空字符串差异；原始字节、限长字节、LOB 负载 |
| NULL | Go 指针、sql.NullBool/Int16/Int32/Int64/Float64/String/Time，区分 NULL 和有效零值 |
| 时间 | time.Time、秒/毫秒/微秒；DATE/DATETIME/TIMESTAMP、MySQL TIME/YEAR、Oracle TIMESTAMP(9) |
| MySQL 专有类型 | CHAR、TEXT/MEDIUMTEXT/LONGTEXT、VARBINARY/BLOB、ENUM、SET、JSON |
| Oracle 专有类型 | NUMBER、CHAR/NCHAR/NVARCHAR2、RAW/BLOB/CLOB/NCLOB、INTERVAL YM/DS、带时区/本地时区时间、ROWID、OBJECT 和集合列 |
| 字段和元数据 | 实际数据库类型、长度、精度/小数位、可空性、默认值；记录 GORM 可报告的主键/自增/注释；NOT NULL、忽略字段和自定义/显式大小写列名 |
| 迁移和约束 | 增量/重复 AutoMigrate、原数据保留、列改名/删除、复合主键、唯一复合索引及重命名/删除、外键、CHECK、MySQL 列注释 |
| GORM 行为 | CRUD、显式/生成主键、批量和全默认值写入、map 与显式 NULL、零值更新、分页、Upsert/DoNothing、错误翻译、事务/保存点、Hook、时间戳、软删除、四类关联/Preload/Join/FullSaveAssociations、Oracle 序列 |

每个标量用例同时校验写入/读回值与 `ColumnTypes`；不是只检查建表成功。
字段类型、可空性、显式长度和高精度小数元数据有独立断言。字段元数据缺少某项时，
JSON 中省略该属性，表示 GORM 未提供，不能据此声称该项已验收。
浮点测试使用可精确表示的普通值；不声称穷举所有浮点值或所有服务器 SQL 类型。

Oracle 空 VARCHAR2 视为 NULL；ROWID 从服务器读取，不能伪造。
OBJECT 会创建真实命名类型并通过 GORM 字段读写。集合列需要服务器实际支持存储；
不支持、权限不足、预处理/LOB/类型描述符错误一律保留失败，不自动降级或按历史结果跳过。
PL/SQL 数组、游标、XA、空间类型和其他底层驱动专属接口不属于此 GORM 验收程序。

## 报告与退出码

报告包含运行 ID、Go/平台、目标名、实际探测的服务器版本、配置的模式和执行方式、
MySQL 档案、各用例耗时、字段/索引信息、创建对象、失败及清理错误。连接后读取真实租户兼容模式并核对配置。
GORM SQL 日志关闭；报告不输出 DSN、密码、SQL 参数或未经处理的驱动错误消息，
服务器错误保留错误码，用例名定位失败范围；标量用例还记录最后执行的步骤，区分建表、元数据、写入、读取和比较。

| 状态 | 含义 |
| --- | --- |
| pass | 执行和断言通过，且对象清理未失败 |
| fail | 连接、DDL、读写、断言、panic 或清理失败 |
| skip | 因前置条件失败而未执行；不能计入通过 |
| not_applicable | 用例属于另一种模式，明确不适用 |

退出码：`0` 全部适用用例通过；`1` 存在失败；`2` 配置、参数或报告路径错误；
`3` 无通过用例或存在跳过。使用 `go run` 时 Go 启动器可能将非零子进程状态统一返回为 1，
CI 若依赖精确退出码应运行编译后的二进制。

报告中的通过数只证明该次目标/版本/配置，不等于其他版本或部署的完整兼容声明。

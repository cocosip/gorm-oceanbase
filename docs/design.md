# gorm-oceanbase 设计方案

本文定义基于 [go-oceanbase](https://github.com/cocosip/go-oceanbase)（`database/sql` 驱动）的待实现 GORM Dialector，目标支持 OceanBase **MySQL 兼容模式**与 **Oracle 兼容模式**。开发与验收状态以 [development.md](development.md) 为准。

- 模块路径：`github.com/cocosip/gorm-oceanbase`，包名 `oceanbase`
- 开发基准：`gorm.io/gorm` v1.31.2（当前最新版本）；v1.25+ 为兼容目标，需完成版本矩阵测试后才能声明支持
- 驱动基线：`github.com/cocosip/go-oceanbase` **v0.1.1**
- Go 版本：1.26（随底层驱动要求）

---

## 1. 背景与约束

### 1.1 底层驱动现状（go-oceanbase）

| 能力 | 说明 | 对 GORM 层的影响 |
| --- | --- | --- |
| 连接方式 | `sql.Open("oceanbase", dsn)` 已注册；更推荐 `oceanbase.NewConnector(oceanbase.Config)` 得到 `driver.Connector`，配置经过校验 | Initialize 优先走 `sql.OpenDB(connector)` |
| DSN 风格 | URL 形式 `oceanbase://user:pass@host:2881/db?mode=mysql`，未知参数直接报错；`ParseDSN` 同时应用驱动默认值 | 使用驱动解析器；在解析前记录显式参数，再施加 GORM 默认策略，不自造 DSN 格式 |
| 双模式 | `ModeMySQL` / `ModeOracle` / `ModeAuto`；Connector 在首条连接成功初始化后固定模式，后续连接不得混用模式 | 默认 MySQL；显式 `mode=auto` 时用已有 `SessionVariable(ctx, conn, "ob_compatibility_mode")` 读取并校验实际模式（见 §4.1） |
| MySQL 兼容档案（v0.1.1+） | `Session.CompatibleMysqlVersion`（DSN `compatibleMysqlVersion`）选择 MySQL 5/8 客户端语义；**驱动默认 5，档案 5 截去 TIME 结果的小数，不是所有 DATETIME/TIMESTAMP 小数** | GORM MySQL 模式默认注入 **8**，保留 TIME 小数并采用对应元数据语义；Config/DSN 显式指定时尊重用户 |
| 时间读取 | `Session.ParseTime` 默认 false；普通日期/时间戳此时返回字符串，不能直接扫描到 GORM 的 `time.Time` 字段 | 内部连接统一开启 ParseTime；外部连接由调用者开启，带时区类型另见 §5.2 |
| 版本解析（v0.1.1+） | 驱动内部解析已识别 "OceanBase" 标记、取其后版本为真实 OB 版本（internal/configspec，**未导出**） | GORM 侧 version.go 采用相同策略；按模式查询真实 OB 版本（见 R1） |
| 参数绑定 | 两种模式都支持位置参数 `?`（Oracle 另支持命名参数） | `BindVarTo` 统一写 `?`，无需 Oracle 的 `:1` |
| 错误 | `*dberrors.Error{Code, SQLState, Message}`，保留服务器原始错误码与 SQLSTATE | `ErrorTranslator` 直接按 Code 映射（见 §7） |
| RowsAffected | Oracle 模式要求 `MatchedRows`，`ChangedRows` 会被驱动拒绝 | 提前校验并返回配置错误，不静默纠正用户设置 |
| 生成主键 | 没有非零插入 ID 时 `LastInsertId()` 返回 `ErrUnsupported`；驱动支持 OUT 参数 | Oracle Create 必须接管执行与主键回填，基础 RETURNING INTO 属于 M2 |
| 扩展能力 | ExecBatch、LOB、游标、XA、OBProxy 路由、全链路诊断等 | 使用驱动原生 API，补充可选连接作用域 helper 与配置示例（见 §8） |

### 1.2 参照实现

- **MySQL 模式**：以 `gorm.io/driver/mysql` v1.6 为行为蓝本（OceanBase MySQL 模式与 MySQL 语法高度兼容），但**不 import 该包**，把需要的实现（类型映射、QuoteTo、ON DUPLICATE KEY UPDATE 子句构建、information_schema 查询）在本仓库内落地，避免引入 `go-sql-driver/mysql` 依赖。
- **Oracle 模式**：参考社区 Oracle 方言惯例（双引号 + 大写化标识符、`FETCH FIRST` 分页、`MERGE` upsert、`NUMBER/VARCHAR2/CLOB` 类型、`ALL_*` 数据字典）。

---

## 2. 总体结构

单包双方言：用户只面对一个 `oceanbase.Dialector`，模式由 DSN/Config 决定；MySQL/Oracle 两种"方言行为"作为内部实现拆分文件。

```
gorm-oceanbase/
├── oceanbase.go          # Config、Open(dsn)、New(cfg)、Dialector 主体与 Initialize
├── dialect_mysql.go      # MySQL 模式行为：QuoteTo(反引号)、DataTypeOf、ClauseBuilders
├── dialect_oracle.go     # Oracle 模式行为、Create 回调、整句 INSERT/INSERT ALL/MERGE 与主键回填
├── migrator.go           # MySQL 模式 Migrator（information_schema，参考 mysql driver 差异点）
├── migrator_oracle.go    # Oracle 模式 Migrator（ALL_TAB_COLUMNS / ALL_CONSTRAINTS / ALL_INDEXES …）
├── error_translator.go   # dberrors.Error → gorm.ErrDuplicatedKey 等
├── version.go            # 版本探测与解析（"5.7.25-OceanBase_CE-v4.2.1…"）
├── options.go            # 可选 WithPinnedConn 桥接与事务边界检查
├── docs/design.md        # 本文档
└── tests/integration/    # 集成测试（build tag `integration` + 环境变量提供 DSN）
```

**取舍说明**：不拆成 `mysqlmode`/`oraclemode` 两个子包。用户只需配置一次模式；公共 Config 和错误翻译入口共用。模式在 Initialize 时固化，选择对应构建函数与回调，不在每次操作时重新探测。

---

## 3. Config 与入口

```go
type Config struct {
    DSN        string              // oceanbase:// URL
    DSNConfig  *obdriver.Config    // 结构化配置；与 DSN 二选一，复制后使用，不序列化回 DSN
    Conn       gorm.ConnPool       // 外部连接池（与 DSN/DSNConfig 互斥）

    Mode       obdriver.Mode       // 非零值覆盖输入配置；零值表示未指定，auto 通过 DSN 的 mode=auto 请求

    SkipInitializeWithVersion bool // 跳过方言层版本查询；未知版本禁用需确认的能力，不跳过模式解析
    ServerVersion             string

    // —— MySQL 模式（语义对齐 gorm mysql driver）——
    DefaultStringSize        uint    // 0 对齐 mysql v1.6.0：普通 string 为 longtext，特定索引等字段为 varchar(191)
    DefaultDatetimePrecision *int
    DisableDatetimePrecision bool
    CompatibleMysqlVersion   int     // 0 未指定；非零值覆盖 DSN/DSNConfig，MySQL 最终默认 8
    DontSupportRenameIndex / RenameColumn / ForShareClause / DropConstraint / NullAsDefaultValue … bool

    // —— Oracle 模式 ——
    DefaultNumberPrecision …          // NUMBER 默认精度策略
    // （序列/IDENTITY 主键策略见 §6）

    // —— 内部 ——
    resolvedMode obdriver.Mode       // 创建正式 Connector 前固化
}
```

上述代码为字段草案，`obdriver` 是 `github.com/cocosip/go-oceanbase` 的导入别名；入口包仍为 `oceanbase`。`Open/New` 不连接数据库，解析或配置错误由 Initialize 返回。输入本身须通过驱动校验，顶层覆盖策略不用于放宽非法 DSN 或未知参数的校验。

入口函数与 gorm 生态惯例一致：

```go
gorm.Open(oceanbase.Open("oceanbase://user:pwd@127.0.0.1:2881/db?mode=mysql"), &gorm.Config{...})
gorm.Open(oceanbase.New(oceanbase.Config{DSNConfig: &cfg, Mode: obdriver.ModeOracle}), ...)
```

---

## 4. Initialize 流程（核心）

**前置钩子 `Apply(*gorm.Config)`**（gorm.Open 在 Initialize 之前调用）：只记录是否需要安装默认 `NowFunc`。最终模式确定后，Initialize 为未自定义 NowFunc 的用户安装对应精度策略：MySQL 默认毫秒，Oracle 默认微秒。不能在 Apply 中把尚未解析的 auto 当成 MySQL；两阶段都不得覆盖用户的 NowFunc。

```
Initialize(db)
 ├─ 1. 校验输入互斥关系，记录 DSN 显式参数；ParseDSN 或复制 DSNConfig
 ├─ 2. 解析模式；仅 DSN mode=auto 使用临时池与 SessionVariable 探测（§4.1）
 ├─ 3. 内部配置：写入具体模式、开启 ParseTime、MySQL 档案默认 8，校验 RowsAffectedMode
 ├─ 4. 创建正式 NewConnector→sql.OpenDB；外部 Conn 遵守 §4.2 前提，先仅保存在局部变量
 │     NewConnector 会复制配置；第 3 步必须在此之前完成，不能修改原配置补救
 ├─ 5. 版本探测：MySQL SELECT OB_VERSION()；Oracle SELECT ob_version() FROM dual
 │     MySQL 仅在函数不支持时回退 SELECT VERSION()；网络、权限等错误直接返回
 │     按真实 OB 版本设置能力；未知版本保守处理，安装默认 NowFunc
 ├─ 6. callbacks.RegisterDefaultCallbacks(db, &callbacks.Config{CreateClauses:…, …})
 │     MySQL: Create=[INSERT,VALUES,ON CONFLICT], Update/Delete 含 LIMIT
 │     Oracle: 保留事务、hook、关联回调；替换 gorm:create，统一构建并执行整条语句（§5.3）
 ├─ 7. 注册其他 ClauseBuilders（不覆盖用户已注册的同名子句）
 └─ 8. 所有可能返回错误的初始化步骤成功后，才将候选连接池赋给 db.ConnPool
```

内部池直接使用 `*sql.DB`，用户通过 `db.DB()` 获取连接池。GORM 的 `GetDBConnector` 查询对象是 ConnPool；仅在确有池包装时由包装实现，不给 Dialector 添加无效接口。Dialector 实现 `SavePointerDialectorInterface`（两种模式 OB 都支持 SAVEPOINT）。

**Initialize 失败的所有权边界**：GORM v1.31.2 在 Dialector.Initialize 返回错误后会通过 `db.DB()` 取池并 Close；仅做到 Dialector 自己不调用外部池 Close 并不足够。版本查询等步骤使用局部候选池，所有可失败步骤结束后才挂载到 `db.ConnPool`。若实现确需提前挂载，错误返回前须清除 `db.ConnPool` 和已设置的 `db.Statement.ConnPool`，防止 GORM 追溯关闭外部池或事务所属池。失败时由 Dialector 关闭自己创建的临时/正式池，外部 Conn 始终归调用者所有。验收必须从 `gorm.Open` 入口注入版本查询等初始化错误，并确认原池仍可查询、原事务仍可使用；不能只直接调用 Initialize 测试。

### 4.1 模式的确定与默认值

GORM 的 SQL 生成要求**同一 GORM DB 实例内模式一致**。顶层 Mode 的零值保留为“未指定”，不同时表示显式 auto。解析优先级：

```
Config.Mode（非零） > 输入配置的模式 > 默认 MySQL
输入配置：DSN 显式 mode（含 auto），或 DSNConfig.Mode 的非零值
```

- 未指定模式时默认 MySQL；结构化配置的 ModeAuto 零值也按未指定处理。需要自动探测时使用 DSN `mode=auto`，不引入第二种零值含义。
- auto 使用临时 Connector（ModeAuto）取得 `*sql.Conn`，调用 `SessionVariable(ctx, conn, "ob_compatibility_mode")`。已有 API 会按物理连接实际模式构建 SQL；只接受非 NULL 的 MYSQL/ORACLE 值（忽略大小写），未知值、超时、权限及其他查询错误均返回初始化错误。
- 关闭探测连接与临时池，再用具体模式和最终配置创建正式池。探测作用域只做驱动连接初始化与模式读取，不执行 ORM CRUD；调用者的 Session.InitSQL 等连接初始化配置会随探测连接执行，不能假设整个 gorm.Open 只建一条物理连接。不能尝试修改临时 Connector 已复制的配置。无须新增 PinnedMode API 或驱动 PR。
- 同一 GORM DB 不支持混合模式租户；内部正式 Connector 的具体 Mode 与 `ErrModeMismatch` 共同约束后续物理连接。M1 尚未交付 Oracle 时，检测到 Oracle 返回明确的未支持错误，不能套用 MySQL 方言。

### 4.2 时间与外部连接配置

- **内部连接**：DSN/DSNConfig 都转换为结构化配置，经适配策略处理后走 NewConnector。DSN 显式 `parseTime=false` 返回配置错误；DSN 未指定或 DSNConfig 的 bool 零值统一设置 `Session.ParseTime=true`。这是 GORM 普通 `time.Time` 字段的连接前提。
- **MySQL 档案**：优先顶层非零 `CompatibleMysqlVersion`，其次 DSN 显式参数或 DSNConfig 中的非零值，否则为 8。必须在 ParseDSN 应用默认 5 之前记录 DSN 参数是否出现；已由调用者 ParseDSN/Normalize 得到的 DSNConfig 中的 5 按已配置值保留，不猜测其来源。Oracle 不注入 MySQL 档案。
- **外部 Conn**：与 DSN/DSNConfig 互斥，必须显式提供具体 Mode；不修改或重建调用者的池。调用者负责开启 ParseTime、保证模式一致，并选择 MySQL 档案 8（若选择 5，接受其 TIME 结果小数损失）。跳过版本查询不豁免这些前提。
- **版本**：Oracle 读取真实 `ob_version()`，不使用 Oracle 兼容视图中的版本号判断 OB 功能。`ServerVersion` 已提供时使用该值；无版本且跳过查询时按未知处理。`SkipInitializeWithVersion` 仅作用于方言层，驱动自己的版本探测由 `Session.SkipVersionProbe` 独立控制。

---

## 5. 方言行为对照

| 维度 | MySQL 模式 | Oracle 模式 |
| --- | --- | --- |
| `Name()` | `"oceanbase"`（内部区分 flavor） | 同左 |
| `QuoteTo` | 反引号，`` ` `` 转义（照搬 mysql driver 状态机） | 双引号 `"`，转义 `""`；**未加引号路径的标识符按大写处理**（OB Oracle 未加引号标识符默认大写，GORM snake_case 模型名需归一） |
| `BindVarTo` | `?` | `?`（驱动双模式支持位置参数） |
| `DefaultValueOf` | `DEFAULT`（INSERT 可写 `VALUES (DEFAULT)`） | 表示使用数据库默认值的标记，由 Oracle Create 回调按省略列处理；不得替换为 NULL（见 §5.3） |
| NowFunc（Apply 记录、Initialize 安装） | 按 `DefaultDatetimePrecision` 截断 | 截断到微秒（TIMESTAMP 默认精度 6） |
| LIMIT/OFFSET | 原生 | `ClauseBuilders["LIMIT"]` 改写为 `OFFSET n ROWS FETCH FIRST m ROWS ONLY` |
| ON CONFLICT | `ON DUPLICATE KEY UPDATE`（含 `excluded` → `VALUES(col)` 改写，同 mysql driver） | Oracle Create 回调按主键/冲突目标分流 MERGE 或 INSERT RETURNING；支持边界见 §5.3 |
| 批量插入（`Create(&slice)`/CreateBatchSize） | 原生多行 `VALUES (...),(...)` | Create 回调整句构建 INSERT ALL；需要主键回填的记录逐条 INSERT RETURNING INTO，保留输入顺序（M2） |
| FOR SHARE | `FOR SHARE`/`LOCK IN SHARE MODE`（按版本） | `FOR UPDATE`（共享锁语义退化，文档标注） |
| SavePoint | `SAVEPOINT` / `ROLLBACK TO SAVEPOINT` | 同左 |
| `Explain` | `logger.ExplainSQL(sql, nil, '\'', vars...)` | 同左（占位符同为 `?`） |

### 5.1 DataTypeOf 类型映射

**MySQL 模式**（对齐 gorm mysql driver，`getSchema*Type` 系列原样落地）：

| schema.DataType | SQL 类型 |
| --- | --- |
| Bool | `boolean`（OB 4.x 支持） |
| Int/Uint | `tinyint/smallint/mediumint/int/bigint`（按 Size 分档，Uint 加 `unsigned`，自增加 `AUTO_INCREMENT`） |
| Float | `decimal(p,s)` / `float` / `double` |
| String | `varchar(n)` / `mediumtext` / `longtext`；零配置普通 string 为 `longtext`，191 仅适用于下述条件 |
| Time | `datetime(p)`（默认精度 3，可配） |
| Bytes | `varbinary(n)` / `mediumblob` / `longblob` |

字符串规则按 mysql v1.6.0：优先使用字段 Size；未指定时使用非零 `DefaultStringSize`。二者均未指定时，主键、带默认值或 `INDEX`/`UNIQUE` tag 的字段取 191，其余取 `longtext`。确定长度后，65536 至 2²⁴（含）使用 `mediumtext`，超过 2²⁴ 使用 `longtext`，其余正长度使用 `varchar(n)`；显式 SQL 类型 tag 透传。不把所有未标长度的字符串统一限制为 191。

**Oracle 模式**：

| schema.DataType | SQL 类型 |
| --- | --- |
| Bool | `NUMBER(1)` |
| Int | `NUMBER(n,0)`（Size→精度：≤8→3、≤16→5、≤32→10、64→19；自增见 §6） |
| Uint | `NUMBER(n,0)`（Size→精度：≤8→3、≤16→5、≤32→10、64→20；uint64 最大值为 18446744073709551615） |
| Float | `NUMBER(p,s)` / `BINARY_FLOAT` / `BINARY_DOUBLE` |
| String | `VARCHAR2(n)`（默认 512）/ `NVARCHAR2` / `CLOB`（超长） |
| Time | `TIMESTAMP(p)` / `TIMESTAMP WITH TIME ZONE` / `TIMESTAMP WITH LOCAL TIME ZONE`（tag 可选） |
| Bytes | `RAW(n)` / `BLOB` |
| 自定义 | 显式 `gorm:"type:真实SQL类型名"` 透传；`types.Object/Array` 的前提与限制见 §5.2 |

### 5.2 驱动特殊类型的适配边界

- `types.Object/Array` 不能仅凭 Go 类型推断数据库命名类型。模型必须使用真实类型 tag（例如 `gorm:"type:MY_OBJECT_T"`），并提供匹配的 Schema、Type 和有序属性；不带 tag 的 Object 可能被 GORM 推断为 string。
- 复杂类型结果要求服务器预处理的二进制查询。使用驱动 `PrepareServer`，并通过 GORM `PrepareStmt: true` 确保没有绑定参数的 SELECT 也被预处理；仅设置驱动默认 PrepareMode 不能保证无参数查询走此路径。普通文本查询返回复杂类型的 ErrUnsupported 不应被隐藏。
- 类型创建和服务端存储支持必须单独验收。OBJECT 列可用不代表命名集合列也可用，PL/SQL Array 绑定可用也不代表支持数组列；AutoMigrate 不负责创建自定义类型。
- ParseTime 解决普通 DATE/TIMESTAMP 到 `time.Time` 的读取；带时区/本地时区的结果须使用驱动匹配的 Scanner 类型等保真路径，不能承诺所有 TIMESTAMP 变体自动映射到 `time.Time`。

### 5.3 Oracle Create、默认值与 upsert

Oracle 替换 `gorm:create` 执行回调，保留 GORM 默认的事务、模型 hook 和关联保存回调。该回调读取 VALUES、ON CONFLICT 及 schema 上下文，统一构建并执行 INSERT、INSERT ALL 或 MERGE；不依赖未列入 BuildClauses 的 ON CONFLICT builder，也不在已经写入 INSERT INTO 的 VALUES builder 后追加整条 INSERT ALL。

1. **默认值**：按 GORM 的字段/default tag 和输入形式区分“省略列”和“写入 NULL”。带 default tag 的结构体零值按 GORM 约定触发默认值，不能仅凭 nil 指针推断用户要求显式 NULL；显式 NULL 用 map 中存在的 nil 值或 `gorm.Expr("NULL")` 等可区分输入表达。生成主键、数据库函数默认值等未提供的字段通过省略列触发默认值；显式 NULL 保留为 NULL。DefaultValueOf 返回可识别的默认值标记，必须在构建最终语句前消费，不能把缺省字段统一绑定成 NULL。
2. **批量插入**：只将列集合相同、无需生成主键回填且可用 INSERT ALL 表达的相邻记录合并；列集合不同、仅含缺省字段或无法安全批量表达时逐条执行。涉及生成主键时使用单行 INSERT RETURNING INTO 并回填原记录，禁止推算连续 ID。所有路径保持输入顺序与当前事务，默认值、混合显式/生成 ID 和关联保存都是 M2 验收项。
3. **MERGE**：明确 ON CONFLICT 的匹配列，未指定列时使用模型完整主键；无可用目标时拒绝，目标必须有唯一约束。按列构造插入/更新分支，`UpdateAll` 展开时排除 ON 列；用户显式要求修改 ON 列时返回不支持，不能静默删除该赋值。DoNothing 省略更新分支。不支持的冲突目标、条件或表达式必须在对应 Create 回调的 DML 前返回明确错误，不能静默丢弃语义；同一 Create 回调处理的批次先全部校验，再执行该批次任何一条 DML。事务中多个会话竞争同一未存在键时，服务器仍可能报唯一约束冲突，按错误翻译规则返回，不自动重试。
4. **生成主键与关联保存**：GORM 的关联回调会给新对象自动添加 ON CONFLICT，不能一见零主键就拒绝。对单列自动生成主键，若有效冲突目标仅为该主键、当前值按 GORM 约定缺省，且冲突选项属于支持范围，则使用单行 INSERT RETURNING INTO 并回填原对象；已有主键的记录仍走 MERGE。该规则按 schema/目标/输入值判断，同样适用于显式调用和 `Save(&slice)`，不猜测子句来源。新关联对象的 ID 必须在后续外键或中间表写入前可用；混合新旧记录按输入顺序逐条分流。发生唯一冲突时传播错误，不重放写入，也不按其他唯一键寻找或合并关联对象。
5. **M2 upsert 边界**：MERGE 分支要求主键已提供或预分配，不承诺其返回服务器生成的主键。按非主键唯一列匹配且仍需生成/回填主键的 upsert 在对应回调 DML 前拒绝，提示先按 §6 取号；不能借用第 4 条绕过限制。按非主键匹配的 MERGE 不回填已有行的主键，不能将输入 ID 当作查询所得的目标行 ID。非主键数据库默认字段的回填、通用 clause.Returning 和 Update/Delete RETURNING 属于 M3。
6. **失败语义**：RETURNING 不支持或输出解码失败时传播错误，不回退到 LastInsertId、MAX(id) 或重新插入。默认事务据错误回滚；关闭默认事务时此前成功的 hook、关联或批量写入可能已落库。回调内预校验不承诺撤销更早执行的 hook 或关联写入，文档与测试须明确这一边界。

---

## 6. Oracle 模式下的主键生成与回填（M2 必需）

驱动提供 SQL 执行与 OUT 参数能力，方言负责建表、选择生成策略和回填模型。Oracle 不能依赖 MySQL 的 LastInsertId；在尚不知道 ID 时“按主键回查”无法成立。M2 必须完成基础主键 RETURNING INTO，无须为此修改驱动。

### 6.1 IDENTITY

模型声明自增时，Oracle Migrator 的目标 DDL 为 `id NUMBER(20,0) GENERATED BY DEFAULT AS IDENTITY`（uint64；int64 使用 NUMBER(19,0)）。普通插入省略零值 ID，并用 `INSERT ... RETURNING id INTO ?` 配合 `sql.Out{Dest: &id}` 取回主键；执行走当前 ConnPool 的 ExecContext，由 Oracle Create 回调回填模型，不进入默认 Create 的 LastInsertId 分支。

4.2.1 是原方案提出的候选最低版本，**本次走查未验证其 IDENTITY 支持门槛**。该门槛及 DDL/RETURNING 组合须经真实版本验收后写入兼容矩阵；未知版本或已知低于门槛时，在执行建表 DDL 前返回不支持并提示显式主键/序列路径。不得先建表失败再自动尝试另一种 schema。

`BY DEFAULT` 不等于 `BY DEFAULT ON NULL`，不把显式 NULL 当成自动编号。批量的生成 ID 逐条获取并回填；已有 ID 原样插入，不重新生成。

### 6.2 显式序列预分配

不使用 IDENTITY 时，先显式创建序列 `CREATE SEQUENCE my_seq`，再在当前事务中通过 Oracle 表达式 `my_seq.NEXTVAL` 取号，将 ID 设置到模型后 Create。示意代码：

```go
type User struct {
    ID uint64 `gorm:"primaryKey;autoIncrement:false"`
}

user := User{}
err := db.Transaction(func(tx *gorm.DB) error {
    if err := tx.Raw("SELECT my_seq.NEXTVAL FROM dual").Scan(&user.ID).Error; err != nil {
        return err
    }
    return tx.Create(&user).Error
})
```

序列值消耗不因事务回滚而恢复，ID 不保证连续。OracleMigrator 可提供 `HasSequence/CreateSequence` 扩展方法（不属于 GORM 通用 Migrator 接口），序列标识符必须引用转义。不要使用 PostgreSQL 风格的 nextval 函数，也不要将数据库默认表达式误认为模型 ID 回填机制。

### 6.3 必测行为

覆盖 IDENTITY、预分配序列、显式主键；单行/批量、混合显式 ID 与生成 ID、uint64 边界、事务回滚与关闭默认事务后的错误。关联必须覆盖新建 belongs-to/has-one/has-many/many-to-many、混合新旧对象及 FullSaveAssociations，验证主键回填、外键与中间表引用和已有对象更新；另测 Save(slice) 的零主键分流及非主键冲突目标的拒绝。RETURNING 的真实成功用例与失败不重放用例均通过后，才能声明 Oracle Create 可用。

## 7. 错误翻译（ErrorTranslator）

`Translate(err)` 以 `var serverErr *dberrors.Error; errors.As(err, &serverErr)` 取原始错误码，按已固化模式查两张表：

- MySQL 模式：`1062→gorm.ErrDuplicatedKey`，`1451/1452→gorm.ErrForeignKeyViolated`（MySQL 协议错误码在 OB MySQL 模式一致）
- Oracle 模式：`1(ORA-00001)→ErrDuplicatedKey`，`2291/2292(ORA-02291/2292)→ErrForeignKeyViolated`
- 未命中原样返回；两表按模式隔离，避免码值冲突

配合 `gorm.Config{TranslateError: true}` 生效。

## 8. 驱动扩展能力的暴露（不走 Dialector 接口）

桥接遵守驱动实际签名：ExecBatch 接受 Execer；LOB、游标和元数据使用的 Session 支持非 nil 的 `*sql.Conn` 或 `*sql.Tx`；XA 要求固定连接，并且不能与本地 sql.Tx 混用。直接使用已有驱动 API，不把它们统一限制成另取一条连接。

可选 `options.go` 仅提供：

```go
// WithPinnedConn 复用当前 *sql.Conn 或从普通池借出连接；拒绝事务上下文
func WithPinnedConn[T any](db *gorm.DB, fn func(ctx context.Context, conn *sql.Conn) (T, error)) (T, error)
```

- 当前为普通池时借出一条连接，在回调退出后归还；当前已为 `*sql.Conn` 时复用且不关闭调用者的连接。保留 GORM Statement.Context，兼容 PrepareStmt 包装；无法识别所有权时返回错误。
- 当前为事务（含 PrepareStmt 包装的事务）时，WithPinnedConn 返回明确错误，不能通过 `db.DB()` 另取连接。事务内 ExecBatch/LOB/游标/元数据应直接传入当前 `*sql.Tx`，复用原提交/回滚边界；无法取得原事务时拒绝操作。
- XAStart/End/Prepare 等属于同一分支的操作必须在同一次固定连接作用域中完成。LOB、游标等句柄不得逃出其所属连接/事务的生命周期。
- M3 示例覆盖事务内写入后回滚、单连接池不另借连接、上下文取消与 XA 混用拒绝；不增加重复包装驱动 API 的函数。

## 9. 里程碑

| 阶段 | 范围 | 验收 |
| --- | --- | --- |
| **M1：MySQL 模式可用** | 配置合并与 SessionVariable 模式探测、连接前默认策略、MySQL 方言、Migrator、错误翻译、真实 OB 版本解析 | DryRun SQL 基线；真实 MySQL 租户 CRUD/关联/事务/迁移；parseTime 与档案 5/8、datetime/TIME 精度验收 |
| **M2：Oracle 模式可用** | Oracle Create 回调、默认值省略、INSERT ALL 与逐条回填路径、受限 MERGE（§5.3）、FETCH FIRST、IDENTITY/序列、基础主键 RETURNING INTO、ALL_* Migrator、错误翻译 | 真实 Oracle 租户 CRUD/关联/迁移；单行/批量主键回填、显式 NULL、类型边界、失败事务语义；记录实际验证版本 |
| **M3：增强** | 非主键默认字段与通用 Create/Update/Delete RETURNING、事务边界明确的驱动扩展示例、OBProxy 配置文档 | RETURNING 与事务桥接真实验收；兼容性矩阵（docs/compatibility.md）落稿 |

## 10. 测试策略

1. **单元与协议模拟（无真实库）**：输入互斥与优先级、DSN 显式参数来源、Connector 实际收到的最终配置、auto 返回 MYSQL/ORACLE/NULL/未知值/错误、NowFunc 的初始化时机与用户覆盖、QuoteTo、类型边界、错误码与版本解析；从 gorm.Open 注入初始化错误，验证外部池/事务未被关闭。
2. **SQL 与执行路径**：MySQL 对拍基线含普通/索引字符串及 DefaultStringSize；Oracle 完整 INSERT/INSERT ALL/MERGE 快照，不只测试单个 builder；主键 OUT 回填原记录、无 LastInsertId 分支、default tag 零值与显式 NULL、混合 ID、错误不重放、hook/关联/事务生命周期；覆盖关联自动 ON CONFLICT、Save(slice)、非主键目标拒绝、批量预校验及 ON 列更新拒绝。模拟测试不能替代服务端语法验收。
3. **集成（build tag `integration`）**：环境变量 `OB_MYSQL_DSN` / `OB_ORACLE_DSN` 提供真实租户。除 CRUD、关联、事务/SavePoint、AutoMigrate 增量变更、并发外，覆盖 datetime(3)/(6) 与 TIME(3)/(6)、档案 5/8、ParseTime、Oracle 生成主键/序列、NUMBER 的 int64/uint64 边界、Object/Array 受限路径及负例。IDENTITY 门槛、默认值和 MERGE 功能按实际版本记录结果。
4. **CI 与声明范围**：默认运行无库测试，另用 `go test -tags=integration ./tests/integration -run '^$'` 验证集成测试编译；真实验收显式运行 `go test -tags=integration ./tests/integration -run MySQL` 或 `-run Oracle`。未提供 DSN 时的 skip 不能记作真实验收通过；v1.25+ 的 GORM 兼容声明要求单独版本矩阵。

## 11. 风险与开放问题

| # | 风险/问题 | 应对 |
| --- | --- | --- |
| R1 | MySQL 兼容版本前缀或 Oracle 兼容视图版本被误当成 OB 版本 | 两种模式查询真实 ob_version()，Oracle 加 FROM dual；MySQL 有限回退 VERSION() 并按 OceanBase 标记解析；未知版本禁用未经确认的能力 |
| R2 | Oracle 模式标识符大小写（模型 snake_case vs 未加引号自动大写） | QuoteTo 统一大写化未引号标识符；文档显著说明；集成测试覆盖 |
| R3 | 仅改 VALUES/ON CONFLICT builder 会残留 INSERT 前缀或跳过 upsert | M2 替换 gorm:create，统一构建/执行整句；快照覆盖前缀、默认值、冲突分支和批量路径 |
| R4 | 驱动仍在开发期（README 标注 under development） | 正常 `go get github.com/cocosip/go-oceanbase` 引用远程模块（无 replace）；Dialector 只依赖驱动稳定面（Config/DSN/dberrors/types），驱动不兼容变更时升版本号跟进 |
| R5 | 混合模式/多租户同池 | 明确不支持；Initialize 固化模式 + 驱动 ErrModeMismatch 兜底 |
| R6 | 档案 5 截去 TIME 结果小数；驱动默认 ParseTime=false 导致 time.Time 扫描失败 | 正式 Connector 创建前注入默认档案 8 和 ParseTime=true；显式档案 5 保留并验证差异，分别测试 TIME 与 DATETIME，不能混为同一问题 |
| R7 | Oracle 缺少插入 ID 时默认 GORM Create 报 ErrUnsupported；按未知主键回查无效 | 基础主键 RETURNING INTO 纳入 M2；生成 ID 批量逐条回填，禁止写入重放 |
| R8 | NULL 替代默认值或混合 ID 批量导致数据语义错误 | 缺省字段省略列，显式 NULL 保留；逐条或按相邻相同列集合批量构建 |
| R9 | 配置在 NewConnector 复制后才注入；外部池无法写回 | 内部先解析、探测、合并再创建正式池；外部池使用独立且明确的配置前提 |
| R10 | NUMBER(19) 不覆盖 uint64；Object/Array 类型与查询路径存在条件 | uint64 使用 NUMBER(20,0)；自定义 SQL 类型 tag 与服务器预处理，验收服务端真实存储边界 |
| R11 | 扩展桥接另借连接逃逸 GORM 当前事务 | WithPinnedConn 拒绝事务；事务内直接复用 sql.Tx，测试回滚、单连接池与包装类型 |
| R12 | IDENTITY 的 4.2.1 门槛及最低 GORM 版本尚未实际验收 | 记录为待验证要求；真实版本矩阵通过前不声明相应支持，不以文档修订作为验收 |
| R13 | GORM 在 Initialize 失败后关闭已挂载的外部池 | 候选池延迟挂载，提前挂载的错误路径解除引用；通过 gorm.Open 验证调用者的池和事务仍可用 |
| R14 | 关联自动 ON CONFLICT 与生成主键互相冲突 | 主键目标且生成值缺省时 INSERT RETURNING，已有主键 MERGE；关联全类型、混合记录和 Save(slice) 均纳入 M2 |

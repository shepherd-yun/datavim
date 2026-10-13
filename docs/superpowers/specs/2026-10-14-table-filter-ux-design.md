# 表数据过滤体验改进 设计文档

- 日期：2026-10-14
- 状态：待审阅
- 范围：表数据 tab 的过滤交互（`f` / `F`），以及所有弹框共用的单行输入框

## 1. 背景

v0.6 加入了表数据过滤：`f` 按当前格的值过滤，`F` 打开一个输入框手写 `WHERE` 条件。实际使用中的问题：

1. `F` 里列名要自己输入，什么时候要加引号（大小写混写、保留字）得自己判断。
2. `LIKE '%abc%'` 这类常用条件要完整手写。
3. 输入框只能在末尾追加，`←` `→` 等光标键没有反应，写错了只能删掉重来。
4. 条件写错导致数据库报错后，再按 `F` 回到的是旧条件，刚才输入的内容丢了。

作者的主要用法是**单列的简单查找**：某列等于 / 包含某个值、为空，偶尔叠加两三个条件。

## 2. 目标

- 日常的单列查找不用写 SQL：`f` 下的快捷菜单覆盖等于、不等于、包含、大于、小于、为空、非空、清除。
- 复杂条件仍然手写，但 `F` 要写得顺手：光标可以移动、`Tab` 补全列名并自动加引号、出错不丢输入。
- 所有弹框共用的输入框都支持光标编辑。

**不做：** 过滤历史（↑↓ 翻历史）；结构化的多条件过滤面板；输入框的 vim 模式。

## 3. 设计

### 3.1 单行输入框支持光标（`overlay/input.go`）

`field` 增加光标位置 `pos`（rune 下标），编辑都发生在光标处。按键是 readline 风格：

| 键 | 作用 |
|---|---|
| `←` `→`，`Ctrl-b` `Ctrl-f` | 左右移动一个字符 |
| `Home` `End`，`Ctrl-a` `Ctrl-e` | 跳到行首 / 行尾 |
| `Alt-←` `Alt-→`，`Alt-b` `Alt-f` | 跳到上一个 / 下一个词首（词 = 字母、数字、下划线组成的连续串） |
| `Backspace` / `Delete` | 删光标前 / 光标处的字符 |
| `Ctrl-w` | 删光标前一个词（连同它前面的空白） |
| `Ctrl-u` / `Ctrl-k` | 删到行首 / 删到行尾 |
| 普通字符、粘贴 | 插入到光标处 |

- 新建的 `field` 光标默认在末尾。
- `field` 提供 `View(width int) string`：光标处的字符反色显示（在末尾时画一个反色空格）。宽度不够时横向滚动，保证光标可见，两侧被截掉的部分用 `…` 表示。显示宽度按 `ansi.StringWidth` 计算，能处理中文。
- 另有 `ViewMasked(width int, mask rune)`，把每个字符画成 `mask`（`•`），光标规则相同，供密码类输入使用。
- 使用方：`Prompt`、`CmdLine`（`:` 和 `/`）、`Confirm`（输入名字确认删除）、`ConnForm`（密码字段用 `ViewMasked`）、`Password`、`Secret`（SSH 口令，用 `ViewMasked`）。它们原来自己画的 `value + "█"` 改成调用 `field.View` / `ViewMasked`。
- `CmdLine` 原有行为保持不变：输入为空时按 `Backspace` 关闭命令行。
- `Ctrl-u` 的语义从"清空"变成"删到行首"。光标在末尾时两者效果一样。

### 3.2 `F`：手写 WHERE 的编辑器

仍然用 `overlay.Prompt`，增加两个可选能力（用选项函数传入，其它使用方不受影响）：

- **`WithCompleter(func(text string, cursor int) (start int, cands []complete.Candidate))`**：按 `Tab` 时调用，弹出候选列表。
- **`WithValidate(func(text string) error)`**：按 `Enter` 时先校验，出错时框不关闭，把错误红字显示在框里；用户修改后错误消失。

**补全**

- 取光标前的当前词（字母、数字、下划线、`"`、`` ` ``）作为前缀。
- 候选包括：
  - 这张表的列名。显示原名并标注类型，插入的文本经过 `sqlx.QuoteIfNeeded`。列信息用 data tab 已有的 `t.cols`，不需要再查元数据。
  - 关键字 `AND OR NOT LIKE ILIKE(仅 PG) IN IS NULL BETWEEN TRUE FALSE`。
- 排序用 `complete.Rank`，弹窗渲染复用 `complete.Model`，画在输入行下方。
- 弹窗打开时：`Tab` / `↓` / `Ctrl-n` 下一个，`Shift-Tab` / `↑` / `Ctrl-p` 上一个，`Enter` 接受（替换当前词），`Esc` 只关闭弹窗，继续输入任意字符也会关闭弹窗。
- 只有一个候选时，`Tab` 直接补全，不弹窗。

**预填**

- 当前没有过滤条件时，预填 `<当前列名> `（需要时加引号）。没有当前列时为空。
- 已有过滤条件时，预填现有条件。
- 上一次 `F` 的条件在数据库端执行失败时（见下文），预填那条失败的条件。
- 光标都放在末尾。

**提示文案**

标签行：`WHERE …  例: name LIKE '%abc%' AND age > 18`；底部：`tab 补全 · enter 确定 · esc 取消 · 空 = 全部行`。

**出错处理**

- **本地校验**：`WithValidate` 调用 `tableQuery.check`（多条语句、不是查询）。出错时框不关闭。
- **数据库报错**：比如列名拼错，仍然显示在 tab 的错误行，原有数据保留。data tab 新增字段 `failedWhere`：用新条件加载失败时记下这条条件，加载成功时清空。下次按 `F` 优先预填它。
- 当前有未提交的修改时，行为不变：按 `F` 提示先提交或丢弃，不打开框。

### 3.3 `f`：当前列的快捷过滤菜单

`f` 从单键动作改成 Grid context 下的前缀键。停顿后 which-key 弹窗会列出后续按键（复用现有机制，不需要新代码）。

| 按键 | 动作 | 生成的条件 | 交互 |
|---|---|---|---|
| `ff` | FilterEqual | `col = <值>`；NULL 格生成 `col IS NULL` | 用当前格的值，立即生效（即原 `f`） |
| `f!` | FilterNotEqual | `col <> <值>`；NULL 格生成 `col IS NOT NULL` | 立即生效 |
| `fs`，`f~` | FilterContains | PG：`col::text ILIKE '%v%'`；MySQL：`col LIKE '%v%'` | 弹框输入 v，预填当前格的文本 |
| `f>` | FilterGreater | `col > <v>` | 弹框输入 v，预填当前格的文本 |
| `f<` | FilterLess | `col < <v>` | 弹框输入 v，预填当前格的文本 |
| `fn` | FilterNull | `col IS NULL` | 立即生效 |
| `fN` | FilterNotNull | `col IS NOT NULL` | 立即生效 |
| `fx` | FilterClear | 清空过滤条件 | 立即生效 |

- 新条件用现有的 `andFilter` 和已有条件以 AND 连接，然后走 `applyQuery`：回到第一页，保持当前列。
- 列名用 `sqlx.QuoteIdent`，值用 `sqlx.Literal(dialect, col.DatabaseType, v)`，和现在的 `valueFilter` 一样。
- **包含**：先把 v 里的 `\` `%` `_` 转义成 `\\` `\%` `\_`，前后加 `%`，再按文本类型交给 `sqlx.Literal`（MySQL 下它会再把 `\` 加倍，所以最后到达 LIKE 的转义符是正确的）。两种数据库 LIKE 的默认转义符都是 `\`。PG 加 `::text` 是为了让数字、uuid、时间等列也能"包含"查找；用 ILIKE 是为了不区分大小写，和 MySQL 默认排序规则下的 LIKE 一致。
- **值输入框**：标题如 `name 包含`、`age >`。输入为空时按 `Enter` 等于取消。
- 光标不在任何格上（空表）时，状态栏提示 `no cell to filter by`。`fn` `fN` `fx` 只需要当前列；`fx` 什么都不需要。
- 有未提交的修改时，和其它过滤动作一样被拦下并提示。

keymap 变化（`keymap/defaults.go`、`keymap/keymap.go`）：

- 删除 `FilterValue`（`f`），新增上表中的 8 个动作。
- `F`（FilterPrompt）不变。
- Help 和 README 的描述同步更新。

## 4. 涉及的文件

| 文件 | 改动 |
|---|---|
| `internal/ui/overlay/input.go` | `field` 增加光标、编辑键、`View`、`ViewMasked` |
| `internal/ui/overlay/prompt.go` | 用 `field.View`；增加 `WithCompleter`、`WithValidate`；补全弹窗 |
| `internal/ui/overlay/cmdline.go`、`confirm.go`、`connform.go`、`password.go`、`secret.go` | 改用 `field.View` / `ViewMasked` |
| `internal/ui/keymap/keymap.go`、`defaults.go` | 新的过滤动作和绑定 |
| `internal/ui/tablequery.go` | `containsFilter`、比较条件、NULL 条件等生成函数 |
| `internal/ui/tabs.go` | `f` 菜单各动作的分发、值输入框、`F` 的预填 / 补全 / 校验、`failedWhere` |
| `README.md`、`CHANGELOG.md` | 按键说明 |

## 5. 测试

- **`field` 单元测试**：每个编辑键在行首、行中、行尾的效果；中文；粘贴插入到光标处；`View` 在窄宽度下滚动后光标仍可见。
- **`Prompt` 测试**：`Tab` 补全（单个候选直接补全、多个候选弹窗选择、`Esc` 只关弹窗）；校验失败时框不关闭并显示错误。
- **条件生成测试**（两种方言）：`=`、`<>`、NULL 格、包含的转义（值里带 `% _ \ '`）、数字列不加引号、需要加引号的列名。
- **集成测试**（`press(...)` 风格）：`ff` 和原 `f` 行为一致；`fs` 输入值后生成的 SQL；`fx` 清空；`F` 在空过滤时预填当前列；数据库报错后再按 `F` 预填失败的条件；有未提交修改时被拦截。
- 现有使用 `f` / `keymap.FilterValue` 的测试（`tabs_test.go`、`datatab_edit_test.go`、`keymap_test.go`）改用 `ff` / `FilterEqual`。

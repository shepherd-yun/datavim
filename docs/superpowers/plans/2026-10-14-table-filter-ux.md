# 表数据过滤体验改进 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让表数据过滤顺手：所有弹框的单行输入框支持光标编辑；`f` 变成当前列的快捷过滤菜单（等于 / 不等于 / 包含 / 大于 / 小于 / 为空 / 非空 / 清除）；`F` 的 WHERE 编辑器预填当前列、`Tab` 补全列名与关键字、本地校验不关框、数据库报错后再按 `F` 带出失败的条件。

**Architecture:** `overlay.field` 加光标位置和 readline 编辑键，并自己负责绘制（`View` / `ViewMasked`，光标反色、横向滚动）；所有用它的弹框改调这两个方法。`overlay.Prompt` 增加选项函数 `WithCompleter` / `WithValidate`，补全弹窗复用 `panes/complete`。过滤条件的生成是 `internal/ui/tablequery.go` 里的纯函数；`dataTab` 负责把 keymap 动作接到这些函数和弹框上。

**Tech Stack:** Go、Bubble Tea v2（`charm.land/bubbletea/v2`）、Lip Gloss v2、`github.com/charmbracelet/x/ansi`；不新增依赖。

**Spec:** `docs/superpowers/specs/2026-10-14-table-filter-ux-design.md`

## Global Constraints

- v0.1–v0.6 计划的 Global Constraints 继续有效，特别是：**英文 UI 文案**、UI 层不做 IO、Conventional Commits + attribution（`Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`）、测试绝不碰真实系统剪贴板。
- spec 里用中文写的文案（如 `name 包含`、`tab 补全 · enter 确定`）是示意，实现用英文：标题 `Filter <table>`，值输入框标签 `<col> contains …` / `<col> > …` / `<col> < …`，`F` 的标签 `WHERE …  e.g. name LIKE '%abc%' AND age > 18 · empty shows every row`，有补全时底部 `tab complete · enter ok · esc cancel`。
- 不新增依赖；`go.mod` 不变。
- 每个任务结束时：`gofmt -l .` 无输出、`go vet ./...` 通过、`go test ./...` 全绿。
- 光标位置一律是 **rune 下标**（不是字节下标）。

## Review Focus

1. **中文等宽字符**：输入框里有中文时，横向滚动后显示宽度不超过给定宽度，光标仍可见。→ Task 1 `TestFieldViewWideRunes`。
2. **真实终端的 Alt 键**：某些终端发来的 `alt+b` 带着 `Text: "b"`，不能当普通字符插入。→ Task 1 `TestFieldIgnoresAltText`。
3. **密码不泄露**：掩码视图里在任何光标位置都不出现明文。→ Task 1 `TestFieldViewMasked`。
4. **「包含」的值带 `% _ \ '`**：两种方言下生成的 LIKE 都正确转义，并通过 `tableQuery.check`。→ Task 3 `TestFilterConditions` 和 Task 4 `TestFilterMenuContains`。
5. **按了 `f` 又反悔**：`f` 后按 `Esc` 或无关的键，不发任何查询，也不打开弹框。→ Task 4 `TestFilterMenuPrefixCancels`。

---

### Task 1: 单行输入框支持光标

**Files:**
- Modify: `internal/ui/overlay/input.go`（整体重写）
- Modify: `internal/ui/overlay/prompt.go`、`cmdline.go`、`confirm.go`、`password.go`、`secret.go`、`connform.go`（各自的 `View` 改调 `field.View` / `ViewMasked`）
- Modify: `internal/ui/uitest/keys.go`、`internal/ui/uitest/*_test.go`
- Modify: `internal/ui/app_test.go:212`、`internal/ui/app_test.go:348`（原来断言 `:█` / `/█`）
- Test: `internal/ui/overlay/input_test.go`（新建）

**Interfaces:**
- Produces（Task 2 使用）：
  - `func newField(s string) field`：光标在末尾
  - `func (f *field) handle(msg tea.KeyPressMsg)`
  - `func (f *field) paste(s string)`：插入到光标处
  - `func (f *field) String() string`、`func (f *field) Len() int`
  - `func (f *field) Pos() int`：光标的 rune 下标
  - `func (f *field) replace(start int, text string)`：把 `[start, pos)` 的 rune 换成 text，光标移到 text 之后
  - `func (f *field) View(width int) string`、`func (f *field) ViewMasked(width int, mask rune) string`

- [ ] **Step 1: 测试用的按键补齐**

在 `internal/ui/uitest/keys.go` 里，给 `special` 加上：

```go
	"home":      {Code: tea.KeyHome},
	"end":       {Code: tea.KeyEnd},
	"delete":    {Code: tea.KeyDelete},
	"alt+left":  {Code: tea.KeyLeft, Mod: tea.ModAlt},
	"alt+right": {Code: tea.KeyRight, Mod: tea.ModAlt},
```

并在 `Key` 里 `ctrl+` 分支后面加一个 `alt+` 分支：

```go
	if rest, ok := strings.CutPrefix(s, "alt+"); ok {
		r, _ := utf8.DecodeRuneInString(rest)
		return tea.KeyPressMsg{Code: r, Mod: tea.ModAlt}
	}
```

在 `TestKeyStringRoundTrip` 的列表末尾加上 `"home", "end", "delete", "alt+left", "alt+right", "alt+b", "ctrl+w"`。

Run: `go test ./internal/ui/uitest/`
Expected: PASS

- [ ] **Step 2: 写 field 的失败测试**

新建 `internal/ui/overlay/input_test.go`：

```go
package overlay

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/ui/uitest"
)

func keysInto(f *field, keys ...string) {
	for _, k := range keys {
		f.handle(uitest.Key(k))
	}
}

// show is the field's text with the cursor drawn as |.
func show(f *field) string {
	r := []rune(f.String())
	return string(r[:f.Pos()]) + "|" + string(r[f.Pos():])
}

func TestFieldCursorMoves(t *testing.T) {
	f := newField("select abc_1 from t")
	for _, c := range []struct {
		key, want string
	}{
		{"home", "|select abc_1 from t"},
		{"right", "s|elect abc_1 from t"},
		{"ctrl+f", "se|lect abc_1 from t"},
		{"left", "s|elect abc_1 from t"},
		{"ctrl+b", "|select abc_1 from t"},
		{"left", "|select abc_1 from t"}, // stops at the start
		{"alt+right", "select |abc_1 from t"},
		{"alt+f", "select abc_1 |from t"},
		{"alt+left", "select |abc_1 from t"},
		{"alt+b", "|select abc_1 from t"},
		{"end", "select abc_1 from t|"},
		{"right", "select abc_1 from t|"}, // stops at the end
		{"ctrl+a", "|select abc_1 from t"},
		{"ctrl+e", "select abc_1 from t|"},
	} {
		keysInto(&f, c.key)
		if got := show(&f); got != c.want {
			t.Fatalf("after %s: %q, want %q", c.key, got, c.want)
		}
	}
}

func TestFieldEditsAtCursor(t *testing.T) {
	f := newField("ac")
	keysInto(&f, "left", "b")
	if got := show(&f); got != "ab|c" {
		t.Fatalf("insert: %q", got)
	}
	keysInto(&f, "backspace")
	if got := show(&f); got != "a|c" {
		t.Fatalf("backspace: %q", got)
	}
	keysInto(&f, "delete")
	if got := show(&f); got != "a|" {
		t.Fatalf("delete: %q", got)
	}
	keysInto(&f, "delete", "home", "backspace")
	if got := show(&f); got != "|a" {
		t.Fatalf("no-ops at the ends: %q", got)
	}

	f = newField("name = 'x' and")
	keysInto(&f, "ctrl+w")
	if got := show(&f); got != "name = 'x' |" {
		t.Fatalf("ctrl+w: %q", got)
	}
	keysInto(&f, "ctrl+w")
	if got := show(&f); got != "name = '|" {
		t.Fatalf("ctrl+w skips the punctuation before the word: %q", got)
	}

	f = newField("abcdef")
	keysInto(&f, "left", "left", "ctrl+k")
	if got := show(&f); got != "abcd|" {
		t.Fatalf("ctrl+k: %q", got)
	}
	keysInto(&f, "left", "ctrl+u")
	if got := show(&f); got != "|d" {
		t.Fatalf("ctrl+u: %q", got)
	}
}

func TestFieldPasteAndReplace(t *testing.T) {
	f := newField("ad")
	keysInto(&f, "left")
	f.paste("b\nc")
	if got := show(&f); got != "abc|d" {
		t.Fatalf("paste inserts at the cursor without control runes: %q", got)
	}
	f = newField("where na")
	f.replace(6, "name")
	if got := show(&f); got != "where name|" {
		t.Fatalf("replace: %q", got)
	}
	f = newField("中文")
	keysInto(&f, "left", "x")
	if got := show(&f); got != "中x|文" {
		t.Fatalf("runes, not bytes: %q", got)
	}
}

func TestFieldIgnoresAltText(t *testing.T) {
	f := newField("a")
	f.handle(tea.KeyPressMsg{Code: 'b', Mod: tea.ModAlt, Text: "b"})
	f.handle(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl, Text: "x"})
	if f.String() != "a" {
		t.Fatalf("alt / ctrl keys are not typed: %q", f.String())
	}
}

func TestFieldView(t *testing.T) {
	f := newField("abcdefghij")
	if got := ansi.Strip(f.View(20)); got != "abcdefghij " {
		t.Fatalf("fits: %q", got)
	}
	if got := ansi.Strip(f.View(5)); got != "…hij " {
		t.Fatalf("end: %q", got)
	}
	keysInto(&f, "home")
	if got := ansi.Strip(f.View(5)); got != "abcd…" {
		t.Fatalf("start: %q", got)
	}
	for i := 0; i < 5; i++ {
		keysInto(&f, "right")
	}
	got := ansi.Strip(f.View(6))
	if !strings.Contains(got, "f") || ansi.StringWidth(got) > 6 {
		t.Fatalf("middle keeps the cursor's rune in view: %q", got)
	}
}

func TestFieldViewWideRunes(t *testing.T) {
	f := newField("中文输入法测试")
	for _, w := range []int{4, 6, 9} { // 4 is the least that fits "…试" and the cursor
		got := ansi.Strip(f.View(w))
		if ansi.StringWidth(got) > w || !strings.Contains(got, "试") {
			t.Fatalf("width %d: %q", w, got)
		}
	}
}

func TestFieldViewMasked(t *testing.T) {
	f := newField("hunter2")
	for i := 0; i <= f.Len(); i++ {
		v := ansi.Strip(f.ViewMasked(40, '•'))
		if strings.ContainsAny(v, "hunter2") {
			t.Fatalf("cursor at %d leaks the secret: %q", f.Pos(), v)
		}
		if strings.Count(v, "•") != 7 {
			t.Fatalf("one bullet per rune: %q", v)
		}
		keysInto(&f, "left")
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/ui/overlay/ -run 'TestField'`
Expected: 编译失败（`f.Pos undefined`、`f.replace undefined`、`f.View undefined`）

- [ ] **Step 4: 重写 `internal/ui/overlay/input.go`**

```go
package overlay

import (
	"slices"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// field is a single-line text input with a cursor, edited with readline
// keys. Positions are rune indexes.
type field struct {
	r   []rune
	pos int // 0..len(r)
}

func newField(s string) field {
	r := []rune(s)
	return field{r: r, pos: len(r)}
}

func (f *field) handle(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "left", "ctrl+b":
		f.pos = max(f.pos-1, 0)
	case "right", "ctrl+f":
		f.pos = min(f.pos+1, len(f.r))
	case "home", "ctrl+a":
		f.pos = 0
	case "end", "ctrl+e":
		f.pos = len(f.r)
	case "alt+left", "alt+b":
		f.pos = f.wordLeft()
	case "alt+right", "alt+f":
		f.pos = f.wordRight()
	case "backspace":
		if f.pos > 0 {
			f.cut(f.pos-1, f.pos)
		}
	case "delete":
		if f.pos < len(f.r) {
			f.cut(f.pos, f.pos+1)
		}
	case "ctrl+w":
		f.cut(f.wordLeft(), f.pos)
	case "ctrl+u":
		f.cut(0, f.pos)
	case "ctrl+k":
		f.cut(f.pos, len(f.r))
	default:
		if msg.Text != "" && !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModAlt) {
			f.insert([]rune(msg.Text))
		}
	}
}

// paste inserts a terminal paste at the cursor. The field is single-line,
// so newlines and other control characters are dropped (password managers
// often copy a trailing newline).
func (f *field) paste(s string) {
	var rs []rune
	for _, r := range s {
		if !unicode.IsControl(r) {
			rs = append(rs, r)
		}
	}
	f.insert(rs)
}

// replace swaps the runes from start up to the cursor for text.
func (f *field) replace(start int, text string) {
	f.cut(min(max(start, 0), f.pos), f.pos)
	f.insert([]rune(text))
}

func (f *field) String() string { return string(f.r) }
func (f *field) Len() int       { return len(f.r) }
func (f *field) Pos() int       { return f.pos }

func (f *field) insert(rs []rune) {
	f.r = slices.Insert(f.r, f.pos, rs...)
	f.pos += len(rs)
}

func (f *field) cut(from, to int) {
	f.r = slices.Delete(f.r, from, to)
	f.pos = from
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// wordLeft is where the word before the cursor starts, past any non-word
// runes in between.
func (f *field) wordLeft() int {
	i := f.pos
	for i > 0 && !isWordRune(f.r[i-1]) {
		i--
	}
	for i > 0 && isWordRune(f.r[i-1]) {
		i--
	}
	return i
}

// wordRight is where the next word after the cursor starts.
func (f *field) wordRight() int {
	i := f.pos
	for i < len(f.r) && isWordRune(f.r[i]) {
		i++
	}
	for i < len(f.r) && !isWordRune(f.r[i]) {
		i++
	}
	return i
}

// View draws the text with the cursor as a reversed cell, scrolled so the
// cursor stays within width columns; … marks text cut off at either side.
func (f *field) View(width int) string { return f.view(width, 0) }

// ViewMasked is View with every rune drawn as mask.
func (f *field) ViewMasked(width int, mask rune) string { return f.view(width, mask) }

func (f *field) view(width int, mask rune) string {
	width = max(width, 1)
	n := len(f.r)
	cells := make([]string, n+1) // the last cell is the cursor past the end
	ws := make([]int, n+1)
	for i, r := range f.r {
		if mask != 0 {
			r = mask
		}
		cells[i] = textutil.Sanitize(string(r))
		ws[i] = ansi.StringWidth(cells[i])
	}
	cells[n], ws[n] = " ", 1

	// The window [from, to) holds the cursor; a little of the text after it
	// stays in view, then the text before it, then the rest after it.
	from, to, used := f.pos, f.pos+1, ws[f.pos]
	grow := func(limit int) {
		for to <= n && used+ws[to] <= limit {
			used += ws[to]
			to++
		}
	}
	grow(min(width, used+width/4))
	for from > 0 && used+ws[from-1] <= width {
		from--
		used += ws[from]
	}
	grow(width)

	// Make room for a … at each cut-off side, never dropping the cursor.
	left := from > 0
	if left {
		for used+1 > width && from < f.pos {
			used -= ws[from]
			from++
		}
		if left = used+1 <= width; left {
			used++
		}
	}
	right := to <= n
	if right {
		for used+1 > width && to-1 > f.pos {
			to--
			used -= ws[to]
		}
		right = used+1 <= width
	}

	out := slices.Clone(cells[from:to])
	c := f.pos - from
	out[c] = theme.Selected.Render(out[c])
	s := strings.Join(out, "")
	if left {
		s = "…" + s
	}
	if right {
		s += "…"
	}
	return s
}
```

- [ ] **Step 5: 跑 field 测试**

Run: `go test ./internal/ui/overlay/ -run 'TestField'`
Expected: PASS

- [ ] **Step 6: 所有使用方改调 `View` / `ViewMasked`**

`prompt.go` 的 `View`：删掉 `value := ...` 和截断的 `if`，输入行改为

```go
		"> " + p.input.View(w-4),
```

（`ansi` 的 import 如果不再使用就删掉。）

`cmdline.go`：

```go
// View renders the single status-bar line.
func (c *CmdLine) View(width, _ int) string { return c.prefix + c.input.View(max(width-1, 1)) }
```

`confirm.go`：`"> "+c.input.String()+"█"` 改为 `"> "+c.input.View(w-4)`。

`password.go`：`"> " + strings.Repeat("•", p.input.Len()) + "█"` 改为 `"> " + p.input.ViewMasked(w-4, '•')`。`View` 开头加 `w := max(min(width-4, 50), 10)`，`theme.Box(...)` 的宽度参数改成 `w`。

`secret.go`：`"> "+strings.Repeat("•", s.input.Len())+"█"` 改为 `"> "+s.input.ViewMasked(w-4, '•')`。

`connform.go` 的 `View` 循环改为（当前字段用光标视图，其它字段保持原样）：

```go
	for i, fd := range f.fields {
		var v string
		label := fmt.Sprintf("%-14s", fd.label)
		switch {
		case fd.choices != nil:
			v = "‹ " + fd.choices[fd.choice] + " ›"
		case i == f.cur && fd.secret:
			v = fd.input.ViewMasked(w-16, '•')
		case i == f.cur:
			v = fd.input.View(w - 16)
		case fd.secret:
			v = strings.Repeat("•", fd.input.Len())
		default:
			v = textutil.Sanitize(fd.input.String())
		}
		if i == f.cur {
			label = theme.ActiveTab.Render(label)
		}
		lines = append(lines, label+v)
	}
```

清理不再使用的 `strings` import（`password.go`、`secret.go` 里如果别处不用就删）。

- [ ] **Step 7: 改原来断言 `█` 的两个测试**

`internal/ui/app_test.go` 的 `TestCommandLine`：

```go
	press(t, m, ":")
	if lines := strings.Split(screen(m), "\n"); m.cmdline == nil || !strings.HasPrefix(lines[len(lines)-1], ":") {
		t.Fatal("command line shown in the status bar")
	}
```

`TestSearchTreeAndGrid` 同理，把 `":"` 换成 `"/"`。

- [ ] **Step 8: 全量验证**

Run: `gofmt -l . && go vet ./... && go test ./...`
Expected: 无格式问题，全部 PASS（`TestPrompt` 的长路径断言 `end.csv` 仍然成立：光标在末尾时窗口靠右）

- [ ] **Step 9: Commit**

```bash
git add internal/ui/overlay internal/ui/uitest internal/ui/app_test.go
git commit -m "feat(ui): cursor editing in single-line inputs

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Prompt 支持补全和校验

**Files:**
- Modify: `internal/ui/overlay/prompt.go`
- Test: `internal/ui/overlay/prompt_test.go`（新建）

**Interfaces:**
- Consumes（Task 1）：`field.Pos()`、`field.replace(start, text)`、`field.View(width)`
- Produces（Task 5 使用）：
  - `type Completer func(text string, cursor int) (start int, cands []complete.Candidate)`：cursor 和 start 都是 rune 下标；cands 已经排好序
  - `type PromptOption func(*Prompt)`
  - `func WithCompleter(c Completer) PromptOption`
  - `func WithValidate(v func(string) error) PromptOption`
  - `func NewPrompt(title, label, initial string, submit func(string) tea.Msg, opts ...PromptOption) *Prompt`（原有调用不用改）

- [ ] **Step 1: 写失败测试**

新建 `internal/ui/overlay/prompt_test.go`：

```go
package overlay

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/ui/panes/complete"
)

// columns completes the word before the cursor from a fixed list.
func columns(text string, cursor int) (int, []complete.Candidate) {
	rs := []rune(text)
	start := cursor
	for start > 0 && rs[start-1] != ' ' {
		start--
	}
	var out []complete.Candidate
	for _, c := range []string{"name", "nickname", "id"} {
		if strings.HasPrefix(c, string(rs[start:cursor])) {
			out = append(out, complete.Candidate{Text: c, Label: c, Kind: complete.Column})
		}
	}
	return start, out
}

func textMsg(s string) tea.Msg { return CommandMsg{Text: s} }

func TestPromptCompletesASingleCandidate(t *testing.T) {
	p := NewPrompt("Filter", "WHERE", "x = 1 and i", textMsg, WithCompleter(columns))
	if o, msg := send(t, p, "tab", "enter"); o != nil || msg != (CommandMsg{Text: "x = 1 and id"}) {
		t.Fatalf("o=%v msg=%#v", o, msg)
	}
}

func TestPromptCompletionPopup(t *testing.T) {
	p := NewPrompt("Filter", "WHERE", "n", textMsg, WithCompleter(columns))
	send(t, p, "tab")
	if v := ansi.Strip(p.View(80, 24)); !strings.Contains(v, "nickname") || !strings.Contains(v, "name") {
		t.Fatalf("popup lists the candidates:\n%s", v)
	}
	o, msg := send(t, p, "tab", "enter")
	if o == nil || msg != nil || p.input.String() != "nickname" || p.popup != nil {
		t.Fatalf("tab moves on, enter accepts and keeps the prompt open: %q", p.input.String())
	}
	if _, msg := send(t, p, "enter"); msg != (CommandMsg{Text: "nickname"}) {
		t.Fatalf("msg=%#v", msg)
	}
}

func TestPromptPopupKeys(t *testing.T) {
	p := NewPrompt("Filter", "WHERE", "n", textMsg, WithCompleter(columns))
	if o, _ := send(t, p, "tab", "esc"); o == nil || p.popup != nil {
		t.Fatal("esc closes only the popup")
	}
	send(t, p, "tab", "a")
	if p.popup != nil || p.input.String() != "na" {
		t.Fatalf("typing closes the popup and types: %q", p.input.String())
	}
	send(t, p, "backspace", "tab", "down", "up", "enter")
	if p.input.String() != "name" {
		t.Fatalf("up / down move in the popup: %q", p.input.String())
	}
	send(t, p, "ctrl+u", "z", "tab")
	if p.popup != nil || p.input.String() != "z" {
		t.Fatal("no candidates: nothing happens")
	}
}

func TestPromptWithoutCompleterIgnoresTab(t *testing.T) {
	p := NewPrompt("Export", "File:", "a", textMsg)
	if _, msg := send(t, p, "tab", "enter"); msg != (CommandMsg{Text: "a"}) {
		t.Fatalf("msg=%#v", msg)
	}
}

func TestPromptValidate(t *testing.T) {
	check := func(s string) error {
		if strings.Contains(s, ";") {
			return errors.New("one condition only")
		}
		return nil
	}
	p := NewPrompt("Filter", "WHERE", "a; b", textMsg, WithValidate(check))
	o, msg := send(t, p, "enter")
	if o == nil || msg != nil || !strings.Contains(ansi.Strip(p.View(80, 24)), "one condition only") {
		t.Fatalf("an invalid value keeps the prompt open with the error:\n%s", ansi.Strip(p.View(80, 24)))
	}
	send(t, p, "ctrl+u")
	if strings.Contains(ansi.Strip(p.View(80, 24)), "one condition only") {
		t.Fatal("editing clears the error")
	}
	if _, msg := send(t, p, "a", "enter"); msg != (CommandMsg{Text: "a"}) {
		t.Fatalf("msg=%#v", msg)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/ui/overlay/ -run 'TestPrompt'`
Expected: 编译失败（`WithCompleter undefined`、`p.popup undefined`）

- [ ] **Step 3: 实现 `internal/ui/overlay/prompt.go`**

整个文件替换为：

```go
package overlay

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/shepherd-yun/datavim/internal/ui/panes/complete"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// Completer offers completions for the word before cursor in text; start
// is where that word begins. Both are rune indexes.
type Completer func(text string, cursor int) (start int, cands []complete.Candidate)

// PromptOption configures a Prompt.
type PromptOption func(*Prompt)

// WithCompleter makes tab complete the word before the cursor.
func WithCompleter(c Completer) PromptOption { return func(p *Prompt) { p.complete = c } }

// WithValidate checks the text on enter; an error keeps the prompt open.
func WithValidate(v func(string) error) PromptOption { return func(p *Prompt) { p.validate = v } }

// Prompt asks for one line of text with an editable initial value.
type Prompt struct {
	title, label string
	input        field
	submit       func(string) tea.Msg
	complete     Completer
	validate     func(string) error
	popup        *complete.Model
	popStart     int    // where the completed word starts
	err          string // the last validation error, until the next edit
}

// NewPrompt builds a prompt; submit turns the entered text into a message.
func NewPrompt(title, label, initial string, submit func(string) tea.Msg, opts ...PromptOption) *Prompt {
	p := &Prompt{title: title, label: label, input: newField(initial), submit: submit}
	for _, o := range opts {
		o(p)
	}
	return p
}

func (p *Prompt) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	key := msg.String()
	if p.popup != nil {
		switch key {
		case "tab", "down", "ctrl+n":
			p.popup.Next()
			return p, nil
		case "shift+tab", "up", "ctrl+p":
			p.popup.Prev()
			return p, nil
		case "enter":
			p.input.replace(p.popStart, p.popup.Chosen().Text)
			p.popup = nil
			return p, nil
		case "esc":
			p.popup = nil
			return p, nil
		}
		p.popup = nil
	}
	switch key {
	case "esc":
		return nil, nil
	case "enter":
		text := p.input.String()
		if p.validate != nil {
			if err := p.validate(text); err != nil {
				p.err = err.Error()
				return p, nil
			}
		}
		return nil, emit(p.submit(text))
	case "tab":
		if p.complete != nil {
			p.suggest()
			return p, nil
		}
	}
	p.err = ""
	p.input.handle(msg)
	return p, nil
}

// suggest completes a lone candidate at once, or opens the popup.
func (p *Prompt) suggest() {
	start, cands := p.complete(p.input.String(), p.input.Pos())
	switch len(cands) {
	case 0:
	case 1:
		p.input.replace(start, cands[0].Text)
	default:
		p.popup, p.popStart = complete.New(cands), start
	}
}

func (p *Prompt) View(width, height int) string {
	w := max(min(width-4, 80), 20)
	lines := []string{p.label, "> " + p.input.View(w-4)}
	if p.err != "" {
		for _, l := range textutil.Wrap(textutil.Sanitize(p.err), w-2) {
			lines = append(lines, theme.Error.Render(l))
		}
	}
	footer := "enter ok · esc cancel"
	if p.complete != nil {
		footer = "tab complete · " + footer
	}
	lines = append(lines, "", theme.Muted.Render(footer))
	box := theme.Box(p.title, strings.Join(lines, "\n"), w, len(lines)+2, true)
	if p.popup == nil {
		return box
	}
	// The popup hangs below the box, indented past its border; its lines
	// are not padded, so the screen beside it stays visible.
	pop := p.popup.View(min(w-2, 60), max(height-lipgloss.Height(box), 3))
	popLines := strings.Split(pop, "\n")
	for i, l := range popLines {
		popLines[i] = "  " + l
	}
	return box + "\n" + strings.Join(popLines, "\n")
}

// Paste inserts text at the cursor.
func (p *Prompt) Paste(text string) {
	p.popup, p.err = nil, ""
	p.input.paste(text)
}
```

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/ui/overlay/`
Expected: PASS（包括原有的 `TestPrompt` 和 `paste_test.go`）

- [ ] **Step 5: 全量验证并提交**

Run: `gofmt -l . && go vet ./... && go test ./...`
Expected: 全部 PASS

```bash
git add internal/ui/overlay/prompt.go internal/ui/overlay/prompt_test.go
git commit -m "feat(ui): prompts can complete with tab and validate before closing

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: 过滤条件的生成函数

**Files:**
- Modify: `internal/ui/tablequery.go`
- Modify: `internal/ui/tabs.go`（唯一调用 `valueFilter` 的地方）
- Test: `internal/ui/tablequery_test.go`

**Interfaces:**
- Produces（Task 4 使用）：
  - `func compareFilter(d sqlx.Dialect, col db.ColumnInfo, op string, v db.Value) string`：op 是 `=` `<>` `>` `<`；NULL 值时 `=` → `IS NULL`，`<>` → `IS NOT NULL`
  - `func nullFilter(d sqlx.Dialect, col string, not bool) string`
  - `func containsFilter(d sqlx.Dialect, col, s string) string`
- 删除：`valueFilter`（被 `compareFilter(d, col, "=", v)` 取代）

- [ ] **Step 1: 改写测试**

在 `internal/ui/tablequery_test.go` 中，把 `TestValueFilter` 里的两处 `valueFilter(` 改为 `compareFilter(` 并在第二个参数后插入 `"="`：

```go
	if got := compareFilter(sqlx.MySQL, name, "=", db.Value{Text: "it's"}); got != "`name` = 'it''s'" {
	...
	if got := compareFilter(sqlx.Postgres, name, "=", db.Value{Null: true}); got != `"name" IS NULL` {
```

并在文件末尾追加：

```go
func TestFilterConditions(t *testing.T) {
	name := db.ColumnInfo{Name: "name", DatabaseType: "VARCHAR"}
	age := db.ColumnInfo{Name: "age", DatabaseType: "INT"}
	for i, c := range []struct{ got, want string }{
		{compareFilter(sqlx.MySQL, name, "<>", db.Value{Text: "bob"}), "`name` <> 'bob'"},
		{compareFilter(sqlx.Postgres, name, "<>", db.Value{Null: true}), `"name" IS NOT NULL`},
		{compareFilter(sqlx.Postgres, age, ">", db.Value{Text: "18"}), `"age" > 18`},
		{compareFilter(sqlx.MySQL, age, "<", db.Value{Text: "x"}), "`age` < 'x'"},
		{nullFilter(sqlx.MySQL, "name", false), "`name` IS NULL"},
		{nullFilter(sqlx.Postgres, "name", true), `"name" IS NOT NULL`},
		{containsFilter(sqlx.MySQL, "name", "bo"), "`name` LIKE '%bo%'"},
		{containsFilter(sqlx.Postgres, "age", "1"), `"age"::text ILIKE '%1%'`},
		// % _ \ are LIKE wildcards / the escape: they match themselves; MySQL also doubles \ in the literal
		{containsFilter(sqlx.MySQL, "name", `50%_a\b'c`), "`name` LIKE '%50\\\\%\\\\_a\\\\\\\\b''c%'"},
		{containsFilter(sqlx.Postgres, "name", `50%_a\b'c`), `"name"::text ILIKE '%50\%\_a\\b''c%'`},
	} {
		if c.got != c.want {
			t.Errorf("%d: got %s, want %s", i, c.got, c.want)
		}
	}
}

func TestContainsFilterIsOneCondition(t *testing.T) {
	ref := db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "users"}
	for _, d := range []sqlx.Dialect{sqlx.MySQL, sqlx.Postgres} {
		q := tableQuery{where: containsFilter(d, "name", `x'; DROP TABLE users; --\`)}
		if err := q.check(d, ref); err != nil {
			t.Errorf("%v: %v (%s)", d, err, q.where)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/ui/ -run 'TestValueFilter|TestFilterConditions|TestContainsFilter'`
Expected: 编译失败（`compareFilter undefined`）

- [ ] **Step 3: 实现**

在 `internal/ui/tablequery.go` 中，把 `valueFilter` 整个替换为：

```go
// compareFilter is col op v, op being = <> > or <; a NULL v turns = and <>
// into IS NULL and IS NOT NULL.
func compareFilter(d sqlx.Dialect, col db.ColumnInfo, op string, v db.Value) string {
	if v.Null {
		return nullFilter(d, col.Name, op == "<>")
	}
	return sqlx.QuoteIdent(d, col.Name) + " " + op + " " + sqlx.Literal(d, col.DatabaseType, v)
}

// nullFilter is col IS NULL, or IS NOT NULL with not.
func nullFilter(d sqlx.Dialect, col string, not bool) string {
	if not {
		return sqlx.QuoteIdent(d, col) + " IS NOT NULL"
	}
	return sqlx.QuoteIdent(d, col) + " IS NULL"
}

// likeEscaper makes % _ and \ match themselves in a LIKE pattern (\ is the
// default escape on MySQL and PostgreSQL).
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// containsFilter matches rows whose col contains s, ignoring case: ILIKE on
// PostgreSQL, with ::text so numbers, dates and uuids match too, and LIKE
// on MySQL, whose default collations ignore case.
func containsFilter(d sqlx.Dialect, col, s string) string {
	pattern := sqlx.Literal(d, "TEXT", db.Value{Text: "%" + likeEscaper.Replace(s) + "%"})
	if d == sqlx.Postgres {
		return sqlx.QuoteIdent(d, col) + "::text ILIKE " + pattern
	}
	return sqlx.QuoteIdent(d, col) + " LIKE " + pattern
}
```

在 `internal/ui/tabs.go` 的 `case keymap.FilterValue:` 分支里，把

```go
		cond := valueFilter(t.dialect, col, v)
```

改为

```go
		cond := compareFilter(t.dialect, col, "=", v)
```

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/ui/ -run 'TestValueFilter|TestFilterConditions|TestContainsFilter|TestDataTabFilter'`
Expected: PASS

- [ ] **Step 5: 全量验证并提交**

Run: `gofmt -l . && go vet ./... && go test ./...`

```bash
git add internal/ui/tablequery.go internal/ui/tablequery_test.go internal/ui/tabs.go
git commit -m "feat(ui): build not-equal, comparison, null and contains filters

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: `f` 快捷过滤菜单

**Files:**
- Modify: `internal/ui/keymap/keymap.go`（动作常量）
- Modify: `internal/ui/keymap/defaults.go`（绑定）
- Modify: `internal/ui/tabs.go`（`dataTab.Handle`、`dataTab.Update`、新消息类型和辅助方法）
- Test: `internal/ui/keymap/keymap_test.go`、`internal/ui/tabs_test.go`、`internal/ui/datatab_edit_test.go`、`internal/ui/filter_test.go`（新建）

**Interfaces:**
- Consumes（Task 3）：`compareFilter`、`nullFilter`、`containsFilter`；已有的 `andFilter`、`applyQuery`
- Produces：
  - keymap 动作 `FilterEqual`（`"filter.eq"`）、`FilterNotEqual`（`"filter.ne"`）、`FilterContains`（`"filter.contains"`）、`FilterGreater`（`"filter.gt"`）、`FilterLess`（`"filter.lt"`）、`FilterNull`（`"filter.null"`）、`FilterNotNull`（`"filter.notnull"`）、`FilterClear`（`"filter.clear"`）；删除 `FilterValue`
  - `type filterValueMsg struct { tabID int; action keymap.Action; col db.ColumnInfo; text string }`
  - `func (t *dataTab) addFilter(cond string) tea.Cmd`

- [ ] **Step 1: 改 keymap 测试**

`internal/ui/keymap/keymap_test.go` 的 `TestFilterAndSortBindings` 替换为：

```go
func TestFilterAndSortBindings(t *testing.T) {
	k := Default()
	for keys, want := range map[string]Action{
		"f f": FilterEqual, "f !": FilterNotEqual, "f s": FilterContains, "f ~": FilterContains,
		"f >": FilterGreater, "f <": FilterLess, "f n": FilterNull, "f N": FilterNotNull,
		"f x": FilterClear, "F": FilterPrompt, "s": SortColumn,
	} {
		seq := strings.Fields(keys)
		if a, res := k.Resolve(Grid, seq); a != want || res != Matched {
			t.Errorf("grid %s: %q %v", keys, a, res)
		}
		if _, res := k.Resolve(Results, seq); res == Matched {
			t.Errorf("console results are not filtered: %s", keys)
		}
	}
	if _, res := k.Resolve(Grid, []string{"f"}); res != Pending {
		t.Errorf("f is a prefix: %v", res)
	}
}
```

（如果 `keymap_test.go` 还没有 import `strings`，加上。）

- [ ] **Step 2: 写 data tab 的失败测试**

新建 `internal/ui/filter_test.go`：

```go
package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
)

const usersPage = " ORDER BY `id` LIMIT 3 OFFSET 0"

func TestFilterMenuInstantConditions(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	tab.Handle(keymap.Right) // name of user1
	for _, c := range []struct {
		action keymap.Action
		where  string
	}{
		{keymap.FilterNotEqual, "`name` <> 'user1'"},
		{keymap.FilterNotNull, "(`name` <> 'user1') AND `name` IS NOT NULL"},
	} {
		tab.Update(run(tab.Handle(c.action)))
		if got, want := b.conn.lastQuery(), "SELECT * FROM `app`.`users` WHERE ("+c.where+"\n)"+usersPage; got != want {
			t.Fatalf("%s: query = %q, want %q", c.action, got, want)
		}
	}
	tab.Update(run(tab.Handle(keymap.FilterClear)))
	if got := b.conn.lastQuery(); got != "SELECT * FROM `app`.`users`"+usersPage {
		t.Fatalf("fx clears the filter: %q", got)
	}
	if tab.Handle(keymap.FilterClear) != nil {
		t.Fatal("fx without a filter does not reload")
	}
	tab.Update(run(tab.Handle(keymap.FilterNull)))
	if got := b.conn.lastQuery(); !strings.Contains(got, "WHERE (`name` IS NULL\n)") {
		t.Fatalf("fn: %q", got)
	}
}

// promptFor runs action and returns the value prompt it opens.
func promptFor(t *testing.T, tab *dataTab, action keymap.Action) *overlay.Prompt {
	t.Helper()
	msg, ok := run(tab.Handle(action)).(openOverlayMsg)
	p, isPrompt := msg.overlay.(*overlay.Prompt)
	if !ok || !isPrompt {
		t.Fatalf("%s opens a prompt: %#v", action, msg)
	}
	return p
}

func TestFilterMenuContains(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	tab.Handle(keymap.Right)
	p := promptFor(t, tab, keymap.FilterContains)
	if v := ansi.Strip(p.View(80, 24)); !strings.Contains(v, "name contains") || !strings.Contains(v, "user1") {
		t.Fatalf("the prompt names the column and starts from the cell:\n%s", v)
	}
	tab.Update(run(tab.Update(filterValueMsg{tabID: 1, action: keymap.FilterContains, col: colNamed("name"), text: `50%_'`})))
	if got := b.conn.lastQuery(); !strings.Contains(got, "WHERE (`name` LIKE '%50\\\\%\\\\_''%'\n)") {
		t.Fatalf("contains escapes the value: %q", got)
	}
}

func TestFilterMenuComparisons(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	promptFor(t, tab, keymap.FilterGreater) // cursor on id
	tab.Update(run(tab.Update(filterValueMsg{tabID: 1, action: keymap.FilterGreater, col: colNamed("id"), text: "1"})))
	tab.Update(run(tab.Update(filterValueMsg{tabID: 1, action: keymap.FilterLess, col: colNamed("id"), text: "9"})))
	if got := b.conn.lastQuery(); !strings.Contains(got, "WHERE ((`id` > '1') AND `id` < '9'\n)") {
		t.Fatalf("comparisons add up: %q", got)
	}
	before := b.conn.lastQuery()
	if tab.Update(filterValueMsg{tabID: 1, action: keymap.FilterLess, col: colNamed("id"), text: ""}) != nil || b.conn.lastQuery() != before {
		t.Fatal("an empty value cancels")
	}
}

// colNamed is the column called name as the fake reports it (no type, so
// values are quoted).
func colNamed(name string) db.ColumnInfo { return db.ColumnInfo{Name: name} }

func TestFilterMenuNeedsACell(t *testing.T) {
	b := newFakeBackend()
	b.conn.rows = 0
	tab := loadedDataTab(t, b)
	before := b.conn.lastQuery()
	tab.Handle(keymap.FilterEqual)
	if b.conn.lastQuery() != before || !strings.Contains(ansi.Strip(tab.View(true)), "no cell to filter by") {
		t.Fatalf("ff on an empty page:\n%s", ansi.Strip(tab.View(true)))
	}
}

func TestFilterMenuPrefixCancels(t *testing.T) {
	m, b, _ := openUsersTab(t)
	before := b.conn.lastQuery()
	press(t, m, "f", "esc")
	press(t, m, "f", "q")
	if b.conn.lastQuery() != before || m.overlay != nil {
		t.Fatal("f then esc or an unbound key does nothing")
	}
	press(t, m, "l", "f", "f")
	if got := b.conn.lastQuery(); !strings.Contains(got, "WHERE (`name` = 'user1'\n)") {
		t.Fatalf("ff filters by the cell: %q", got)
	}
}

func TestBadFilterValueKeepsThePage(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	b.conn.setFail("SELECT * FROM `app`.`users` WHERE (`id` > 'x'\n)"+usersPage, errors.New("bad value"))
	tab.Update(run(tab.Update(filterValueMsg{tabID: 1, action: keymap.FilterGreater, col: colNamed("id"), text: "x"})))
	if v := ansi.Strip(tab.View(true)); !strings.Contains(v, "bad value") || !strings.Contains(v, "user1") {
		t.Fatalf("view:\n%s", v)
	}
}
```

在 `internal/ui/tabs_test.go` 的 `TestDataTabFilterByValue` 中，把两处 `keymap.FilterValue` 改为 `keymap.FilterEqual`。

在 `internal/ui/datatab_edit_test.go` 的 `TestUnsavedChangesBlockReloads` 中，把 `{"f"}` 改为 `{"f", "f"}, {"f", "s"}, {"f", "x"}`。

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/ui/... 2>&1 | head -20`
Expected: 编译失败（`FilterEqual undefined`、`filterValueMsg undefined`）

- [ ] **Step 4: keymap**

`internal/ui/keymap/keymap.go`：删除 `FilterValue      Action = "filter.value"` 一行，在同一位置加入：

```go
	FilterEqual      Action = "filter.eq"
	FilterNotEqual   Action = "filter.ne"
	FilterContains   Action = "filter.contains"
	FilterGreater    Action = "filter.gt"
	FilterLess       Action = "filter.lt"
	FilterNull       Action = "filter.null"
	FilterNotNull    Action = "filter.notnull"
	FilterClear      Action = "filter.clear"
```

`internal/ui/keymap/defaults.go`：把

```go
		Binding{Grid, keys("f"), FilterValue, "Filter by this value"},
```

替换为

```go
		Binding{Grid, keys("f", "f"), FilterEqual, "Filter: equals this value"},
		Binding{Grid, keys("f", "!"), FilterNotEqual, "Filter: not this value"},
		Binding{Grid, keys("f", "s"), FilterContains, "Filter: contains…"},
		Binding{Grid, keys("f", "~"), FilterContains, "Filter: contains…"},
		Binding{Grid, keys("f", ">"), FilterGreater, "Filter: greater than…"},
		Binding{Grid, keys("f", "<"), FilterLess, "Filter: less than…"},
		Binding{Grid, keys("f", "n"), FilterNull, "Filter: is NULL"},
		Binding{Grid, keys("f", "N"), FilterNotNull, "Filter: is not NULL"},
		Binding{Grid, keys("f", "x"), FilterClear, "Clear the filter"},
```

Run: `go test ./internal/ui/keymap/`
Expected: PASS（`TestDefaultIsValid` 确认没有前缀冲突）

- [ ] **Step 5: data tab 接线**

在 `internal/ui/tabs.go` 的 `filterMsg` 定义后面加：

```go
// filterValueMsg is a value typed for fs, f> or f<.
type filterValueMsg struct {
	tabID  int
	action keymap.Action
	col    db.ColumnInfo
	text   string
}

func (m filterValueMsg) TabID() int { return m.tabID }
```

`dataTab.Update` 的 switch 里，在 `case filterMsg:` 之后加：

```go
	case filterValueMsg:
		if m.text == "" {
			return nil
		}
		switch m.action {
		case keymap.FilterContains:
			return t.addFilter(containsFilter(t.dialect, m.col.Name, m.text))
		case keymap.FilterGreater:
			return t.addFilter(compareFilter(t.dialect, m.col, ">", db.Value{Text: m.text}))
		case keymap.FilterLess:
			return t.addFilter(compareFilter(t.dialect, m.col, "<", db.Value{Text: m.text}))
		}
```

`dataTab.Handle` 里拦截未提交修改的那一行改为：

```go
	case keymap.NextPage, keymap.PrevPage, keymap.Refresh, keymap.FilterPrompt, keymap.SortColumn,
		keymap.FilterEqual, keymap.FilterNotEqual, keymap.FilterContains, keymap.FilterGreater,
		keymap.FilterLess, keymap.FilterNull, keymap.FilterNotNull, keymap.FilterClear:
```

把原来的 `case keymap.FilterValue:` 整个分支替换为：

```go
	case keymap.FilterEqual, keymap.FilterNotEqual:
		col, v, ok := t.grid.Cell()
		if !ok {
			t.setStatus("no cell to filter by", true)
			return nil
		}
		op := "="
		if a == keymap.FilterNotEqual {
			op = "<>"
		}
		return t.addFilter(compareFilter(t.dialect, col, op, v))
	case keymap.FilterNull, keymap.FilterNotNull:
		col, ok := t.grid.Column()
		if !ok {
			t.setStatus("no column to filter by", true)
			return nil
		}
		return t.addFilter(nullFilter(t.dialect, col.Name, a == keymap.FilterNotNull))
	case keymap.FilterContains, keymap.FilterGreater, keymap.FilterLess:
		return t.filterValuePrompt(a)
	case keymap.FilterClear:
		if t.query.where == "" {
			return nil
		}
		return t.applyQuery(func(q *tableQuery) { q.where = "" })
```

在 `applyQuery` 后面加两个方法：

```go
// addFilter narrows the filter with cond.
func (t *dataTab) addFilter(cond string) tea.Cmd {
	return t.applyQuery(func(q *tableQuery) { q.where = andFilter(q.where, cond) })
}

var filterVerbs = map[keymap.Action]string{
	keymap.FilterContains: "contains",
	keymap.FilterGreater:  ">",
	keymap.FilterLess:     "<",
}

// filterValuePrompt asks for the value of fs, f> or f<, starting from the
// cell under the cursor.
func (t *dataTab) filterValuePrompt(a keymap.Action) tea.Cmd {
	col, ok := t.grid.Column()
	if !ok {
		t.setStatus("no column to filter by", true)
		return nil
	}
	initial := ""
	if _, v, ok := t.grid.Cell(); ok && !v.Null {
		initial = v.Text
	}
	id := t.id
	return openOverlay(overlay.NewPrompt("Filter "+t.ref.Name, col.Name+" "+filterVerbs[a]+" …", initial,
		func(s string) tea.Msg { return filterValueMsg{tabID: id, action: a, col: col, text: s} }))
}
```

- [ ] **Step 6: 跑测试**

Run: `go test ./internal/ui/...`
Expected: PASS（`b.conn.rows = 0` 让 fake 返回有列、无行的结果，见 `fakes_test.go` 的 `fakeConn.Query`）

- [ ] **Step 7: 全量验证并提交**

Run: `gofmt -l . && go vet ./... && go test ./...`

```bash
git add internal/ui/keymap internal/ui/tabs.go internal/ui/tabs_test.go internal/ui/datatab_edit_test.go internal/ui/filter_test.go
git commit -m "feat(ui): f opens a quick filter menu for the current column

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: `F` 的预填、补全、校验和失败回填

**Files:**
- Modify: `internal/ui/tabs.go`（`FilterPrompt` 分支、`filterMsg` 处理、`pageLoaded`、`dataTab` 字段）
- Test: `internal/ui/filter_test.go`

**Interfaces:**
- Consumes（Task 2）：`overlay.WithCompleter`、`overlay.WithValidate`、`overlay.Completer`；（已有）`complete.Rank`、`complete.Candidate`、`maxCandidates`（`completion.go`）、`sqlx.QuoteIfNeeded`、`tableQuery.check`
- Produces：`dataTab.failedWhere string`；`func normFilter(s string) string`；`func (t *dataTab) completeFilter(text string, cursor int) (int, []complete.Candidate)`

- [ ] **Step 1: 写失败测试**

在 `internal/ui/filter_test.go` 的 import 里加上 `"slices"`、`"github.com/shepherd-yun/datavim/internal/ui/panes/complete"`、`"github.com/shepherd-yun/datavim/internal/ui/uitest"`，然后在文件末尾追加：

```go
// filterPrompt presses F and returns the prompt.
func filterPrompt(t *testing.T, tab *dataTab) *overlay.Prompt {
	t.Helper()
	return promptFor(t, tab, keymap.FilterPrompt)
}

func promptText(t *testing.T, p *overlay.Prompt) string {
	t.Helper()
	_, cmd := p.HandleKey(uitest.Key("enter"))
	if cmd == nil {
		t.Fatal("enter did not submit")
	}
	m, ok := cmd().(filterMsg)
	if !ok {
		t.Fatalf("submit: %#v", cmd())
	}
	return m.text
}

func candTexts(cands []complete.Candidate) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.Text
	}
	return out
}

func TestFilterPromptStartsFromTheColumn(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	tab.Handle(keymap.Right)
	if got := promptText(t, filterPrompt(t, tab)); got != "name " {
		t.Fatalf("an empty filter starts with the column: %q", got)
	}
	tab.Update(run(tab.Update(filterMsg{tabID: 1, text: "id > 1"})))
	if got := promptText(t, filterPrompt(t, tab)); got != "id > 1" {
		t.Fatalf("an existing filter is kept: %q", got)
	}
}

func TestFilterPromptCompletes(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	start, cands := tab.completeFilter("id > 1 and na", 13)
	if start != 11 || len(cands) == 0 || cands[0].Text != "name" || cands[0].Detail != "text" {
		t.Fatalf("start=%d cands=%+v", start, cands)
	}
	_, cands = tab.completeFilter("name li", 7)
	if len(cands) == 0 || cands[0].Text != "like" {
		t.Fatalf("keywords follow the typed case: %+v", cands)
	}
	_, cands = tab.completeFilter("name IS N", 9)
	if texts := candTexts(cands); !slices.Contains(texts, "NULL") || !slices.Contains(texts, "NOT") {
		t.Fatalf("upper case: %v", texts)
	}
	if _, cands = tab.completeFilter("", 0); len(cands) < 2 {
		t.Fatalf("tab on nothing offers everything: %+v", cands)
	}
}

func TestFilterPromptChecksLocally(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	p := filterPrompt(t, tab)
	p.HandleKey(uitest.Key("ctrl+u"))
	for _, k := range uitest.Type("1=1; DELETE FROM users") {
		p.HandleKey(k)
	}
	o, cmd := p.HandleKey(uitest.Key("enter"))
	if o == nil || cmd != nil || !strings.Contains(ansi.Strip(p.View(80, 24)), "the filter must be a condition") {
		t.Fatalf("a bad filter keeps the prompt open:\n%s", ansi.Strip(p.View(80, 24)))
	}
}

func TestFilterPromptBringsBackAFailedFilter(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	b.conn.setFail("SELECT * FROM `app`.`users` WHERE (nmae = 'x'\n)"+usersPage, errors.New("unknown column nmae"))
	tab.Update(run(tab.Update(filterMsg{tabID: 1, text: "nmae = 'x'"})))
	if got := promptText(t, filterPrompt(t, tab)); got != "nmae = 'x'" {
		t.Fatalf("F brings back the failed filter: %q", got)
	}
	tab.Update(run(tab.Update(filterMsg{tabID: 1, text: "name = 'x'"})))
	tab.Update(run(tab.Update(filterMsg{tabID: 1, text: ""})))
	if got := promptText(t, filterPrompt(t, tab)); got != "id " {
		t.Fatalf("a later success forgets it: %q", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/ui/ -run 'TestFilterPrompt'`
Expected: 编译失败（`tab.completeFilter undefined`）

- [ ] **Step 3: 实现**

`dataTab` 结构体里 `wantColumn` 之后加字段：

```go
	failedWhere string // a filter the server rejected, offered again by F
```

`pageLoaded` 开头的出错分支改为：

```go
	if m.err != nil {
		t.err = m.err.Error()
		if m.query.where != t.query.where {
			t.failedWhere = m.query.where
		}
		return
	}
	t.failedWhere = ""
```

在 `filterMsg` 类型定义后面加：

```go
// normFilter trims a typed filter and its trailing semicolons.
func normFilter(s string) string {
	return strings.TrimSpace(strings.TrimRight(strings.TrimSpace(s), ";"))
}
```

`Update` 里的 `case filterMsg:` 改为：

```go
	case filterMsg:
		where := normFilter(m.text)
		return t.applyQuery(func(q *tableQuery) { q.where = where })
```

`Handle` 里的 `case keymap.FilterPrompt:` 分支改为 `return t.filterPrompt()`，并加以下方法（放在 `filterValuePrompt` 后面）：

```go
// filterPrompt edits the WHERE condition, starting from a filter the
// server just rejected, else the current one, else the cursor's column.
func (t *dataTab) filterPrompt() tea.Cmd {
	initial := t.query.where
	switch {
	case t.failedWhere != "":
		initial = t.failedWhere
	case initial == "":
		if col, ok := t.grid.Column(); ok {
			initial = sqlx.QuoteIfNeeded(col.Name, t.dialect) + " "
		}
	}
	id := t.id
	return openOverlay(overlay.NewPrompt("Filter "+t.ref.Name,
		"WHERE …  e.g. name LIKE '%abc%' AND age > 18 · empty shows every row", initial,
		func(s string) tea.Msg { return filterMsg{tabID: id, text: s} },
		overlay.WithCompleter(t.completeFilter),
		overlay.WithValidate(t.checkFilter)))
}

// checkFilter rejects a typed filter that is not a single condition.
func (t *dataTab) checkFilter(s string) error {
	q := t.query
	q.where = normFilter(s)
	return q.check(t.dialect, t.ref)
}

var filterKeywords = []string{"AND", "OR", "NOT", "LIKE", "IN", "IS", "NULL", "BETWEEN", "TRUE", "FALSE"}

func isFilterWordRune(r rune) bool {
	return r == '_' || r == '"' || r == '`' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// completeFilter offers the table's columns and condition keywords for the
// word before the cursor in the F prompt.
func (t *dataTab) completeFilter(text string, cursor int) (int, []complete.Candidate) {
	rs := []rune(text)
	cursor = min(max(cursor, 0), len(rs))
	start := cursor
	for start > 0 && isFilterWordRune(rs[start-1]) {
		start--
	}
	prefix := strings.Trim(string(rs[start:cursor]), "\"`")
	var cands []complete.Candidate
	for _, c := range t.cols {
		cands = append(cands, complete.Candidate{
			Text: sqlx.QuoteIfNeeded(c.Name, t.dialect), Label: c.Name, Kind: complete.Column, Detail: c.Type,
		})
	}
	kws := filterKeywords
	if t.dialect == sqlx.Postgres {
		kws = append(slices.Clone(kws), "ILIKE")
	}
	lower := strings.ToUpper(prefix) != prefix
	for _, kw := range kws {
		if lower {
			kw = strings.ToLower(kw)
		}
		cands = append(cands, complete.Candidate{Text: kw, Label: kw, Kind: complete.Keyword})
	}
	return start, complete.Rank(prefix, cands, maxCandidates)
}
```

在 `tabs.go` 的 import 中加入 `"slices"`、`"unicode"` 和 `"github.com/shepherd-yun/datavim/internal/ui/panes/complete"`。

注意 `t.cols` 是 meta 读到的 `[]db.Column`（带 `Type`）；fake 里 `name` 的类型是 `text`、`id` 是 `bigint`。

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/ui/ -run 'TestFilter|TestDataTab'`
Expected: PASS。原有的 `TestDataTabFilterPrompt` 仍然通过（它只检查打开的是 `*overlay.Prompt`，再直接发 `filterMsg`）。

- [ ] **Step 5: 全量验证并提交**

Run: `gofmt -l . && go vet ./... && go test ./...`

```bash
git add internal/ui/tabs.go internal/ui/filter_test.go
git commit -m "feat(ui): F starts from the column, completes names and keeps a failed filter

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: 文档

**Files:**
- Modify: `README.md`（按键表和过滤相关的段落）
- Modify: `CHANGELOG.md`（`## [Unreleased]`）

- [ ] **Step 1: 找出要改的地方**

Run: `grep -n '`f`\|`F`\|Filter\|filter\|Ctrl-u' README.md`

- [ ] **Step 2: 改 README**

把描述 `f` 按值过滤的那一行（形如 ``| `f` / `F` | … |``，以实际文字为准）替换为下面几行，放在原位置：

```markdown
| `ff` / `f!` | Filter: the column equals / is not the cell's value (NULL → `IS NULL` / `IS NOT NULL`) |
| `fs` or `f~` | Filter: the column contains… (asks, starting from the cell; ignores case; `%` `_` match themselves) |
| `f>` / `f<` | Filter: the column is greater / less than… (asks, starting from the cell) |
| `fn` / `fN` | Filter: the column is / is not NULL |
| `fx` | Clear the filter |
| `F` | Edit the filter as SQL (`WHERE …`): starts from the column, `Tab` completes column names and keywords |
```

`f` 菜单的条件都用 `AND` 叠加到现有过滤上。如果 README 有讲过滤的段落，补一句：

```markdown
Filters made with `f…` are added with `AND`; `F` shows and edits the whole condition. Text inputs
take readline keys: `←` `→`, `Home` `End` / `Ctrl-a` `Ctrl-e`, `Alt-b` `Alt-f`, `Ctrl-w`, `Ctrl-u`, `Ctrl-k`.
```

- [ ] **Step 3: 改 CHANGELOG**

在 `## [Unreleased]` 下（已有 `### Added` / `### Fixed` 就合并进去）：

```markdown
### Added
- `f` is a quick filter menu for the current column: `ff` equals, `f!` not equal, `fs` / `f~`
  contains, `f>` / `f<` greater / less than, `fn` / `fN` NULL / not NULL, `fx` clears.
- `F` starts from the cursor's column, completes column names and keywords with `Tab`, keeps
  the prompt open on a malformed filter, and brings back a filter the server rejected.
- Text inputs (prompts, `:` and `/`, the connection form, passwords) move the cursor with
  readline keys: `←` `→`, `Home` `End`, `Ctrl-a` `Ctrl-e`, `Alt-b` `Alt-f`, `Ctrl-w`, `Ctrl-k`.

### Changed
- `f` alone no longer filters; `ff` filters by the cell's value.
- `Ctrl-u` in a text input deletes to the start of the line instead of clearing it.
```

- [ ] **Step 4: 验证并提交**

Run: `go test ./...`（确保没误改代码）

```bash
git add README.md CHANGELOG.md
git commit -m "docs: filter menu, F completion and cursor editing

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

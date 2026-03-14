// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	lua "github.com/yuin/gopher-lua"
)

// LuaRuntime loads and executes Lua view scripts that build UI node trees.
type LuaRuntime struct {
	dir string

	mu sync.Mutex
	L  *lua.LState
}

// NewLuaRuntime creates a runtime that loads .lua files from dir.
func NewLuaRuntime(dir string) (*LuaRuntime, error) {
	r := &LuaRuntime{dir: dir}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	return r, nil
}

// Close releases the Lua state.
func (r *LuaRuntime) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.L != nil {
		r.L.Close()
		r.L = nil
	}
}

// Reload re-reads all .lua files from the configured directory.
func (r *LuaRuntime) Reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.L != nil {
		r.L.Close()
	}

	L := lua.NewState()
	r.L = L

	registerNodeFuncs(L)

	entries, err := os.ReadDir(r.dir)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Warn("lua views directory not found, using fallback views", "dir", r.dir)
			return nil
		}
		return fmt.Errorf("read lua dir %s: %w", r.dir, err)
	}

	loaded := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".lua" {
			continue
		}
		path := filepath.Join(r.dir, e.Name())
		if err := L.DoFile(path); err != nil {
			return fmt.Errorf("load %s: %w", path, err)
		}
		loaded++
	}
	slog.Info("lua views loaded", "dir", r.dir, "files", loaded)
	return nil
}

// Scripts reads and concatenates all .lua files from the configured directory,
// returning the raw source text. This is sent to clients for client-side rendering.
func (r *LuaRuntime) Scripts() (string, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read lua dir %s: %w", r.dir, err)
	}

	var buf []byte
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".lua" {
			continue
		}
		path := filepath.Join(r.dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		if len(buf) > 0 {
			buf = append(buf, '\n')
		}
		buf = append(buf, data...)
	}
	return string(buf), nil
}

// CallScreen calls a named Lua function with the given state and returns the
// resulting node tree. If the function doesn't exist, it returns a fallback node.
func (r *LuaRuntime) CallScreen(name string, state map[string]any) (*Node, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	fn := r.L.GetGlobal(name)
	if fn == lua.LNil {
		return &Node{
			Type:  "text",
			Props: Props{Text: fmt.Sprintf("screen %q not defined", name)},
		}, nil
	}

	stateTable := goToLua(r.L, state)
	if err := r.L.CallByParam(lua.P{
		Fn:      fn,
		NRet:    1,
		Protect: true,
	}, stateTable); err != nil {
		return nil, fmt.Errorf("call %s: %w", name, err)
	}

	ret := r.L.Get(-1)
	r.L.Pop(1)

	node, err := luaToNode(ret)
	if err != nil {
		return nil, fmt.Errorf("convert %s result: %w", name, err)
	}
	return node, nil
}

// registerNodeFuncs adds all the UI builder functions to the Lua state.
func registerNodeFuncs(L *lua.LState) {
	// text(str) or text(str, props_table)
	L.SetGlobal("text", L.NewFunction(func(L *lua.LState) int {
		str := L.CheckString(1)
		n := newNodeTable(L, "text")
		setTableString(L, n, "text", str)
		if L.GetTop() >= 2 {
			mergeProps(L, n, L.CheckTable(2))
		}
		L.Push(n)
		return 1
	}))

	// text_styled(str, props_table)
	L.SetGlobal("text_styled", L.NewFunction(func(L *lua.LState) int {
		str := L.CheckString(1)
		props := L.CheckTable(2)
		n := newNodeTable(L, "text")
		setTableString(L, n, "text", str)
		mergeProps(L, n, props)
		L.Push(n)
		return 1
	}))

	// vstack(spacing, ...) / hstack(spacing, ...)
	for _, name := range []string{"vstack", "hstack"} {
		typeName := name
		L.SetGlobal(typeName, L.NewFunction(func(L *lua.LState) int {
			spacing := L.CheckInt(1)
			n := newNodeTable(L, typeName)
			setTableInt(L, n, "spacing", spacing)
			addVarChildren(L, n, 2)
			L.Push(n)
			return 1
		}))
	}

	// zstack(...)
	L.SetGlobal("zstack", L.NewFunction(func(L *lua.LState) int {
		n := newNodeTable(L, "zstack")
		addVarChildren(L, n, 1)
		L.Push(n)
		return 1
	}))

	// spacer(min_length)
	L.SetGlobal("spacer", L.NewFunction(func(L *lua.LState) int {
		minLen := 0
		if L.GetTop() >= 1 {
			minLen = L.CheckInt(1)
		}
		n := newNodeTable(L, "spacer")
		if minLen > 0 {
			setTableInt(L, n, "min_length", minLen)
		}
		L.Push(n)
		return 1
	}))

	// scroll(id, ...)
	L.SetGlobal("scroll", L.NewFunction(func(L *lua.LState) int {
		id := L.CheckString(1)
		n := newNodeTable(L, "scroll")
		setTableString(L, n, "id", id)
		addVarChildren(L, n, 2)
		L.Push(n)
		return 1
	}))

	// list(id, ...)
	L.SetGlobal("list", L.NewFunction(func(L *lua.LState) int {
		id := L.CheckString(1)
		n := newNodeTable(L, "list")
		setTableString(L, n, "id", id)
		addVarChildren(L, n, 2)
		L.Push(n)
		return 1
	}))

	// button(id, label, action)
	L.SetGlobal("button", L.NewFunction(func(L *lua.LState) int {
		id := L.CheckString(1)
		label := L.CheckString(2)
		action := L.CheckString(3)
		n := newNodeTable(L, "button")
		setTableString(L, n, "id", id)
		setTableString(L, n, "text", label)
		setTableString(L, n, "action", action)
		L.Push(n)
		return 1
	}))

	// icon_button(id, sf_symbol, action)
	L.SetGlobal("icon_button", L.NewFunction(func(L *lua.LState) int {
		id := L.CheckString(1)
		sym := L.CheckString(2)
		action := L.CheckString(3)
		n := newNodeTable(L, "button")
		setTableString(L, n, "id", id)
		setTableString(L, n, "sf_symbol", sym)
		setTableString(L, n, "action", action)
		L.Push(n)
		return 1
	}))

	// text_field(id, placeholder, action)
	L.SetGlobal("text_field", L.NewFunction(func(L *lua.LState) int {
		id := L.CheckString(1)
		placeholder := L.CheckString(2)
		action := L.CheckString(3)
		n := newNodeTable(L, "text_field")
		setTableString(L, n, "id", id)
		setTableString(L, n, "placeholder", placeholder)
		setTableString(L, n, "action", action)
		L.Push(n)
		return 1
	}))

	// image_sf(name)
	L.SetGlobal("image_sf", L.NewFunction(func(L *lua.LState) int {
		name := L.CheckString(1)
		n := newNodeTable(L, "image")
		setTableString(L, n, "sf_symbol", name)
		L.Push(n)
		return 1
	}))

	// image_asset(name)
	L.SetGlobal("image_asset", L.NewFunction(func(L *lua.LState) int {
		name := L.CheckString(1)
		n := newNodeTable(L, "image")
		setTableString(L, n, "image_asset", name)
		L.Push(n)
		return 1
	}))

	// image_url(url)
	L.SetGlobal("image_url", L.NewFunction(func(L *lua.LState) int {
		url := L.CheckString(1)
		n := newNodeTable(L, "image")
		setTableString(L, n, "image_url", url)
		L.Push(n)
		return 1
	}))

	// nav(title, toolbar, body)
	L.SetGlobal("nav", L.NewFunction(func(L *lua.LState) int {
		title := L.CheckString(1)
		toolbar := L.Get(2) // may be nil for no toolbar
		body := L.Get(3)
		n := newNodeTable(L, "nav")
		setTableString(L, n, "title", title)
		children := L.NewTable()
		if toolbar != lua.LNil {
			children.Append(toolbar)
		}
		if body != lua.LNil {
			children.Append(body)
		}
		n.RawSetString("children", children)
		L.Push(n)
		return 1
	}))

	// toolbar(leading_table, trailing_table)
	L.SetGlobal("toolbar", L.NewFunction(func(L *lua.LState) int {
		leading := L.CheckTable(1)
		trailing := L.CheckTable(2)
		n := newNodeTable(L, "toolbar")
		// Children: leading items, then trailing items marked
		children := L.NewTable()
		leadingGroup := newNodeTable(L, "toolbar_leading")
		lc := L.NewTable()
		leading.ForEach(func(_, v lua.LValue) { lc.Append(v) })
		leadingGroup.RawSetString("children", lc)
		children.Append(leadingGroup)

		trailingGroup := newNodeTable(L, "toolbar_trailing")
		tc := L.NewTable()
		trailing.ForEach(func(_, v lua.LValue) { tc.Append(v) })
		trailingGroup.RawSetString("children", tc)
		children.Append(trailingGroup)

		n.RawSetString("children", children)
		L.Push(n)
		return 1
	}))

	// sheet(id, content)
	L.SetGlobal("sheet", L.NewFunction(func(L *lua.LState) int {
		id := L.CheckString(1)
		content := L.Get(2)
		n := newNodeTable(L, "sheet")
		setTableString(L, n, "id", id)
		if content != lua.LNil {
			children := L.NewTable()
			children.Append(content)
			n.RawSetString("children", children)
		}
		L.Push(n)
		return 1
	}))

	// badge(text, color)
	L.SetGlobal("badge", L.NewFunction(func(L *lua.LState) int {
		str := L.CheckString(1)
		color := L.CheckString(2)
		n := newNodeTable(L, "text")
		setTableString(L, n, "text", str)
		setTableString(L, n, "font", "caption2")
		setTableString(L, n, "weight", "semibold")
		setTableString(L, n, "color", "white")
		setTableString(L, n, "bg_color", color)
		setTableFloat(L, n, "corner_radius", 4)
		setPadding(L, n, []int{2, 6, 2, 6})
		L.Push(n)
		return 1
	}))

	// progress(text)
	L.SetGlobal("progress", L.NewFunction(func(L *lua.LState) int {
		str := ""
		if L.GetTop() >= 1 {
			str = L.CheckString(1)
		}
		n := newNodeTable(L, "progress")
		if str != "" {
			setTableString(L, n, "text", str)
		}
		L.Push(n)
		return 1
	}))

	// padding(node, values...)
	L.SetGlobal("padding", L.NewFunction(func(L *lua.LState) int {
		child := L.Get(1)
		values := make([]int, 0, 4)
		for i := 2; i <= L.GetTop(); i++ {
			values = append(values, L.CheckInt(i))
		}
		// Wrap the child in a container with padding
		n := newNodeTable(L, "padding")
		setPadding(L, n, values)
		children := L.NewTable()
		children.Append(child)
		n.RawSetString("children", children)
		L.Push(n)
		return 1
	}))

	// background(node, color, corner_radius)
	L.SetGlobal("background", L.NewFunction(func(L *lua.LState) int {
		child := L.Get(1)
		color := L.CheckString(2)
		cr := 0.0
		if L.GetTop() >= 3 {
			cr = float64(L.CheckNumber(3))
		}
		n := newNodeTable(L, "background")
		setTableString(L, n, "bg_color", color)
		if cr > 0 {
			setTableFloat(L, n, "corner_radius", cr)
		}
		children := L.NewTable()
		children.Append(child)
		n.RawSetString("children", children)
		L.Push(n)
		return 1
	}))

	// swipe_action(id, label, action, style)
	L.SetGlobal("swipe_action", L.NewFunction(func(L *lua.LState) int {
		id := L.CheckString(1)
		label := L.CheckString(2)
		action := L.CheckString(3)
		style := ""
		if L.GetTop() >= 4 {
			style = L.CheckString(4)
		}
		n := newNodeTable(L, "swipe_action")
		setTableString(L, n, "id", id)
		setTableString(L, n, "text", label)
		setTableString(L, n, "action", action)
		if style != "" {
			setTableString(L, n, "style", style)
		}
		L.Push(n)
		return 1
	}))

	// tap(id, action, child)
	L.SetGlobal("tap", L.NewFunction(func(L *lua.LState) int {
		id := L.CheckString(1)
		action := L.CheckString(2)
		child := L.Get(3)
		n := newNodeTable(L, "tap")
		setTableString(L, n, "id", id)
		setTableString(L, n, "action", action)
		children := L.NewTable()
		children.Append(child)
		n.RawSetString("children", children)
		L.Push(n)
		return 1
	}))

	// props(table) — create a props modifier (returns the table as-is for mergeProps)
	L.SetGlobal("props", L.NewFunction(func(L *lua.LState) int {
		L.Push(L.CheckTable(1))
		return 1
	}))

	// with_props(node, props_table) — apply props to an existing node
	L.SetGlobal("with_props", L.NewFunction(func(L *lua.LState) int {
		node := L.CheckTable(1)
		p := L.CheckTable(2)
		mergeProps(L, node, p)
		L.Push(node)
		return 1
	}))
}

// --- Lua table helpers ---

func newNodeTable(L *lua.LState, typeName string) *lua.LTable {
	t := L.NewTable()
	t.RawSetString("type", lua.LString(typeName))
	return t
}

func setTableString(L *lua.LState, t *lua.LTable, key, val string) {
	t.RawSetString(key, lua.LString(val))
}

func setTableInt(L *lua.LState, t *lua.LTable, key string, val int) {
	t.RawSetString(key, lua.LNumber(val))
}

func setTableFloat(L *lua.LState, t *lua.LTable, key string, val float64) {
	t.RawSetString(key, lua.LNumber(val))
}

func setPadding(L *lua.LState, t *lua.LTable, values []int) {
	pt := L.NewTable()
	for _, v := range values {
		pt.Append(lua.LNumber(v))
	}
	t.RawSetString("padding", pt)
}

// mergeProps copies all fields from props table into the node table.
func mergeProps(L *lua.LState, node, props *lua.LTable) {
	props.ForEach(func(k, v lua.LValue) {
		node.RawSet(k, v)
	})
}

// addVarChildren adds all arguments from startIdx onward as children.
func addVarChildren(L *lua.LState, t *lua.LTable, startIdx int) {
	children := L.NewTable()
	for i := startIdx; i <= L.GetTop(); i++ {
		v := L.Get(i)
		if v == lua.LNil {
			continue
		}
		children.Append(v)
	}
	if children.Len() > 0 {
		t.RawSetString("children", children)
	}
}

// --- Lua table → Node conversion ---

func luaToNode(v lua.LValue) (*Node, error) {
	t, ok := v.(*lua.LTable)
	if !ok {
		return nil, fmt.Errorf("expected table, got %s", v.Type())
	}

	node := &Node{
		Type: luaString(t, "type"),
		ID:   luaString(t, "id"),
	}

	// Props
	node.Props.Text = luaString(t, "text")
	node.Props.Placeholder = luaString(t, "placeholder")
	node.Props.SFSymbol = luaString(t, "sf_symbol")
	node.Props.ImageAsset = luaString(t, "image_asset")
	node.Props.ImageURL = luaString(t, "image_url")
	node.Props.Font = luaString(t, "font")
	node.Props.Weight = luaString(t, "weight")
	node.Props.Color = luaString(t, "color")
	node.Props.BgColor = luaString(t, "bg_color")
	node.Props.CornerRadius = luaFloat(t, "corner_radius")
	node.Props.Opacity = luaFloat(t, "opacity")
	node.Props.Spacing = luaInt(t, "spacing")
	node.Props.MinLength = luaInt(t, "min_length")
	node.Props.Alignment = luaString(t, "alignment")
	node.Props.MaxLines = luaInt(t, "max_lines")
	node.Props.Truncate = luaString(t, "truncate")
	node.Props.Title = luaString(t, "title")
	node.Props.Disabled = luaBool(t, "disabled")
	node.Props.Action = luaString(t, "action")
	node.Props.Style = luaString(t, "style")

	// Padding
	if pt, ok := t.RawGetString("padding").(*lua.LTable); ok {
		pt.ForEach(func(_, v lua.LValue) {
			if n, ok := v.(lua.LNumber); ok {
				node.Props.Padding = append(node.Props.Padding, int(n))
			}
		})
	}

	// Children
	if ct, ok := t.RawGetString("children").(*lua.LTable); ok {
		ct.ForEach(func(_, v lua.LValue) {
			child, err := luaToNode(v)
			if err != nil {
				slog.Warn("skipping invalid child node", "err", err)
				return
			}
			node.Children = append(node.Children, *child)
		})
	}

	return node, nil
}

func luaString(t *lua.LTable, key string) string {
	v := t.RawGetString(key)
	if s, ok := v.(lua.LString); ok {
		return string(s)
	}
	return ""
}

func luaFloat(t *lua.LTable, key string) float64 {
	v := t.RawGetString(key)
	if n, ok := v.(lua.LNumber); ok {
		return float64(n)
	}
	return 0
}

func luaInt(t *lua.LTable, key string) int {
	v := t.RawGetString(key)
	if n, ok := v.(lua.LNumber); ok {
		return int(n)
	}
	return 0
}

func luaBool(t *lua.LTable, key string) bool {
	v := t.RawGetString(key)
	if b, ok := v.(lua.LBool); ok {
		return bool(b)
	}
	return false
}

// goToLua converts a Go value to a Lua value, recursively handling maps and slices.
func goToLua(L *lua.LState, v any) lua.LValue {
	switch val := v.(type) {
	case nil:
		return lua.LNil
	case string:
		return lua.LString(val)
	case bool:
		return lua.LBool(val)
	case int:
		return lua.LNumber(val)
	case int64:
		return lua.LNumber(val)
	case float64:
		return lua.LNumber(val)
	case map[string]any:
		t := L.NewTable()
		for k, v := range val {
			t.RawSetString(k, goToLua(L, v))
		}
		return t
	case []any:
		t := L.NewTable()
		for _, v := range val {
			t.Append(goToLua(L, v))
		}
		return t
	case []map[string]any:
		t := L.NewTable()
		for _, v := range val {
			t.Append(goToLua(L, v))
		}
		return t
	default:
		return lua.LString(fmt.Sprint(val))
	}
}

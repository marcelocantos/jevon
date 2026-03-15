// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import Foundation
import os

private let logger = Logger(subsystem: "com.marcelocantos.jevon", category: "LuaRuntime")

// MARK: - Lua macro replacements (C macros aren't visible to Swift)

private func luaPop(_ L: OpaquePointer, _ n: Int32) {
    lua_settop(L, -(n) - 1)
}

private func luaIsNil(_ L: OpaquePointer, _ idx: Int32) -> Bool {
    lua_type(L, idx) == LUA_TNIL
}

private func luaIsTable(_ L: OpaquePointer, _ idx: Int32) -> Bool {
    lua_type(L, idx) == LUA_TTABLE
}

// MARK: - LuaRuntime

/// Runs Lua view scripts that build UI node trees. Mirrors the Go LuaRuntime
/// in internal/ui/lua.go — same builder functions, same table schema.
@MainActor
final class LuaRuntime {
    nonisolated(unsafe) private var L: OpaquePointer?

    init() {
        L = luaL_newstate()
        guard let L else {
            logger.error("Failed to create Lua state")
            return
        }
        openSafeLibs(L)
        registerBuilders(L)
    }

    deinit {
        if let L { lua_close(L) }
    }

    /// Load and execute a Lua source string. Returns true on success.
    func loadScript(_ source: String) -> Bool {
        guard let L else { return false }
        injectEnvironment(L)
        if luaL_loadstring(L, source) != 0 {
            logAndPop(L, "load")
            return false
        }
        if lua_pcall(L, 0, 0, 0) != 0 {
            logAndPop(L, "exec")
            return false
        }
        return true
    }

    /// Call a named Lua screen function with the given state dictionary.
    /// Returns nil on error (logged internally).
    func callScreen(_ name: String, state: [String: Any]) -> ViewNode? {
        guard let L else { return nil }

        lua_getfield(L, LUA_GLOBALSINDEX, name)
        if luaIsNil(L, -1) {
            luaPop(L, 1)
            logger.warning("Screen function '\(name)' not defined")
            return ViewNode(
                type: "text", id: nil,
                props: ViewProps(
                    text: "screen \"\(name)\" not defined",
                    placeholder: nil, sfSymbol: nil, imageAsset: nil,
                    imageURL: nil, font: nil, weight: nil, color: nil,
                    bgColor: nil, cornerRadius: nil, opacity: nil,
                    spacing: nil, padding: nil, minLength: nil,
                    alignment: nil, maxLines: nil, truncate: nil,
                    title: nil, titleDisplayMode: nil,
                    disabled: nil, action: nil, style: nil,
                    keyboard: nil, autocorrect: nil,
                    autocapitalize: nil, submitLabel: nil,
                    scrollAnchor: nil, scrollDismissKeyboard: nil,
                    keyboardAvoidance: nil,
                    frameWidth: nil, frameHeight: nil,
                    frameMaxWidth: nil, frameMaxHeight: nil,
                    foregroundStyle: nil, contentMode: nil,
                    a11yLabel: nil
                ),
                children: nil
            )
        }

        pushSwiftValue(L, state)

        if lua_pcall(L, 1, 1, 0) != 0 {
            logAndPop(L, "call \(name)")
            return nil
        }

        let node = luaTableToViewNode(L, at: -1)
        luaPop(L, 1)
        return node
    }
}

// MARK: - Environment injection

/// Provide an `os` table with `getenv` so Lua scripts can access HOME etc.
private func injectEnvironment(_ L: OpaquePointer) {
    lua_createtable(L, 0, 1)
    lua_pushcclosure(L, { L -> Int32 in
        guard let L else { return 0 }
        guard let ckey = lua_tolstring(L, 1, nil) else {
            lua_pushnil(L)
            return 1
        }
        let key = String(cString: ckey)
        if let val = ProcessInfo.processInfo.environment[key] {
            lua_pushstring(L, val)
        } else {
            lua_pushnil(L)
        }
        return 1
    }, 0)
    lua_setfield(L, -2, "getenv")
    lua_setfield(L, LUA_GLOBALSINDEX, "os")
}

// MARK: - Error helper

private func logAndPop(_ L: OpaquePointer, _ context: String) {
    if let cstr = lua_tolstring(L, -1, nil) {
        logger.error("Lua \(context) error: \(String(cString: cstr))")
    }
    luaPop(L, 1)
}

// MARK: - Safe library loading

private func openSafeLibs(_ L: OpaquePointer) {
    // Each luaopen_* pushes its library table; pop after each.
    luaopen_base(L)
    lua_settop(L, 0)
    luaopen_table(L)
    lua_settop(L, 0)
    luaopen_string(L)
    lua_settop(L, 0)
    luaopen_math(L)
    lua_settop(L, 0)
}

// MARK: - Builder registration

private func reg(_ L: OpaquePointer, _ name: String,
                 _ fn: @convention(c) (OpaquePointer?) -> Int32) {
    lua_pushcclosure(L, fn, 0)
    lua_setfield(L, LUA_GLOBALSINDEX, name)
}

private func registerBuilders(_ L: OpaquePointer) {
    reg(L, "text",         luaFn_text)
    reg(L, "text_styled",  luaFn_textStyled)
    reg(L, "vstack",       luaFn_vstack)
    reg(L, "hstack",       luaFn_hstack)
    reg(L, "zstack",       luaFn_zstack)
    reg(L, "spacer",       luaFn_spacer)
    reg(L, "scroll",       luaFn_scroll)
    reg(L, "list",         luaFn_list)
    reg(L, "button",       luaFn_button)
    reg(L, "icon_button",  luaFn_iconButton)
    reg(L, "text_field",   luaFn_textField)
    reg(L, "image_sf",     luaFn_imageSF)
    reg(L, "image_asset",  luaFn_imageAsset)
    reg(L, "image_url",    luaFn_imageURL)
    reg(L, "nav",          luaFn_nav)
    reg(L, "toolbar",      luaFn_toolbar)
    reg(L, "sheet",        luaFn_sheet)
    reg(L, "badge",        luaFn_badge)
    reg(L, "progress",     luaFn_progress)
    reg(L, "padding",      luaFn_padding)
    reg(L, "background",   luaFn_background)
    reg(L, "swipe_action", luaFn_swipeAction)
    reg(L, "tap",          luaFn_tap)
    reg(L, "props",        luaFn_props)
    reg(L, "with_props",   luaFn_withProps)
}

// MARK: - Table construction helpers

/// Create a new table at the top of the stack with type=typeName.
private func newNodeTable(_ L: OpaquePointer, _ typeName: String) {
    lua_createtable(L, 0, 4)
    lua_pushstring(L, typeName)
    lua_setfield(L, -2, "type")
}

/// Set a string field on the table at the top of the stack.
private func setField(_ L: OpaquePointer, _ key: String, _ val: String) {
    lua_pushstring(L, val)
    lua_setfield(L, -2, key)
}

/// Set an integer field on the table at the top of the stack.
private func setFieldNum(_ L: OpaquePointer, _ key: String, _ val: Double) {
    lua_pushnumber(L, val)
    lua_setfield(L, -2, key)
}

/// Read a string argument, returning "" if it's not a string.
private func getString(_ L: OpaquePointer, _ idx: Int32) -> String {
    guard let cstr = lua_tolstring(L, idx, nil) else { return "" }
    return String(cString: cstr)
}

/// Collect varargs from startIdx..argCount as children on the node table at nodeAbsIdx.
private func addVarChildren(_ L: OpaquePointer, nodeAbsIdx: Int32, startIdx: Int32, argCount: Int32) {
    guard startIdx <= argCount else { return }

    lua_createtable(L, argCount - startIdx + 1, 0)
    var n: Int32 = 0
    for i in startIdx...argCount {
        if lua_type(L, i) == LUA_TNIL { continue }
        n += 1
        lua_pushvalue(L, i)
        lua_rawseti(L, -2, n)
    }

    if n > 0 {
        lua_setfield(L, nodeAbsIdx, "children")
    } else {
        luaPop(L, 1)
    }
}

/// Copy all fields from the table at propsAbsIdx into the table at nodeAbsIdx.
private func mergeProps(_ L: OpaquePointer, nodeAbsIdx: Int32, propsAbsIdx: Int32) {
    lua_pushnil(L)
    while lua_next(L, propsAbsIdx) != 0 {
        lua_pushvalue(L, -2) // copy key
        lua_insert(L, -2)    // swap so key is below value
        lua_rawset(L, nodeAbsIdx)
        // key remains at top for lua_next
    }
}

/// Set padding array on the table at the top of the stack.
private func setPaddingValues(_ L: OpaquePointer, _ values: [Int32]) {
    lua_createtable(L, Int32(values.count), 0)
    for (i, v) in values.enumerated() {
        lua_pushnumber(L, Double(v))
        lua_rawseti(L, -2, Int32(i + 1))
    }
    lua_setfield(L, -2, "padding")
}

// MARK: - Builder C functions

// text(str) or text(str, props_table)
private func luaFn_text(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let argCount = lua_gettop(L)
    let str = getString(L, 1)
    newNodeTable(L, "text")
    setField(L, "text", str)
    let nodeIdx = lua_gettop(L)
    if argCount >= 2 && luaIsTable(L, 2) {
        mergeProps(L, nodeAbsIdx: nodeIdx, propsAbsIdx: 2)
    }
    return 1
}

// text_styled(str, props_table)
private func luaFn_textStyled(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let str = getString(L, 1)
    newNodeTable(L, "text")
    setField(L, "text", str)
    let nodeIdx = lua_gettop(L)
    if luaIsTable(L, 2) {
        mergeProps(L, nodeAbsIdx: nodeIdx, propsAbsIdx: 2)
    }
    return 1
}

// vstack(spacing, ...)
private func luaFn_vstack(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let argCount = lua_gettop(L)
    let spacing = lua_tonumber(L, 1)
    newNodeTable(L, "vstack")
    setFieldNum(L, "spacing", spacing)
    let nodeIdx = lua_gettop(L)
    addVarChildren(L, nodeAbsIdx: nodeIdx, startIdx: 2, argCount: argCount)
    return 1
}

// hstack(spacing, ...)
private func luaFn_hstack(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let argCount = lua_gettop(L)
    let spacing = lua_tonumber(L, 1)
    newNodeTable(L, "hstack")
    setFieldNum(L, "spacing", spacing)
    let nodeIdx = lua_gettop(L)
    addVarChildren(L, nodeAbsIdx: nodeIdx, startIdx: 2, argCount: argCount)
    return 1
}

// zstack(...)
private func luaFn_zstack(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let argCount = lua_gettop(L)
    newNodeTable(L, "zstack")
    let nodeIdx = lua_gettop(L)
    addVarChildren(L, nodeAbsIdx: nodeIdx, startIdx: 1, argCount: argCount)
    return 1
}

// spacer(min_length?)
private func luaFn_spacer(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let argCount = lua_gettop(L)
    newNodeTable(L, "spacer")
    if argCount >= 1 {
        let minLen = lua_tonumber(L, 1)
        if minLen > 0 { setFieldNum(L, "min_length", minLen) }
    }
    return 1
}

// scroll(id, ...)
private func luaFn_scroll(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let argCount = lua_gettop(L)
    let id = getString(L, 1)
    newNodeTable(L, "scroll")
    setField(L, "id", id)
    let nodeIdx = lua_gettop(L)
    addVarChildren(L, nodeAbsIdx: nodeIdx, startIdx: 2, argCount: argCount)
    return 1
}

// list(id, ...)
private func luaFn_list(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let argCount = lua_gettop(L)
    let id = getString(L, 1)
    newNodeTable(L, "list")
    setField(L, "id", id)
    let nodeIdx = lua_gettop(L)
    addVarChildren(L, nodeAbsIdx: nodeIdx, startIdx: 2, argCount: argCount)
    return 1
}

// button(id, label, action)
private func luaFn_button(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let id = getString(L, 1)
    let label = getString(L, 2)
    let action = getString(L, 3)
    newNodeTable(L, "button")
    setField(L, "id", id)
    setField(L, "text", label)
    setField(L, "action", action)
    return 1
}

// icon_button(id, sf_symbol, action)
private func luaFn_iconButton(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let id = getString(L, 1)
    let sym = getString(L, 2)
    let action = getString(L, 3)
    newNodeTable(L, "button")
    setField(L, "id", id)
    setField(L, "sf_symbol", sym)
    setField(L, "action", action)
    return 1
}

// text_field(id, placeholder, action)
private func luaFn_textField(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let id = getString(L, 1)
    let placeholder = getString(L, 2)
    let action = getString(L, 3)
    newNodeTable(L, "text_field")
    setField(L, "id", id)
    setField(L, "placeholder", placeholder)
    setField(L, "action", action)
    return 1
}

// image_sf(name)
private func luaFn_imageSF(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let name = getString(L, 1)
    newNodeTable(L, "image")
    setField(L, "sf_symbol", name)
    return 1
}

// image_asset(name)
private func luaFn_imageAsset(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let name = getString(L, 1)
    newNodeTable(L, "image")
    setField(L, "image_asset", name)
    return 1
}

// image_url(url)
private func luaFn_imageURL(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let url = getString(L, 1)
    newNodeTable(L, "image")
    setField(L, "image_url", url)
    return 1
}

// nav(title, toolbar_or_nil, body)
private func luaFn_nav(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let title = getString(L, 1)
    newNodeTable(L, "nav")
    setField(L, "title", title)
    let nodeIdx = lua_gettop(L)

    lua_createtable(L, 2, 0)
    var n: Int32 = 0
    // arg 2 = toolbar (may be nil)
    if !luaIsNil(L, 2) {
        n += 1
        lua_pushvalue(L, 2)
        lua_rawseti(L, -2, n)
    }
    // arg 3 = body (may be nil)
    if lua_gettop(L) >= 3 || (!luaIsNil(L, 3)) {
        // Check original argcount: args are at 1,2,3; nodeTable at 4; children at 5
        if lua_type(L, 3) != LUA_TNIL && lua_type(L, 3) != LUA_TNONE {
            n += 1
            lua_pushvalue(L, 3)
            lua_rawseti(L, -2, n)
        }
    }

    if n > 0 {
        lua_setfield(L, nodeIdx, "children")
    } else {
        luaPop(L, 1)
    }
    return 1
}

// toolbar(leading_table, trailing_table)
private func luaFn_toolbar(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    newNodeTable(L, "toolbar")
    let nodeIdx = lua_gettop(L)

    lua_createtable(L, 2, 0) // children array

    // toolbar_leading group
    newNodeTable(L, "toolbar_leading")
    let leadingIdx = lua_gettop(L)
    copyArrayElements(L, fromTableAt: 1, toNodeAt: leadingIdx)
    lua_rawseti(L, -2, 1) // children[1] = toolbar_leading

    // toolbar_trailing group
    newNodeTable(L, "toolbar_trailing")
    let trailingIdx = lua_gettop(L)
    copyArrayElements(L, fromTableAt: 2, toNodeAt: trailingIdx)
    lua_rawseti(L, -2, 2) // children[2] = toolbar_trailing

    lua_setfield(L, nodeIdx, "children")
    return 1
}

/// Copy array elements from a table at srcAbsIdx into a "children" field on the table at dstAbsIdx.
private func copyArrayElements(_ L: OpaquePointer, fromTableAt srcAbsIdx: Int32, toNodeAt dstAbsIdx: Int32) {
    guard luaIsTable(L, srcAbsIdx) else { return }
    let len = Int32(lua_objlen(L, srcAbsIdx))
    guard len > 0 else { return }

    lua_createtable(L, len, 0)
    for i: Int32 in 1...len {
        lua_rawgeti(L, srcAbsIdx, i)
        lua_rawseti(L, -2, i)
    }
    lua_setfield(L, dstAbsIdx, "children")
}

// sheet(id, content)
private func luaFn_sheet(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let id = getString(L, 1)
    newNodeTable(L, "sheet")
    setField(L, "id", id)
    let nodeIdx = lua_gettop(L)
    if lua_gettop(L) >= 2 && !luaIsNil(L, 2) {
        // Hmm, gettop includes nodeTable. Original args: 1=id, 2=content.
        // After newNodeTable pushed, 2 is still content (it's below the node table).
        if lua_type(L, 2) != LUA_TNIL {
            lua_createtable(L, 1, 0)
            lua_pushvalue(L, 2)
            lua_rawseti(L, -2, 1)
            lua_setfield(L, nodeIdx, "children")
        }
    }
    return 1
}

// badge(text, color)
private func luaFn_badge(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let str = getString(L, 1)
    let color = getString(L, 2)
    newNodeTable(L, "text")
    setField(L, "text", str)
    setField(L, "font", "caption2")
    setField(L, "weight", "semibold")
    setField(L, "color", "white")
    setField(L, "bg_color", color)
    setFieldNum(L, "corner_radius", 4)
    setPaddingValues(L, [2, 6, 2, 6])
    return 1
}

// progress(text?)
private func luaFn_progress(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let argCount = lua_gettop(L)
    newNodeTable(L, "progress")
    if argCount >= 1 {
        let str = getString(L, 1)
        if !str.isEmpty { setField(L, "text", str) }
    }
    return 1
}

// padding(child, values...)
private func luaFn_padding(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let argCount = lua_gettop(L)
    // Collect padding values from args 2..N
    var values: [Int32] = []
    if argCount >= 2 {
        for i: Int32 in 2...argCount {
            values.append(Int32(lua_tonumber(L, i)))
        }
    }
    newNodeTable(L, "padding")
    setPaddingValues(L, values)
    let nodeIdx = lua_gettop(L)
    // Wrap child (arg 1) as sole child
    lua_createtable(L, 1, 0)
    lua_pushvalue(L, 1)
    lua_rawseti(L, -2, 1)
    lua_setfield(L, nodeIdx, "children")
    return 1
}

// background(child, color, corner_radius?)
private func luaFn_background(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let argCount = lua_gettop(L)
    let color = getString(L, 2)
    newNodeTable(L, "background")
    setField(L, "bg_color", color)
    if argCount >= 3 {
        let cr = lua_tonumber(L, 3)
        if cr > 0 { setFieldNum(L, "corner_radius", cr) }
    }
    let nodeIdx = lua_gettop(L)
    lua_createtable(L, 1, 0)
    lua_pushvalue(L, 1)
    lua_rawseti(L, -2, 1)
    lua_setfield(L, nodeIdx, "children")
    return 1
}

// swipe_action(id, label, action, style?)
private func luaFn_swipeAction(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let argCount = lua_gettop(L)
    let id = getString(L, 1)
    let label = getString(L, 2)
    let action = getString(L, 3)
    newNodeTable(L, "swipe_action")
    setField(L, "id", id)
    setField(L, "text", label)
    setField(L, "action", action)
    if argCount >= 4 {
        let style = getString(L, 4)
        if !style.isEmpty { setField(L, "style", style) }
    }
    return 1
}

// tap(id, action, child)
private func luaFn_tap(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    let id = getString(L, 1)
    let action = getString(L, 2)
    newNodeTable(L, "tap")
    setField(L, "id", id)
    setField(L, "action", action)
    let nodeIdx = lua_gettop(L)
    lua_createtable(L, 1, 0)
    lua_pushvalue(L, 3)
    lua_rawseti(L, -2, 1)
    lua_setfield(L, nodeIdx, "children")
    return 1
}

// props(table) — pass-through
private func luaFn_props(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    // Just return arg 1 as-is (must be a table)
    lua_pushvalue(L, 1)
    return 1
}

// with_props(node, props_table) — merge props into node
private func luaFn_withProps(_ L: OpaquePointer?) -> Int32 {
    guard let L else { return 0 }
    // arg 1 = node table, arg 2 = props table
    if luaIsTable(L, 1) && luaIsTable(L, 2) {
        mergeProps(L, nodeAbsIdx: 1, propsAbsIdx: 2)
    }
    lua_pushvalue(L, 1)
    return 1
}

// MARK: - Swift value → Lua table (for state dict)

/// Push a Swift value onto the Lua stack, recursively converting dicts and arrays.
private func pushSwiftValue(_ L: OpaquePointer, _ value: Any) {
    switch value {
    case let s as String:
        lua_pushstring(L, s)
    case let b as Bool:
        lua_pushboolean(L, b ? 1 : 0)
    case let n as Int:
        lua_pushnumber(L, Double(n))
    case let n as Int64:
        lua_pushnumber(L, Double(n))
    case let n as Double:
        lua_pushnumber(L, n)
    case let dict as [String: Any]:
        lua_createtable(L, 0, Int32(dict.count))
        for (k, v) in dict {
            pushSwiftValue(L, v)
            lua_setfield(L, -2, k)
        }
    case let arr as [Any]:
        lua_createtable(L, Int32(arr.count), 0)
        for (i, v) in arr.enumerated() {
            pushSwiftValue(L, v)
            lua_rawseti(L, -2, Int32(i + 1))
        }
    case let arr as [[String: Any]]:
        lua_createtable(L, Int32(arr.count), 0)
        for (i, v) in arr.enumerated() {
            pushSwiftValue(L, v)
            lua_rawseti(L, -2, Int32(i + 1))
        }
    default:
        lua_pushstring(L, String(describing: value))
    }
}

// MARK: - Lua table → ViewNode conversion

/// Convert the Lua table at the given stack index to a ViewNode.
private func luaTableToViewNode(_ L: OpaquePointer, at idx: Int32) -> ViewNode? {
    guard luaIsTable(L, idx) else { return nil }
    let absIdx: Int32 = idx > 0 ? idx : lua_gettop(L) + idx + 1

    let type = getStringField(L, absIdx, "type") ?? ""
    let id = getStringField(L, absIdx, "id")

    let props = ViewProps(
        text: getStringField(L, absIdx, "text"),
        placeholder: getStringField(L, absIdx, "placeholder"),
        sfSymbol: getStringField(L, absIdx, "sf_symbol"),
        imageAsset: getStringField(L, absIdx, "image_asset"),
        imageURL: getStringField(L, absIdx, "image_url"),
        font: getStringField(L, absIdx, "font"),
        weight: getStringField(L, absIdx, "weight"),
        color: getStringField(L, absIdx, "color"),
        bgColor: getStringField(L, absIdx, "bg_color"),
        cornerRadius: getDoubleField(L, absIdx, "corner_radius"),
        opacity: getDoubleField(L, absIdx, "opacity"),
        spacing: getIntField(L, absIdx, "spacing"),
        padding: getPaddingField(L, absIdx),
        minLength: getIntField(L, absIdx, "min_length"),
        alignment: getStringField(L, absIdx, "alignment"),
        maxLines: getIntField(L, absIdx, "max_lines"),
        truncate: getStringField(L, absIdx, "truncate"),
        title: getStringField(L, absIdx, "title"),
        titleDisplayMode: getStringField(L, absIdx, "title_display_mode"),
        disabled: getBoolField(L, absIdx, "disabled"),
        action: getStringField(L, absIdx, "action"),
        style: getStringField(L, absIdx, "style"),
        keyboard: getStringField(L, absIdx, "keyboard"),
        autocorrect: getBoolField(L, absIdx, "autocorrect"),
        autocapitalize: getStringField(L, absIdx, "autocapitalize"),
        submitLabel: getStringField(L, absIdx, "submit_label"),
        scrollAnchor: getStringField(L, absIdx, "scroll_anchor"),
        scrollDismissKeyboard: getStringField(L, absIdx, "scroll_dismiss_keyboard"),
        keyboardAvoidance: getStringField(L, absIdx, "keyboard_avoidance"),
        frameWidth: getDoubleField(L, absIdx, "frame_width"),
        frameHeight: getDoubleField(L, absIdx, "frame_height"),
        frameMaxWidth: getFrameDimField(L, absIdx, "frame_max_width"),
        frameMaxHeight: getFrameDimField(L, absIdx, "frame_max_height"),
        foregroundStyle: getStringField(L, absIdx, "foreground_style"),
        contentMode: getStringField(L, absIdx, "content_mode"),
        a11yLabel: getStringField(L, absIdx, "a11y_label")
    )

    // Children
    var children: [ViewNode]?
    lua_getfield(L, absIdx, "children")
    if luaIsTable(L, -1) {
        let childrenIdx = lua_gettop(L)
        let len = Int32(lua_objlen(L, childrenIdx))
        if len > 0 {
            children = []
            for i: Int32 in 1...len {
                lua_rawgeti(L, childrenIdx, i)
                if let child = luaTableToViewNode(L, at: -1) {
                    children?.append(child)
                }
                luaPop(L, 1)
            }
        }
    }
    luaPop(L, 1) // pop children field (or nil)

    // Only pass props if at least one field is non-nil
    let hasProps = props.text != nil || props.placeholder != nil
        || props.sfSymbol != nil || props.imageAsset != nil || props.imageURL != nil
        || props.font != nil || props.weight != nil
        || props.color != nil || props.bgColor != nil
        || props.cornerRadius != nil || props.opacity != nil
        || props.spacing != nil || props.padding != nil || props.minLength != nil
        || props.alignment != nil || props.maxLines != nil || props.truncate != nil
        || props.title != nil || props.titleDisplayMode != nil || props.disabled != nil
        || props.action != nil || props.style != nil
        || props.keyboard != nil || props.autocorrect != nil
        || props.autocapitalize != nil || props.submitLabel != nil
        || props.scrollAnchor != nil || props.scrollDismissKeyboard != nil
        || props.keyboardAvoidance != nil
        || props.frameWidth != nil || props.frameHeight != nil
        || props.frameMaxWidth != nil || props.frameMaxHeight != nil
        || props.foregroundStyle != nil || props.contentMode != nil
        || props.a11yLabel != nil

    return ViewNode(
        type: type,
        id: id,
        props: hasProps ? props : nil,
        children: children
    )
}

// MARK: - Field extraction helpers

private func getStringField(_ L: OpaquePointer, _ tableIdx: Int32, _ key: String) -> String? {
    lua_getfield(L, tableIdx, key)
    defer { luaPop(L, 1) }
    guard lua_type(L, -1) == LUA_TSTRING, let cstr = lua_tolstring(L, -1, nil) else {
        return nil
    }
    return String(cString: cstr)
}

private func getDoubleField(_ L: OpaquePointer, _ tableIdx: Int32, _ key: String) -> Double? {
    lua_getfield(L, tableIdx, key)
    defer { luaPop(L, 1) }
    guard lua_type(L, -1) == LUA_TNUMBER else { return nil }
    return lua_tonumber(L, -1)
}

private func getIntField(_ L: OpaquePointer, _ tableIdx: Int32, _ key: String) -> Int? {
    lua_getfield(L, tableIdx, key)
    defer { luaPop(L, 1) }
    guard lua_type(L, -1) == LUA_TNUMBER else { return nil }
    return Int(lua_tonumber(L, -1))
}

private func getBoolField(_ L: OpaquePointer, _ tableIdx: Int32, _ key: String) -> Bool? {
    lua_getfield(L, tableIdx, key)
    defer { luaPop(L, 1) }
    guard lua_type(L, -1) == LUA_TBOOLEAN else { return nil }
    return lua_toboolean(L, -1) != 0
}

private func getFrameDimField(_ L: OpaquePointer, _ tableIdx: Int32, _ key: String) -> FrameDimension? {
    lua_getfield(L, tableIdx, key)
    defer { luaPop(L, 1) }
    let t = lua_type(L, -1)
    if t == LUA_TNUMBER {
        return .value(lua_tonumber(L, -1))
    } else if t == LUA_TSTRING, let cstr = lua_tolstring(L, -1, nil), String(cString: cstr) == "infinity" {
        return .infinity
    }
    return nil
}

private func getPaddingField(_ L: OpaquePointer, _ tableIdx: Int32) -> [Int]? {
    lua_getfield(L, tableIdx, "padding")
    defer { luaPop(L, 1) }
    guard luaIsTable(L, -1) else { return nil }
    let padIdx = lua_gettop(L)
    let len = Int32(lua_objlen(L, padIdx))
    guard len > 0 else { return nil }
    var result: [Int] = []
    for i: Int32 in 1...len {
        lua_rawgeti(L, padIdx, i)
        result.append(Int(lua_tonumber(L, -1)))
        luaPop(L, 1)
    }
    return result
}

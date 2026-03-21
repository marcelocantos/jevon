-- Copyright 2026 Marcelo Cantos
-- SPDX-License-Identifier: Apache-2.0

-- React-style virtual DOM reconciler for Jevon's server-driven UI.
--
-- Screen functions produce a desired tree each render. The reconciler
-- diffs against the committed tree and emits a minimal patch list.
-- Swift applies patches to a persistent Observable tree, preserving
-- SwiftUI view identity and state (focus, scroll position, etc.).
--
-- Patch operations:
--   {op="init",    node=<full tree>}          — first render
--   {op="update",  id=<string>, props={...}}  — changed props on existing node
--   {op="insert",  parent=<string>, index=<int>, node=<full subtree>}
--   {op="remove",  id=<string>}               — remove node and subtree
--   {op="replace", id=<string>, node=<full subtree>} — type changed
--   {op="reorder", parent=<string>, ids={...}} — children reordered

-- Skip these keys when comparing props.
local STRUCTURAL = {type = true, id = true, children = true}

--------------------------------------------------------------------------------
-- Deep equality
--------------------------------------------------------------------------------

local function deep_equal(a, b)
    if a == b then return true end
    local ta, tb = type(a), type(b)
    if ta ~= tb then return false end
    if ta ~= "table" then return false end
    for k, v in pairs(a) do
        if not deep_equal(v, b[k]) then return false end
    end
    for k in pairs(b) do
        if a[k] == nil then return false end
    end
    return true
end

--------------------------------------------------------------------------------
-- Prop diffing
--------------------------------------------------------------------------------

--- Return a table of changed prop values, or nil if nothing changed.
--- Only non-structural fields (not type/id/children) are compared.
local function diff_props(old, new)
    local changed = nil
    -- Check for changed or added props.
    for k, v in pairs(new) do
        if not STRUCTURAL[k] and not deep_equal(v, old[k]) then
            if not changed then changed = {} end
            changed[k] = v
        end
    end
    -- Check for removed props.
    for k in pairs(old) do
        if not STRUCTURAL[k] and new[k] == nil then
            if not changed then changed = {} end
            changed[k] = "__nil__"  -- sentinel: prop was removed
        end
    end
    return changed
end

--------------------------------------------------------------------------------
-- Tree diffing
--------------------------------------------------------------------------------

local function diff(patches, old, new)
    -- Type changed → full replace.
    if old.type ~= new.type then
        patches[#patches + 1] = {op = "replace", id = old.id, node = new}
        return
    end

    -- Props changed → update.
    local changed = diff_props(old, new)
    if changed then
        patches[#patches + 1] = {op = "update", id = new.id, props = changed}
    end

    -- Diff children.
    local old_children = old.children or {}
    local new_children = new.children or {}

    -- Index old children by ID for O(1) lookup.
    local old_by_id = {}
    local old_order = {}
    for i, child in ipairs(old_children) do
        if child.id then
            old_by_id[child.id] = child
            old_order[#old_order + 1] = child.id
        end
    end

    -- Walk new children: match, insert, or recurse.
    local new_order = {}
    local new_by_id = {}
    for i, child in ipairs(new_children) do
        if child.id then
            new_by_id[child.id] = true
            new_order[#new_order + 1] = child.id

            local old_child = old_by_id[child.id]
            if old_child then
                -- Matched — recurse.
                diff(patches, old_child, child)
            else
                -- New child — insert.
                patches[#patches + 1] = {
                    op = "insert",
                    parent = new.id,
                    index = i,
                    node = child,
                }
            end
        end
    end

    -- Removals: old children not present in new.
    for _, child in ipairs(old_children) do
        if child.id and not new_by_id[child.id] then
            patches[#patches + 1] = {op = "remove", id = child.id}
        end
    end

    -- Reorder detection: compare surviving old order with new order.
    local surviving = {}
    for _, id in ipairs(old_order) do
        if new_by_id[id] then
            surviving[#surviving + 1] = id
        end
    end

    local reordered = false
    if #surviving == #new_order then
        for i = 1, #surviving do
            if surviving[i] ~= new_order[i] then
                reordered = true
                break
            end
        end
    elseif #surviving > 0 then
        -- Insertions changed the count; still check relative order.
        local j = 1
        for _, id in ipairs(new_order) do
            if old_by_id[id] then
                if j > #surviving or surviving[j] ~= id then
                    reordered = true
                    break
                end
                j = j + 1
            end
        end
    end

    if reordered then
        patches[#patches + 1] = {
            op = "reorder",
            parent = new.id,
            ids = new_order,
        }
    end
end

--------------------------------------------------------------------------------
-- Public API
--------------------------------------------------------------------------------

-- Committed trees, keyed by screen name.
local committed = {}

--- Render a screen and diff against the committed tree.
--- Returns:
---   nil              — no changes
---   {op="init", ...} — first render (single table, not an array)
---   {patch, ...}     — array of patch operations
function render_and_diff(screen_name, state)
    local screen_fn = _G[screen_name]
    if not screen_fn then return nil end

    local new_tree = screen_fn(state)
    if not new_tree then return nil end

    local old_tree = committed[screen_name]

    if not old_tree then
        -- First render — send the full tree.
        committed[screen_name] = new_tree
        return {{op = "init", node = new_tree}}
    end

    local patches = {}
    diff(patches, old_tree, new_tree)

    -- Commit the new tree for next diff.
    committed[screen_name] = new_tree

    if #patches == 0 then
        return nil  -- no changes
    end

    return patches
end

--- Reset committed state for a screen (e.g. on disconnect).
function reset_committed(screen_name)
    if screen_name then
        committed[screen_name] = nil
    else
        committed = {}
    end
end

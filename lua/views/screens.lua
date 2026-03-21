-- Copyright 2026 Marcelo Cantos
-- SPDX-License-Identifier: Apache-2.0

-- Server-driven UI view scripts for Jevon.
-- Each screen function receives a state table and returns a node tree
-- built from primitive builder functions.

--------------------------------------------------------------------------------
-- Helpers
--------------------------------------------------------------------------------

local function chat_bubble(msg)
    local is_user = msg.role == "user"
    local msg_text = text(msg.text)
    if is_user then
        msg_text = with_props(msg_text, {color = "white"})
    end
    local bubble = padding(
        background(
            padding(msg_text, 8, 12, 8, 12),
            is_user and "blue" or "gray",
            16
        ),
        2
    )
    if is_user then
        bubble = with_props(bubble, {alignment = "trailing"})
    end

    if is_user then
        return hstack(0, spacer(), bubble)
    else
        return hstack(0, bubble, spacer())
    end
end

local function status_color(status)
    if status == "running" then return "green"
    elseif status == "idle" then return "gray"
    elseif status == "error" then return "red"
    elseif status == "stopped" then return "orange"
    else return "gray"
    end
end

local function session_row(s)
    local name_text = text_styled(s.name, {weight = "medium", max_lines = 1, truncate = "middle"})
    local status_badge = badge(string.upper(s.status), status_color(s.status))

    local row_children = {
        hstack(8, name_text, status_badge)
    }

    if s.workdir ~= "" and s.workdir ~= s.name then
        local dir_text = text_styled(s.workdir, {font = "caption", color = "secondary", max_lines = 1, truncate = "middle"})
        table.insert(row_children, dir_text)
    end

    local content = vstack(4, unpack(row_children))
    local row = padding(content, 4, 0, 4, 0)

    -- Wrap with swipe-to-kill action
    local with_swipe = vstack(0,
        row,
        swipe_action("kill-" .. s.id, "Kill", "kill_session:" .. s.id, "destructive")
    )

    return tap("session-" .. s.id, "session_detail:" .. s.id, with_swipe)
end

--------------------------------------------------------------------------------
-- Connect Screen
--------------------------------------------------------------------------------

function connect_screen(state)
    local status_text
    if state.connected then
        status_text = text_styled("Connected", {color = "green"})
    else
        status_text = text_styled("Disconnected", {color = "red"})
    end

    local body = vstack(16,
        spacer(),
        with_props(image_sf("terminal"), {font = "title", color = "secondary"}),
        text_styled("Connect to Jevon", {font = "title2", weight = "bold"}),
        status_text,
        text_field("host-input", "Host", "connect"),
        text_field("port-input", "Port", "connect"),
        button("connect-btn", "Connect", "connect"),
        spacer()
    )

    return nav("Jevon", nil, padding(body, 16))
end

--------------------------------------------------------------------------------
-- Chat Screen
--------------------------------------------------------------------------------

function chat_screen(state)
    -- Build message list
    local msg_nodes = {}
    if state.messages then
        for i = 1, #state.messages do
            table.insert(msg_nodes, chat_bubble(state.messages[i]))
        end
    end

    -- Append streaming text as a partial jevon bubble
    if state.streaming_text and state.streaming_text ~= "" then
        table.insert(msg_nodes, chat_bubble({role = "jevon", text = state.streaming_text}))
    end

    -- Thinking status bar
    local thinking_bar = nil
    if state.status == "thinking" then
        thinking_bar = padding(
            hstack(8, progress(), text_styled("Thinking...", {font = "caption", color = "secondary"})),
            4
        )
    end

    -- Input bar — extra bottom padding clears the QuickType autocomplete bar,
    -- which SwiftUI's keyboard safe area doesn't account for.
    local input_bar = padding(
        with_props(
            text_field("message-input", "Message", "send_message"),
            {submit_label = "send", autocapitalize = "sentences"}
        ),
        8, 16, 32, 16
    )

    local messages_scroll = with_props(
        scroll("messages", vstack(8, unpack(msg_nodes))),
        {scroll_anchor = "bottom", scroll_dismiss_keyboard = "interactive",
         frame_max_width = "infinity", frame_max_height = "infinity"}
    )

    -- Simple VStack: scroll expands, input bar sits at the bottom.
    -- SwiftUI's automatic keyboard avoidance pushes everything up.
    local body
    if thinking_bar then
        body = vstack(0, messages_scroll, thinking_bar, input_bar)
    else
        body = vstack(0, messages_scroll, input_bar)
    end

    -- Toolbar
    local tb = toolbar(
        {icon_button("sessions-btn", "list.bullet", "show_sessions")},
        {icon_button("mic-btn", "mic.fill", "toggle_voice")}
    )

    return nav("Jevon", tb, body)
end

--------------------------------------------------------------------------------
-- Sessions Screen (shown as sheet)
--------------------------------------------------------------------------------

function sessions_screen(state)
    local tb = toolbar(
        {},
        {button("done-btn", "Done", "dismiss_sheet")}
    )

    local body
    if not state.sessions or #state.sessions == 0 then
        body = vstack(16,
            spacer(),
            with_props(image_sf("terminal"), {font = "title", color = "secondary"}),
            text_styled("No Sessions", {font = "title2", color = "secondary"}),
            spacer()
        )
    else
        local rows = {}
        for i = 1, #state.sessions do
            table.insert(rows, session_row(state.sessions[i]))
        end
        body = list("sessions-list", unpack(rows))
    end

    return nav("Sessions", tb, body)
end

--------------------------------------------------------------------------------
-- Session Detail Screen (shown as sheet)
--------------------------------------------------------------------------------

function session_detail_screen(state)
    local tb = toolbar(
        {},
        {button("detail-done-btn", "Done", "dismiss_sheet")}
    )

    local detail = state.detail
    if not detail then
        return nav("Session Detail", tb,
            vstack(16,
                spacer(),
                text_styled("No session selected", {color = "secondary"}),
                spacer()
            )
        )
    end

    local rows = {
        vstack(2,
            text_styled("Name", {font = "caption", color = "secondary"}),
            text_styled(detail.name or "", {weight = "medium"})
        ),
        vstack(2,
            text_styled("Status", {font = "caption", color = "secondary"}),
            badge(string.upper(detail.status or "unknown"), status_color(detail.status or "unknown"))
        ),
        vstack(2,
            text_styled("Directory", {font = "caption", color = "secondary"}),
            text_styled(detail.workdir or detail.name or "", {font = "caption", color = "secondary"})
        )
    }

    if detail.last_result and detail.last_result ~= "" then
        table.insert(rows, vstack(2,
            text_styled("Last Result", {font = "caption", color = "secondary"}),
            padding(
                background(
                    padding(
                        text_styled(detail.last_result, {font = "monospaced"}),
                        8
                    ),
                    "gray6",
                    8
                ),
                4, 0, 4, 0
            )
        ))
    end

    local body = scroll("detail-scroll", padding(vstack(16, unpack(rows)), 16))

    return nav("Session Detail", tb, body)
end

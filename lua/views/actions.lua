-- Copyright 2026 Marcelo Cantos
-- SPDX-License-Identifier: Apache-2.0

-- Action handlers for Jevon server-driven UI.
-- Each action dispatched from the client is routed through handle_action.
-- Go capability functions (jevon_enqueue, session_kill, push_sessions, etc.)
-- are registered by the server before this script loads.

function handle_action(action, value)
    if action == "send_message" then
        jevon_enqueue(value)
    elseif action == "show_sessions" then
        push_sessions()
    elseif action == "dismiss_sheet" then
        -- Handled client-side when using client-side Lua.
    elseif action == "disconnect" then
        -- Handled by connection layer.
    elseif action:sub(1, 13) == "kill_session:" then
        local id = action:sub(14)
        local err = session_kill(id)
        if not err then
            push_sessions()
        end
    elseif action == "reload_views" then
        push_scripts()
    elseif action == "reset_session" then
        db_set("jevon_claude_id", "")
    elseif action == "rewind_session" then
        local claude_id = db_get("jevon_claude_id")
        if claude_id ~= "" then
            local err = transcript_truncate(claude_id, 0)
            if err then
                notify("Rewind Failed", err)
            else
                db_set("jevon_claude_id", "")
                notify("Session Rewound", "Jevon will start fresh on next message.")
            end
        end
    end
end

local obs = obslua

local browser_source_name = ""
local user_token = ""
local hotkey_skip_id = obs.OBS_INVALID_HOTKEY_ID
local hotkey_show_image_id = obs.OBS_INVALID_HOTKEY_ID

local OBS_KEY_STRING = "OBS_KEY_"

local KEY_SKIP = OBS_KEY_STRING .. "2"
local KEY_SHOW_IMAGE = OBS_KEY_STRING .. "3"

local TIMER_INTERVAL_MS = 3000
local BURST_INTERVAL_MS = 10
local periodic_started = false

local START_KEY = OBS_KEY_STRING .. "0"
local END_KEY = OBS_KEY_STRING .. "1"

local function send_key_event_to_browser(key)
    if browser_source_name == "" then
        obs.script_log(obs.LOG_WARNING, "No browser source selected.")
        return
    end

    local src = obs.obs_get_source_by_name(browser_source_name)
    if src == nil then
        obs.script_log(obs.LOG_WARNING, "Browser source not found: " .. browser_source_name)
        return
    end

    local key_name = key:upper()
    local obs_key = obs.obs_key_from_name(key_name)
    if obs_key == obs.OBS_KEY_NONE then
        obs.script_log(obs.LOG_ERROR, "Invalid key: " .. key)
        obs.obs_source_release(src)
        return
    end

    local vk = obs.obs_key_to_virtual_key(obs_key)
    local event = obs.obs_key_event()
    event.native_vkey = vk
    event.native_scancode = vk
    event.native_modifiers = 0
    event.modifiers = 0
    event.text = ""

    obs.obs_source_send_key_click(src, event, false)
    obs.obs_source_send_key_click(src, event, true)

    -- obs.script_log(obs.LOG_INFO,
    --     string.format("Sent key '%s' to browser source '%s'", key, browser_source_name))

    obs.obs_source_release(src)
end

local function on_hotkey_skip(pressed)
    if pressed then
        send_key_event_to_browser(KEY_SKIP)
    end
end

local function on_hotkey_show_image(pressed)
    if pressed then
        send_key_event_to_browser(KEY_SHOW_IMAGE)
    end
end

local burst_index = 1
local function send_burst()
    if burst_index == 1 then
        -- obs.script_log(obs.LOG_DEBUG, "Burst start")
        send_key_event_to_browser(START_KEY)
    end

    if burst_index <= #user_token then
        send_key_event_to_browser(OBS_KEY_STRING .. string.sub(user_token, burst_index, burst_index))
        burst_index = burst_index + 1
    elseif burst_index == #user_token + 1 then
        send_key_event_to_browser(END_KEY)
        burst_index = burst_index + 1
    else
        burst_index = 1
        obs.timer_remove(send_burst)
    end
end

local function periodic_task()
    burst_index = 1
    obs.timer_add(send_burst, BURST_INTERVAL_MS)
end

local function periodic_tick()
    periodic_task()
end

function script_load(settings)
    hotkey_skip_id = obs.obs_hotkey_register_frontend(
        "baj_ai_skip",
        "BAJ_AI_SKIP",
        on_hotkey_skip
    )
    local hotkey_skip_array = obs.obs_data_get_array(settings, "baj_ai_skip")
    obs.obs_hotkey_load(hotkey_skip_id, hotkey_skip_array)
    obs.obs_data_array_release(hotkey_skip_array)

    hotkey_show_image_id = obs.obs_hotkey_register_frontend(
        "baj_ai_show_image",
        "BAJ_AI_SHOW_IMAGE",
        on_hotkey_show_image
    )
    local hotkey_show_array = obs.obs_data_get_array(settings, "baj_ai_show_image")
    obs.obs_hotkey_load(hotkey_show_image_id, hotkey_show_array)
    obs.obs_data_array_release(hotkey_show_array)

    periodic_task()
	if not periodic_started then
		obs.timer_add(periodic_tick, TIMER_INTERVAL_MS)
		periodic_started = true
	end
end

function script_save(settings)
    local hotkey_skip_array = obs.obs_hotkey_save(hotkey_skip_id)
    obs.obs_data_set_array(settings, "baj_ai_skip", hotkey_skip_array)
    obs.obs_data_array_release(hotkey_skip_array)

    local hotkey_show_array = obs.obs_hotkey_save(hotkey_show_image_id)
    obs.obs_data_set_array(settings, "baj_ai_show_image", hotkey_show_array)
    obs.obs_data_array_release(hotkey_show_array)
end

function script_unload()
	if periodic_started then
		obs.timer_remove(periodic_tick)
		periodic_started = false
	end
end

function script_properties()
    local props = obs.obs_properties_create()

    local list = obs.obs_properties_add_list(
        props,
        "browser_source_name",
        "Browser Source",
        obs.OBS_COMBO_TYPE_LIST,
        obs.OBS_COMBO_FORMAT_STRING
    )

    local sources = obs.obs_enum_sources()
    if sources ~= nil then
        for _, src in ipairs(sources) do
            local id = obs.obs_source_get_unversioned_id(src)
            if id == "browser_source" then
                local name = obs.obs_source_get_name(src)
                obs.obs_property_list_add_string(list, name, name)
            end
        end
        obs.source_list_release(sources)
    end

    obs.obs_properties_add_text(props, "user_token", "Token", obs.OBS_TEXT_DEFAULT)

    return props
end

function script_update(settings)
    browser_source_name = obs.obs_data_get_string(settings, "browser_source_name")
    user_token = obs.obs_data_get_string(settings, "user_token")
end

function script_description()
    return [[
<h2>BAJ AI Hotkeys to skip requests and approve images</h2>
<p>
Assign two global hotkeys in OBS Settings → Hotkeys:<br/>
<b>BAJ_AI_SKIP</b><br/>
<b>BAJ_AI_SHOW_IMAGE</b><br/><br/>
Assign your desired keybinds to the hotkeys.<br/>
Select a Browser Source that you use for BAJ AI in the dropdown.<br/>
Get token from settings page of BAJ AI and paste it in the Token field.
</p>
]]
end

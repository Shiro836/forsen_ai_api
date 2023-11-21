package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"app/ai"
	"app/conns"
	"app/db"
	"app/slg"
	"app/tts"
	"app/twitch"

	"github.com/go-audio/wav"
	lua "github.com/yuin/gopher-lua"
)

var DefaultLuaScript = `
function startswith(text, prefix)
	return text:find(prefix, 1, true) == 1
end

while true do
	user, msg, reward_id = get_next_event()
	if startswith(msg, "!tts ") then
		tts(string.sub(msg, 6, #msg))
	elseif startswith(msg, "!ask ") then
		local request = string.sub(msg, 6, #msg)
		local ai_resp = ai("PROMPT: " .. request .. " FORSEN: ")
		tts(user .. " asked me: " .. request)
		tts(ai_resp)
	end
end
`

type LuaConfig struct {
	MaxScriptExecTime time.Duration  `yaml:"max_script_exec_time"`
	MaxFuncCalls      map[string]int `yaml:"max_func_calls"`
}

type Processor struct {
	luaCfg *LuaConfig

	ai  *ai.Client
	tts *tts.Client
}

func NewProcessor(luacfg *LuaConfig, ai *ai.Client, tts *tts.Client) *Processor {
	return &Processor{
		luaCfg: luacfg,

		ai:  ai,
		tts: tts,
	}
}

func sleepForAudioLen(wavData []byte) {
	reader := bytes.NewReader(wavData)

	d := wav.NewDecoder(reader)
	if d == nil {
		panic("error opening wav data")
	}

	duration, err := d.Duration()
	if err != nil {
		slog.Error("getting duration err", "err", err)
	}
	slog.Info(fmt.Sprintf("sleeping for %s", duration.String()))
	time.Sleep(duration)
}

func (p *Processor) Process(ctx context.Context, updates chan struct{}, eventWriter conns.EventWriter, user string) error {
	ctx, cancel := context.WithCancel(ctx)

	defer func() {
		cancel()
		if r := recover(); r != nil {
			slg.GetSlog(ctx).Error("connection panic", "user", user, "r", r, "stack", string(debug.Stack()))
		}
	}()

	go func() {
		<-updates
		slg.GetSlog(ctx).Info("processor signal recieved")
		cancel()
	}()

	settings, err := db.GetDbSettings(user)
	if err != nil {
		slg.GetSlog(ctx).Info("settings not found, defaulting")
		settings = &db.Settings{
			LuaScript: DefaultLuaScript,
		}
	}

	slg.GetSlog(ctx).Info("Settings fetched", "settings", settings)

	luaState := lua.NewState(lua.Options{
		SkipOpenLibs:        true,
		IncludeGoStackTrace: true,
	})

	luaState.SetGlobal("channel", lua.LString(user))

	for _, pair := range []struct {
		n string
		f lua.LGFunction
	}{
		{lua.LoadLibName, lua.OpenPackage}, // Must be first
		{lua.BaseLibName, lua.OpenBase},
		{lua.TabLibName, lua.OpenTable},
		{lua.StringLibName, lua.OpenString},
	} {
		if err := luaState.CallByParam(lua.P{
			Fn:      luaState.NewFunction(pair.f),
			NRet:    0,
			Protect: true,
		}, lua.LString(pair.n)); err != nil {
			panic(err)
		}
	}

	twitchChatCh := twitch.MessagesFetcher(ctx, user)

	luaState.SetGlobal("ai", luaState.NewFunction(func(l *lua.LState) int {
		request := l.Get(1).String()

		aiResponse, err := p.ai.Ask(ctx, 0, request)
		if err != nil {
			l.Push(lua.LString("ai request error: " + err.Error()))
			return 1
		}

		l.Push(lua.LString(aiResponse))
		return 1
	}))

	luaState.SetGlobal("text", luaState.NewFunction(func(l *lua.LState) int {
		request := l.Get(1).String()

		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeText,
			EventData: []byte(request),
		})

		return 0
	}))

	luaState.SetGlobal("tts_no_sleep", luaState.NewFunction(func(l *lua.LState) int {
		request := l.Get(1).String()

		ttsResponse, err := p.tts.TTS(ctx, request, nil)
		if err != nil {
			l.Push(lua.LString("tts request error: " + err.Error()))
			return 1
		}

		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeAudio,
			EventData: ttsResponse,
		})

		return 0
	}))

	luaState.SetGlobal("tts", luaState.NewFunction(func(l *lua.LState) int {
		request := l.Get(1).String()

		ttsResponse, err := p.tts.TTS(ctx, request, nil)
		if err != nil {
			l.Push(lua.LString("tts request error: " + err.Error()))
			return 1
		}

		if eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeAudio,
			EventData: ttsResponse,
		}) {
			sleepForAudioLen(ttsResponse)
		}

		return 0
	}))

	luaState.SetGlobal("get_next_event", luaState.NewFunction(func(l *lua.LState) int {
		select {
		case msg := <-twitchChatCh:
			slg.GetSlog(ctx).Info("pushing", "msg", msg)
			l.Push(lua.LString(msg.UserName))
			l.Push(lua.LString(msg.Message))
			l.Push(lua.LString(msg.CustomRewardID))
		case <-ctx.Done():
			luaState.Close()
			return 0
		}

		return 3
	}))

	if err := luaState.DoString(settings.LuaScript); err != nil {
		return fmt.Errorf("lua execution err: %w", err)
	}

	slg.GetSlog(ctx).Info("processor is closing")

	return nil
}

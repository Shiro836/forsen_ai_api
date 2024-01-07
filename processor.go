package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"app/ai"
	"app/api"
	"app/conns"
	"app/db"
	"app/rvc"
	"app/slg"
	"app/tts"
	"app/twitch"

	"github.com/go-audio/wav"
	lua "github.com/yuin/gopher-lua"
)

type LuaConfig struct {
	MaxScriptExecTime time.Duration  `yaml:"max_script_exec_time"`
	MaxFuncCalls      map[string]int `yaml:"max_func_calls"`
}

type Processor struct {
	luaCfg *LuaConfig

	rvc *rvc.Client
	ai  *ai.Client
	tts *tts.Client
}

func NewProcessor(luaCfg *LuaConfig, ai *ai.Client, tts *tts.Client, rvc *rvc.Client) *Processor {
	return &Processor{
		luaCfg: luaCfg,

		ai:  ai,
		tts: tts,
		rvc: rvc,
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

func (p *Processor) Process(ctx context.Context, updates chan struct{}, eventWriter conns.EventWriter, user string) (err error) {
	ctx, cancel := context.WithCancel(ctx)

	eventWriter(&conns.DataEvent{
		EventType: conns.EventTypeText,
		EventData: []byte("start"),
	})

	defer func() {
		cancel()
		if r := recover(); r != nil {
			stack := string(debug.Stack())

			err = fmt.Errorf("paniced in Process: %s", stack)
			slg.GetSlog(ctx).Error("connection panic", "user", user, "r", r, "stack", stack)
		}
	}()

	go func() {
		<-updates
		slg.GetSlog(ctx).Info("processor signal recieved")
		cancel()
	}()

	//charNameToCard := make(map[string][]byte, 10)
	twitchRewardIDToRewardID := make(map[string]string, 5)

	userData, err := db.GetUserData(user)
	if err != nil {
		slg.GetSlog(ctx).Info("no user data found", "err", err)
	} else {
		rewards, err := db.GetRewards(userData.ID)
		if err == nil {
			for _, reward := range rewards {
				twitchRewardIDToRewardID[reward.TwitchRewardID] = reward.RewardID
			}
		} else {
			slg.GetSlog(ctx).Info("failed to get rewards", "err", err)
		}

		// cards, err := db.GetAllCards(userData.ID)
		// if err == nil {
		// 	for _, card := range cards {
		// 		charNameToCard[card.CharName] = card.Card
		// 	}
		// } else {
		// 	slg.GetSlog(ctx).Info("failed to get char cards", "err", err)
		// }
	}

	slg.GetSlog(ctx).Info("got from db",
		"rewards", twitchRewardIDToRewardID,
		// "cards", maps.Keys(charNameToCard),
	)

	settings, err := db.GetDbSettings(user)
	if err != nil {
		slg.GetSlog(ctx).Info("settings not found, defaulting")
		settings = &db.Settings{
			LuaScript: api.DefaultLuaScript,
		}
	}
	if len(settings.LuaScript) == 0 {
		settings.LuaScript = api.DefaultLuaScript
	}

	slg.GetSlog(ctx).Info("Settings fetched", "settings", settings)

	luaState := lua.NewState(lua.Options{
		SkipOpenLibs:        true,
		IncludeGoStackTrace: true,
	})

	closeOnce := sync.Once{}

	defer func() {
		closeOnce.Do(func() {
			luaState.Close()
		})
	}()

	go func() {
		<-ctx.Done()
		closeOnce.Do(func() {
			luaState.Close()
		})
	}()

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

	luaState.SetGlobal("broadcaster", lua.LString(user))
	luaState.SetGlobal("get_char_card", luaGetCharCard(ctx, luaState))
	// luaState.SetGlobal("get_char_cards", luaGetAllCharCards(ctx, luaState, charNameToCard))
	luaState.SetGlobal("ai", p.luaAi(ctx, luaState))
	luaState.SetGlobal("text", luaText(luaState, eventWriter))
	luaState.SetGlobal("tts", p.luaTts(ctx, luaState, eventWriter))
	luaState.SetGlobal("get_next_event", luaGetNextEvent(ctx, luaState, twitchChatCh, twitchRewardIDToRewardID))
	luaState.SetGlobal("set_model", luaSetModel(ctx, luaState, eventWriter))
	luaState.SetGlobal("set_image", luaSetImage(ctx, luaState, eventWriter))
	luaState.SetGlobal("filter_text", luaFilter(ctx, luaState, userData))
	luaState.SetGlobal("get_custom_chars", luaGetCustomChars(ctx, luaState, userData))

	if err := luaState.DoString(settings.LuaScript); err != nil {
		if ctx.Err() != nil {
			eventWriter(&conns.DataEvent{
				EventType: conns.EventTypeText,
				EventData: []byte("end"),
			})

			return nil
		}

		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeText,
			EventData: []byte("lua exec err: " + err.Error()),
		})

		return fmt.Errorf("lua execution err: %w", err)
	}

	slg.GetSlog(ctx).Info("processor is closing")

	return nil
}

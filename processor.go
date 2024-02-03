package main

import (
	"context"
	"fmt"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"app/ai"
	"app/api"
	"app/conns"
	"app/db"
	"app/rvc"
	"app/slg"
	"app/tools"
	"app/tts"
	"app/twitch"

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

	skippedMsgs     map[int]struct{}
	skippedMsgsLock sync.RWMutex
}

func NewProcessor(luaCfg *LuaConfig, ai *ai.Client, tts *tts.Client, rvc *rvc.Client) *Processor {
	return &Processor{
		luaCfg: luaCfg,

		ai:  ai,
		tts: tts,
		rvc: rvc,

		skippedMsgs: make(map[int]struct{}, 20),
	}
}

// func sleepForAudioLen(wavData []byte) {
// 	reader := bytes.NewReader(wavData)

// 	d := wav.NewDecoder(reader)
// 	if d == nil {
// 		slog.Error("error opening wav data")
// 	}

// 	duration, err := d.Duration()
// 	if err != nil {
// 		slog.Error("getting duration err", "err", err)
// 	}
// 	slog.Info(fmt.Sprintf("sleeping for %s", duration.String()))
// 	time.Sleep(duration)
// }

func (p *Processor) Process(ctx context.Context, updates chan *conns.Update, eventWriter conns.EventWriter, user string) (err error) {
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
	loop:
		for {
			select {
			case upd, ok := <-updates:
				if !ok {
					updates = nil
					cancel()
				}

				slg.GetSlog(ctx).Info("processor signal recieved", "upd_signal", upd)
				switch upd.UpdateType {
				case conns.RestartProcessor:
					cancel()
					break loop
				case conns.SkipMessage:
					msgID, err := strconv.Atoi(upd.Data)
					if err != nil {
						slg.GetSlog(ctx).Error("msg id is not integer", "err", err)
					}

					func() {
						p.skippedMsgsLock.Lock()
						defer p.skippedMsgsLock.Unlock()

						p.skippedMsgs[msgID] = struct{}{}
					}()

					eventWriter(&conns.DataEvent{
						EventType: conns.EventTypeSkip,
						EventData: []byte(upd.Data),
					})
				}
			case <-ctx.Done():
				break loop
			}
		}
	}()

	userData, err := db.GetUserData(user)
	if err != nil {
		slg.GetSlog(ctx).Info("no user data found", "err", err)
		time.Sleep(4 * time.Second)
		return
	}

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

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()

		twitchChatCh := twitch.MessagesFetcher(ctx, user)

		for msg := range twitchChatCh {
			if len(msg.Message) == 0 || len(msg.UserName) == 0 {
				continue
			}

			if err := db.PushMsg(userData.UserLoginData.UserId, &db.Message{
				UserName:       msg.UserName,
				Message:        msg.Message,
				CustomRewardID: msg.CustomRewardID,

				State: tools.Wait.String(),
			}); err != nil {
				slg.GetSlog(ctx).Error("failed to insert msg", "err", err)
			}
		}
	}()

	luaState.SetGlobal("broadcaster", lua.LString(user))
	luaState.SetGlobal("get_char_card", luaGetCharCard(ctx, luaState))
	luaState.SetGlobal("ai", p.luaAi(ctx, luaState))
	luaState.SetGlobal("text", p.luaText(luaState, eventWriter))
	luaState.SetGlobal("tts", p.luaTts(ctx, luaState, eventWriter))
	luaState.SetGlobal("get_next_event", p.luaGetNextEvent(ctx, luaState, userData.UserLoginData.UserId))
	luaState.SetGlobal("set_model", p.luaSetModel(ctx, luaState, eventWriter))
	luaState.SetGlobal("set_image", p.luaSetImage(ctx, luaState, eventWriter))
	luaState.SetGlobal("filter_text", luaFilter(ctx, luaState, userData))
	luaState.SetGlobal("tts_text", p.luaTtsWithText(ctx, luaState, eventWriter))
	// luaState.SetGlobal("get_char_cards", luaGetAllCharCards(ctx, luaState, charNameToCard))
	// luaState.SetGlobal("get_custom_chars", luaGetCustomChars(ctx, luaState, userData))

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

	wg.Wait()

	return nil
}

func (p *Processor) ToSkipMsg(msgID int) bool {
	p.skippedMsgsLock.RLock()
	defer p.skippedMsgsLock.RUnlock()

	_, ok := p.skippedMsgs[msgID]

	return ok
}

package main

import (
	"app/char"
	"app/conns"
	"app/db"
	"app/slg"
	"app/twitch"
	"context"

	lua "github.com/yuin/gopher-lua"
)

func luaGetCharCard(ctx context.Context, luaState *lua.LState) *lua.LFunction {
	return luaState.NewFunction(func(l *lua.LState) int {
		charName := l.Get(1).String()
		if card, err := db.GetCharCard(charName); err != nil {
			slg.GetSlog(ctx).Error("failed to get card", "err", err)
			l.Push(lua.LString("failed to get card: " + err.Error()))
			return 1
		} else if parsedCard, err := char.FromPngSillyTavernCard(card.Card); err != nil {
			slg.GetSlog(ctx).Error("failed to parse card", "err", err)
			l.Push(lua.LString("failed to parse card: " + err.Error()))
			return 1
		} else {
			cardTbl := l.NewTable()

			cardTbl.RawSetString("name", lua.LString(parsedCard.Name))
			cardTbl.RawSetString("description", lua.LString(parsedCard.Description))
			cardTbl.RawSetString("personality", lua.LString(parsedCard.Personality))
			cardTbl.RawSetString("first_message", lua.LString(parsedCard.FirstMessage))
			cardTbl.RawSetString("message_example", lua.LString(parsedCard.MessageExample))
			cardTbl.RawSetString("scenario", lua.LString(parsedCard.Scenario))
			cardTbl.RawSetString("system_prompt", lua.LString(parsedCard.SystemPrompt))
			cardTbl.RawSetString("post_history_instructions", lua.LString(parsedCard.PostHistoryInstructions))

			l.Push(cardTbl)

			return 1
		}
	})
}

func luaGetAllCharCards(ctx context.Context, luaState *lua.LState, charNameToCard map[string][]byte) *lua.LFunction {
	return luaState.NewFunction(func(l *lua.LState) int {
		arrTbl := l.NewTable()

		for charName, card := range charNameToCard {
			parsedCard, err := char.FromPngSillyTavernCard(card)
			if err != nil {
				slg.GetSlog(ctx).Error("failed to parse card", "err", err)
				continue
			}

			cardTbl := l.NewTable()

			cardTbl.RawSetString("name", lua.LString(parsedCard.Name))
			cardTbl.RawSetString("description", lua.LString(parsedCard.Description))
			cardTbl.RawSetString("personality", lua.LString(parsedCard.Personality))
			cardTbl.RawSetString("first_message", lua.LString(parsedCard.FirstMessage))
			cardTbl.RawSetString("message_example", lua.LString(parsedCard.MessageExample))
			cardTbl.RawSetString("scenario", lua.LString(parsedCard.Scenario))
			cardTbl.RawSetString("system_prompt", lua.LString(parsedCard.SystemPrompt))
			cardTbl.RawSetString("post_history_instructions", lua.LString(parsedCard.PostHistoryInstructions))

			arrTbl.RawSetString(charName, cardTbl)
		}

		l.Push(arrTbl)

		return 1
	})
}

func (p *Processor) luaAi(ctx context.Context, luaState *lua.LState) *lua.LFunction {
	return luaState.NewFunction(func(l *lua.LState) int {
		request := l.Get(1).String()

		aiResponse, err := p.ai.Ask(ctx, 0, request)
		if err != nil {
			l.Push(lua.LString("ai request error: " + err.Error()))
			return 1
		}

		l.Push(lua.LString(aiResponse))
		return 1
	})
}

func luaText(luaState *lua.LState, eventWriter conns.EventWriter) *lua.LFunction {
	return luaState.NewFunction(func(l *lua.LState) int {
		request := l.Get(1).String()

		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeText,
			EventData: []byte(request),
		})

		return 0
	})
}

func (p *Processor) rvcVoice(ttsResponse []byte, voice string) ([]byte, error) {
	switch voice {
	case "megumin":
		return p.rvc.Rvc(context.Background(), "megumin", ttsResponse)
	default:
		return ttsResponse, nil
	}
}

func (p *Processor) luaTts(ctx context.Context, luaState *lua.LState, eventWriter conns.EventWriter) *lua.LFunction {
	return luaState.NewFunction(func(l *lua.LState) int {
		voice := l.Get(1).String()
		request := l.Get(2).String()

		if voiceFile, err := db.GetVoice(voice); err != nil {
			eventWriter(&conns.DataEvent{
				EventType: conns.EventTypeText,
				EventData: []byte("failed to get voice from db: " + err.Error()),
			})
		} else if ttsResponse, err := p.tts.TTS(ctx, request, voiceFile); err != nil {
			eventWriter(&conns.DataEvent{
				EventType: conns.EventTypeText,
				EventData: []byte("failed to tts: " + err.Error()),
			})
		} else if ttsResponse, err = p.rvcVoice(ttsResponse, voice); err != nil {
			eventWriter(&conns.DataEvent{
				EventType: conns.EventTypeText,
				EventData: []byte("failed to rvc: " + err.Error()),
			})
		} else if eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeAudio,
			EventData: ttsResponse,
		}) {
			sleepForAudioLen(ttsResponse)
		}

		return 0
	})
}

func luaGetNextEvent(ctx context.Context, luaState *lua.LState, twitchChatCh chan *twitch.ChatMessage, twitchRewardIDToRewardID map[string]string) *lua.LFunction {
	return luaState.NewFunction(func(l *lua.LState) int {
		select {
		case msg := <-twitchChatCh:
			slg.GetSlog(ctx).Info("recieved", "msg", msg)
			if len(msg.CustomRewardID) != 0 {
				if rewardID, ok := twitchRewardIDToRewardID[msg.CustomRewardID]; ok {
					slg.GetSlog(ctx).Info("converted reward", "new_reward", rewardID)

					l.Push(lua.LString(msg.UserName))
					l.Push(lua.LString(msg.Message))
					l.Push(lua.LString(rewardID))

					return 3
				}
			} else {
				l.Push(lua.LString(msg.UserName))
				l.Push(lua.LString(msg.Message))
				l.Push(lua.LString(msg.CustomRewardID))

				return 3
			}
		case <-ctx.Done():
			return 0
		}

		return 0
	})
}

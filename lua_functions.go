package main

import (
	"app/char"
	"app/conns"
	"app/db"
	"app/metrics"
	"app/slg"
	"app/swearfilter"
	"app/tools"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	lua "github.com/yuin/gopher-lua"
)

func luaGetCharCard(ctx context.Context, luaState *lua.LState) *lua.LFunction {
	return luaState.NewFunction(func(l *lua.LState) int {
		charName := l.Get(1).String()
		if card, err := db.GetCharCard(charName); err != nil {
			slg.GetSlog(ctx).Error("failed to get card", "err", err)
			return 0
		} else if parsedCard, err := char.TryParse(card.Card); err != nil {
			slg.GetSlog(ctx).Error("failed to parse card", "err", err)
			return 0
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

func (p *Processor) luaAi(ctx context.Context, luaState *lua.LState, userName string) *lua.LFunction {
	return luaState.NewFunction(func(l *lua.LState) int {
		request := l.Get(1).String()

		aiResponse, err := p.ai.Ask(ctx, request)
		if err != nil {
			l.Push(lua.LString("ai request error: " + err.Error()))
			return 1
		}

		metrics.AIUserRequests.WithLabelValues(userName).Inc()

		l.Push(lua.LString(aiResponse))
		return 1
	})
}

func (p *Processor) luaText(luaState *lua.LState, eventWriter conns.EventWriter) *lua.LFunction {
	return luaState.NewFunction(func(l *lua.LState) int {
		msgID := int(lua.LVAsNumber(l.Get(1)))
		request := l.Get(2).String()

		if p.ToSkipMsg(msgID) {
			return 0
		}

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
		return p.rvc.Rvc(context.Background(), "megumin", ttsResponse, 3)
	case "gura":
		return p.rvc.Rvc(context.Background(), "gura", ttsResponse, 1)
	case "adolf2":
		return p.rvc.Rvc(context.Background(), "adolf", ttsResponse, 2)
	default:
		return ttsResponse, nil
	}
}

type audioEvent struct {
	AudioBase64 string `json:"audio"`
	MsgID       string `json:"msg_id"`
}

func (p *Processor) luaTts(ctx context.Context, luaState *lua.LState, eventWriter conns.EventWriter) *lua.LFunction {
	return luaState.NewFunction(func(l *lua.LState) int {
		msgID := int(lua.LVAsNumber(l.Get(1)))
		voice := l.Get(2).String()
		request := l.Get(3).String()

		if p.ToSkipMsg(msgID) {
			return 0
		}

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
		} else if ttsResponse.Audio, err = p.rvcVoice(ttsResponse.Audio, voice); err != nil {
			eventWriter(&conns.DataEvent{
				EventType: conns.EventTypeText,
				EventData: []byte("failed to rvc: " + err.Error()),
			})
		} else if data, err := json.Marshal(&audioEvent{AudioBase64: base64.StdEncoding.EncodeToString(ttsResponse.Audio), MsgID: strconv.Itoa(msgID)}); err != nil {
			eventWriter(&conns.DataEvent{
				EventType: conns.EventTypeText,
				EventData: []byte("failed to marshal json: " + err.Error()),
			})
		} else if eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeAudio,
			EventData: data,
		}) {
			time.Sleep(time.Millisecond * time.Duration(ttsResponse.AudioLen))
		}

		return 0
	})
}

func (p *Processor) luaGetNextEvent(ctx context.Context, luaState *lua.LState, userID int) *lua.LFunction {
	return luaState.NewFunction(func(l *lua.LState) int {
		for {
			select {
			case <-ctx.Done():
				return 0
			default:
			}

			msg, err := db.GetNextMsg(userID, tools.Wait.String())
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					time.Sleep(300 * time.Millisecond)
					continue
				}
				slg.GetSlog(ctx).Error("failed to get next msg", "err", err)
				return 0
			}

			func() {
				p.skippedMsgsLock.Lock()
				defer p.skippedMsgsLock.Unlock()

				for k := range p.skippedMsgs {
					delete(p.skippedMsgs, k)
				}
			}()

			if err := db.UpdateStatesWhere(userID, tools.Processed.String(), tools.Current.String()); err != nil {
				slg.GetSlog(ctx).Error("failed to set current state messages to processed", "err", err)
			}

			defer func() {
				if err := db.UpdateState(msg.ID, tools.Current.String()); err != nil {
					slg.GetSlog(ctx).Error("failed to update state", "err", err)
				}
			}()

			var rewardID string
			if len(msg.CustomRewardID) != 0 {
				if rewardID, err = db.GetRewardIDFromTwitchRewardID(msg.CustomRewardID); err != nil {
					slg.GetSlog(ctx).Error("failed to convert reward id", "err", err)
				}
			}

			l.Push(lua.LNumber(msg.ID))
			l.Push(lua.LString(msg.UserName))
			l.Push(lua.LString(msg.Message))
			l.Push(lua.LString(rewardID))

			return 4
		}
	})
}

func (p *Processor) luaSetModel(ctx context.Context, luaState *lua.LState, eventWriter conns.EventWriter) *lua.LFunction {
	return luaState.NewFunction(func(l *lua.LState) int {
		msgID := int(lua.LVAsNumber(l.Get(1)))
		model := l.Get(2).String()

		if p.ToSkipMsg(msgID) {
			return 0
		}

		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeSetModel,
			EventData: []byte(model),
		})

		return 0
	})
}

func (p *Processor) luaSetImage(ctx context.Context, luaState *lua.LState, eventWriter conns.EventWriter) *lua.LFunction {
	return luaState.NewFunction(func(l *lua.LState) int {
		msgID := int(lua.LVAsNumber(l.Get(1)))
		imageUrl := l.Get(2).String()

		if p.ToSkipMsg(msgID) {
			return 0
		}

		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeImage,
			EventData: []byte(imageUrl),
		})

		return 0
	})
}

func luaFilter(ctx context.Context, luaState *lua.LState, userData *db.UserData) *lua.LFunction {
	return luaState.NewFunction(func(l *lua.LState) int {
		request := l.Get(1).String()

		if userData == nil {
			l.Push(lua.LString(request))
			return 1
		}

		swears := make([]string, len(swearfilter.Swears))
		copy(swears, swearfilter.Swears)

		filters, err := db.GetFilters(userData.ID)
		if err == nil {
			swears = append(swears, strings.Split(filters, ",")...)
		}

		swearFilterObj := swearfilter.NewSwearFilter(false, swears...)

		filtered := request

		tripped, _ := swearFilterObj.Check(request)
		for _, word := range tripped {
			filtered = IReplace(filtered, word, strings.Repeat("*", len(word)))
		}

		l.Push(lua.LString(filtered))
		return 1
	})
}

func IReplace(s, old, new string) string { // replace all, case insensitive
	if old == new || old == "" {
		return s // avoid allocation
	}
	t := strings.ToLower(s)
	o := strings.ToLower(old)

	// Compute number of replacements.
	n := strings.Count(t, o)
	if n == 0 {
		return s // avoid allocation
	}
	// Apply replacements to buffer.
	var b strings.Builder
	b.Grow(len(s) + n*(len(new)-len(old)))
	start := 0
	for i := 0; i < n; i++ {
		j := start
		if len(old) == 0 {
			if i > 0 {
				_, wid := utf8.DecodeRuneInString(s[start:])
				j += wid
			}
		} else {
			j += strings.Index(t[start:], o)
		}
		b.WriteString(s[start:j])
		b.WriteString(new)
		start = j + len(old)
	}
	b.WriteString(s[start:])
	return b.String()
}

// func luaGetCustomChars(ctx context.Context, luaState *lua.LState, userData *db.UserData) *lua.LFunction {
// 	return luaState.NewFunction(func(l *lua.LState) int {
// 		if userData == nil {
// 			return 0
// 		}

// 		chars, err := db.GetCustomChars(userData.ID)
// 		slg.GetSlog(ctx).Error("failed to get custom chars", "err", err)

// 		for _, char := range chars {
// 			l.Push(lua.LString(char))
// 		}

// 		return len(chars)
// 	})
// }

// func luaGetAllCharCards(ctx context.Context, luaState *lua.LState, charNameToCard map[string][]byte) *lua.LFunction {
// 	return luaState.NewFunction(func(l *lua.LState) int {
// 		arrTbl := l.NewTable()

// 		for charName, card := range charNameToCard {
// 			parsedCard, err := char.FromPngSillyTavernCard(card)
// 			if err != nil {
// 				slg.GetSlog(ctx).Error("failed to parse card", "err", err)
// 				continue
// 			}

// 			cardTbl := l.NewTable()

// 			cardTbl.RawSetString("name", lua.LString(parsedCard.Name))
// 			cardTbl.RawSetString("description", lua.LString(parsedCard.Description))
// 			cardTbl.RawSetString("personality", lua.LString(parsedCard.Personality))
// 			cardTbl.RawSetString("first_message", lua.LString(parsedCard.FirstMessage))
// 			cardTbl.RawSetString("message_example", lua.LString(parsedCard.MessageExample))
// 			cardTbl.RawSetString("scenario", lua.LString(parsedCard.Scenario))
// 			cardTbl.RawSetString("system_prompt", lua.LString(parsedCard.SystemPrompt))
// 			cardTbl.RawSetString("post_history_instructions", lua.LString(parsedCard.PostHistoryInstructions))

// 			arrTbl.RawSetString(charName, cardTbl)
// 		}

// 		l.Push(arrTbl)

// 		return 1
// 	})
// }

func (p *Processor) luaTtsWithText(ctx context.Context, luaState *lua.LState, eventWriter conns.EventWriter) *lua.LFunction {
	return luaState.NewFunction(func(l *lua.LState) int {
		msgID := int(lua.LVAsNumber(l.Get(1)))
		voice := l.Get(2).String()
		request := l.Get(3).String()

		if p.ToSkipMsg(msgID) {
			return 0
		}

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
		} else if ttsResponse.Audio, err = p.rvcVoice(ttsResponse.Audio, voice); err != nil {
			eventWriter(&conns.DataEvent{
				EventType: conns.EventTypeText,
				EventData: []byte("failed to rvc: " + err.Error()),
			})
		} else if data, err := json.Marshal(&audioEvent{AudioBase64: base64.StdEncoding.EncodeToString(ttsResponse.Audio), MsgID: strconv.Itoa(msgID)}); err != nil {
			eventWriter(&conns.DataEvent{
				EventType: conns.EventTypeText,
				EventData: []byte("failed to marshal json: " + err.Error()),
			})
		} else if eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeAudio,
			EventData: data,
		}) {
			audioLen := time.Millisecond * time.Duration(ttsResponse.AudioLen)

			if len(ttsResponse.Timings) == 0 {
				eventWriter(&conns.DataEvent{
					EventType: conns.EventTypeText,
					EventData: []byte(request),
				})

				time.Sleep(audioLen)

				return 0
			}

			fullSentence := ""
			var slept time.Duration

			for i, timing := range ttsResponse.Timings {
				if p.ToSkipMsg(msgID) {
					break
				}

				word := ttsResponse.Words[i]
				fullSentence += word + " "

				eventWriter(&conns.DataEvent{
					EventType: conns.EventTypeText,
					EventData: []byte(fullSentence),
				})

				toSleep := time.Duration(timing)*time.Millisecond - slept

				if toSleep+slept <= audioLen+time.Second {
					time.Sleep(toSleep)
					slept += toSleep
				}
			}

			if !p.ToSkipMsg(msgID) && slept < audioLen {
				time.Sleep(audioLen - slept)
			}
		}

		return 0
	})
}

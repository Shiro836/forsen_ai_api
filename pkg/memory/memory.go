package memory

import (
	"github.com/google/uuid"
	"golang.org/x/exp/slices"
)

type Message struct {
	id string

	User string
	Msg  string
}

type Memory struct {
	memorySz     int
	userMemorySz int

	memory      map[string][]Message
	nextUserMsg map[string]int

	lastMsgs []Message
	nextMsg  int
}

func New(memorySz, userMemorySz int) *Memory {
	return &Memory{
		memorySz:     memorySz,
		userMemorySz: userMemorySz,

		memory:      make(map[string][]Message, 10),
		nextUserMsg: make(map[string]int, 10),

		lastMsgs: make([]Message, memorySz*2+1),
	}
}

func (m *Memory) initUserMem(user string) {
	if _, ok := m.memory[user]; !ok {
		m.memory[user] = make([]Message, m.userMemorySz*2+1)
		m.nextUserMsg[user] = 0

		return
	}

	if m.nextUserMsg[user] == m.userMemorySz*2 {
		copy(m.memory[user][:m.userMemorySz], m.memory[user][m.userMemorySz:m.userMemorySz*2])
		m.nextUserMsg[user] = m.userMemorySz
	}
}

func (m *Memory) Push(user, msg string) {
	id := uuid.New()

	m.initUserMem(user)

	m.memory[user][m.nextUserMsg[user]] = Message{
		id:   id.String(),
		User: user,
		Msg:  msg,
	}
	m.nextUserMsg[user]++

	if m.nextMsg == m.memorySz*2 {
		copy(m.lastMsgs[:m.memorySz], m.lastMsgs[m.memorySz:m.memorySz*2])
		m.nextMsg = m.memorySz
	}

	m.lastMsgs[m.nextMsg] = Message{
		id:   id.String(),
		User: user,
		Msg:  msg,
	}
	m.nextMsg++
}

func (m *Memory) GetUserMem(user string) []Message {
	return m.memory[user][max(0, m.nextUserMsg[user]-m.userMemorySz):m.nextUserMsg[user]]
}

func (m *Memory) GetMem() []Message {
	return m.lastMsgs[max(0, m.nextMsg-m.memorySz):m.nextMsg]
}

func (m *Memory) GetCombinedMem(user string) []Message {
	res := m.GetMem()

	for _, msg := range m.GetUserMem(user) {
		if !slices.ContainsFunc(res, func(m Message) bool {
			return m.id == msg.id
		}) {
			res = append(res, msg)
		}
	}

	return res
}

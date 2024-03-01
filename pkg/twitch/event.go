package twitch

const (
	EventTypeChat EventType = iota
	EventTypeChannelPoint
	EventTypeFollow
	EventTypeSub
	EventTypeGift
	EventTypeRandom
	EventTypeInfo
	EventTypeRaid
	EventTypeUnknown
)

type EventType int

type Event struct {
	EventType EventType
	UserName  string
	Message   string
}

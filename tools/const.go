package tools

const (
	Any State = iota
	Wait
	Deleted
	Processed
	Current
)

type State int

func (s State) String() string {
	switch s {
	case Any:
		return "any"
	case Wait:
		return "wait"
	case Deleted:
		return "deleted"
	case Processed:
		return "processed"
	case Current:
		return "current"
	default:
		return "unknown"
	}
}

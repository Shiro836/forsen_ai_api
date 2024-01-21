package tools

const (
	Wait State = iota
	Deleted
	Processed
)

type State int

func (s State) String() string {
	switch s {
	case Wait:
		return "wait"
	case Deleted:
		return "deleted"
	case Processed:
		return "processed"
	default:
		return "unknown"
	}
}

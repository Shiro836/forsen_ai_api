package tools

import "sync"

// arguments on left are less important
func PriorityFanIn[T any](ch1 chan T, rest ...chan T) chan T {
	if len(rest) == 0 {
		return ch1
	}

	ch := make(chan T)

	ch2 := rest[0]

	go func() {
		defer close(ch)

		for {
			if ch1 == nil && ch2 == nil {
				return
			}

			select {
			case val, ok := <-ch2:
				if ok {
					ch <- val
					continue
				}
			default:
			}

			select {
			case val, ok := <-ch1:
				if !ok {
					ch1 = nil
					break
				}
				ch <- val
			case val, ok := <-ch2:
				if !ok {
					ch2 = nil
					break
				}
				ch <- val
			}
		}
	}()

	if len(rest) == 1 {
		return ch
	}

	return PriorityFanIn(ch, rest[1:]...)
}

// closes all output channels when any of input channels close. drains all input channels
func CloseAndDrainOnAnyClose[T any](in ...chan T) []chan T {
	if len(in) == 0 {
		return nil
	}

	done := make(chan struct{})
	once := sync.Once{}

	out := make([]chan T, len(in))
	for i, inCh := range in {
		i := i
		inCh := inCh

		out[i] = make(chan T)

		go func() {
			defer close(out[i])
			defer func() {
				for range inCh {
				}
			}()

		loop:
			for {
				select {
				case <-done:
					break loop
				default:
				}

				select {
				case val, ok := <-inCh:
					if !ok {
						break loop
					}
					out[i] <- val
				case <-done:
					break loop
				}
			}

			once.Do(func() {
				close(done)
			})
		}()
	}

	return out
}

package main

// arguments on left are less important
func priorityFanIn[T any](ch1 chan T, rest ...chan T) chan T {
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

	return priorityFanIn(ch, rest[1:]...)
}

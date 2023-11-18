package conns_test

import (
	"app/conns"
	"app/db"
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

var _ conns.Processor = &mockProc{}

type mockProc struct {
	mock.Mock
}

func (p *mockProc) Process(ctx context.Context, eventWriter conns.EventWriter, user string) error {
	args := p.Called(ctx, eventWriter, user)
	return args.Error(0)
}

func TestHandleUser(t *testing.T) {
	assert := assert.New(t)

	processor := &mockProc{}
	processor.On("Process", mock.Anything, mock.Anything, mock.Anything).Return(conns.ErrProcessingEnd)

	connManager := conns.NewConnectionManager(context.Background(), processor)
	connManager.HandleUser(&db.Human{
		Login: "test",
	})

	time.Sleep(100 * time.Millisecond) // is there a better way to wait for goroutine?
	// runtime.Gosched() // doesn't work sometimes

	assert.Len(processor.Calls, 1)
	assert.Equal("test", processor.Calls[0].Arguments.String(2))
}

func TestWait(t *testing.T) {
	assert := assert.New(t)

	processor := &mockProc{}
	processor.On("Process", mock.Anything, mock.Anything, mock.Anything).After(100 * time.Millisecond).Return(conns.ErrProcessingEnd)

	connManager := conns.NewConnectionManager(context.Background(), processor)
	for i := 0; i < 50; i++ {
		connManager.HandleUser(&db.Human{
			Login: "test" + strconv.Itoa(i),
		})
	}

	finish := false

	go func() {
		connManager.Wait()
		finish = true
	}()

	time.Sleep(50 * time.Millisecond)

	assert.False(finish)
	assert.Len(processor.Calls, 50)

	time.Sleep(66 * time.Millisecond)

	assert.True(finish)
	assert.Len(processor.Calls, 50)
}

func TestDataStream(t *testing.T) {
	assert := assert.New(t)

	user := "user"
	event := &conns.DataEvent{
		EventType: conns.EventTypeInfo,
		EventData: []byte("sheeeesh"),
	}

	processor := &mockProc{}
	processor.On("Process", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		args.Get(1).(conns.EventWriter)(event)
	}).Return(conns.ErrProcessingEnd)

	connManager := conns.NewConnectionManager(context.Background(), processor)

	subCh := connManager.Subscribe(user)

	connManager.HandleUser(&db.Human{
		Login: user,
	})

	recievedEvent, ok := <-subCh
	assert.True(ok)
	assert.Equal(event, recievedEvent)

	select {
	case <-subCh:
		assert.Fail("channel must be empty but not closed")
	default:
	}

	connManager.Unsubscribe(user)

	_, ok = <-subCh
	assert.False(ok)
}

func TestUnderLoad(t *testing.T) {
	assert := assert.New(t)

	cnt := 10000
	users, events := []string{}, []*conns.DataEvent{}
	eventsRepeated := 500

	for i := 0; i < cnt; i++ {
		users = append(users, "user"+strconv.Itoa(i))
		events = append(events, &conns.DataEvent{
			EventType: conns.EventTypeInfo,
			EventData: []byte(users[i]),
		})
	}

	processor := &mockProc{}
	for i := 0; i < cnt; i++ {
		processor.On("Process", mock.Anything, mock.Anything, mock.MatchedBy(func(user string) bool {
			return user == users[i]
		})).Run(func(args mock.Arguments) {
			writerFn := args.Get(1).(conns.EventWriter)
			for i := 0; i < eventsRepeated; i++ {
				writerFn(events[i])
			}
		}).Return(conns.ErrProcessingEnd)
	}

	connManager := conns.NewConnectionManager(context.Background(), processor)

	wg := sync.WaitGroup{}

	for i := 0; i < cnt; i++ {
		i := i
		go func() {
			subCh := connManager.Subscribe(users[i])

			for i := 0; i < eventsRepeated; i++ {
				recievedEvent, ok := <-subCh
				assert.True(ok)
				assert.Equal(events[i], recievedEvent)
			}

			select {
			case <-subCh:
				assert.Fail("channel must be empty but not closed")
			default:
			}

			connManager.Unsubscribe(users[i])

			_, ok := <-subCh
			assert.False(ok)
		}()
	}

	for i := 0; i < cnt; i++ {
		connManager.HandleUser(&db.Human{
			Login: users[i],
		})
	}

	wg.Wait()
}

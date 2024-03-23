package conns_test

import (
	"app/db"
	"app/internal/app/conns"
	"context"
	"log/slog"
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

func (p *mockProc) Process(ctx context.Context, updates chan *conns.Update, eventWriter conns.EventWriter, user *db.User) error {
	args := p.Called(ctx, updates, eventWriter, user)
	return args.Error(0)
}

func TestHandleUser(t *testing.T) {
	assert := assert.New(t)

	processor := &mockProc{}
	processor.On("Process", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(conns.ErrProcessingEnd)

	connManager := conns.NewConnectionManager(context.Background(), slog.Default(), processor)

	dbUser := &db.User{
		ID:          11,
		TwitchLogin: "test",
	}

	connManager.HandleUser(
		dbUser,
	)

	time.Sleep(100 * time.Millisecond) // is there a better way to wait for goroutine?
	// runtime.Gosched() // doesn't work sometimes

	assert.Len(processor.Calls, 1)
	assert.Equal(dbUser, processor.Calls[0].Arguments.Get(3))
}

func TestWait(t *testing.T) {
	assert := assert.New(t)

	processor := &mockProc{}
	processor.On("Process", mock.Anything, mock.Anything, mock.Anything, mock.Anything).After(100 * time.Millisecond).Return(conns.ErrProcessingEnd)

	connManager := conns.NewConnectionManager(context.Background(), slog.Default(), processor)
	for i := 0; i < 50; i++ {
		connManager.HandleUser(&db.User{
			ID: i,
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

	user := &db.User{
		ID:          5,
		TwitchLogin: "forsen",
	}
	event := &conns.DataEvent{
		EventType: conns.EventTypeInfo,
		EventData: []byte("sheeeesh"),
	}

	processor := &mockProc{}
	processor.On("Process", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		args.Get(2).(conns.EventWriter)(event)
	}).Return(conns.ErrProcessingEnd)

	connManager := conns.NewConnectionManager(context.Background(), slog.Default(), processor)

	subCh, unsub := connManager.Subscribe(user.ID)
	subCh2, unsub2 := connManager.Subscribe(user.ID)

	connManager.HandleUser(user)

	recievedEvent, ok := <-subCh
	assert.True(ok)
	assert.Equal(event, recievedEvent)

	recievedEvent, ok = <-subCh2
	assert.True(ok)
	assert.Equal(event, recievedEvent)

	select {
	case <-subCh:
		assert.Fail("channel must be empty but not closed")
	case <-subCh2:
		assert.Fail("channel must be empty but not closed")
	default:
	}

	unsub()
	unsub2()

	_, ok = <-subCh
	assert.False(ok)
}

func TestUnderLoad(t *testing.T) {
	assert := assert.New(t)

	cnt := 100
	users, events := []*db.User{}, []*conns.DataEvent{}
	userToEvent := map[string]*conns.DataEvent{}
	eventsRepeated := 2

	for i := 0; i < cnt; i++ {
		users = append(users, &db.User{
			ID:          i,
			TwitchLogin: "user_" + strconv.Itoa(i),
		})
		events = append(events, &conns.DataEvent{
			EventType: conns.EventTypeInfo,
			EventData: []byte(users[i].TwitchLogin),
		})
		userToEvent[users[i].TwitchLogin] = events[i]
	}

	processor := &mockProc{}
	processor.On("Process", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		user := args.Get(3).(*db.User)

		writerFn := args.Get(2).(conns.EventWriter)
		for i := 0; i < eventsRepeated; i++ {
			writerFn(userToEvent[user.TwitchLogin])
		}
	}).Return(conns.ErrProcessingEnd)

	connManager := conns.NewConnectionManager(context.Background(), slog.Default(), processor)

	wg := sync.WaitGroup{}

	for i := 0; i < cnt; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()

			subCh, unsub := connManager.Subscribe(users[i].ID)
			subCh2, unsub2 := connManager.Subscribe(users[i].ID)

			time.Sleep(20 * time.Millisecond)

			for j := 0; j < eventsRepeated; j++ {
				recievedEvent, ok := <-subCh
				assert.True(ok)
				assert.Equal(events[i], recievedEvent)

				recievedEvent, ok = <-subCh2
				assert.True(ok)
				assert.Equal(events[i], recievedEvent)
			}

			select {
			case <-subCh:
				assert.Fail("channel must be empty but not closed")
			case <-subCh2:
				assert.Fail("channel must be empty but not closed")
			default:
			}

			unsub()
			unsub2()

			_, ok := <-subCh
			assert.False(ok)
			_, ok = <-subCh2
			assert.False(ok)
		}()
	}

	time.Sleep(5 * time.Millisecond)

	for i := 0; i < cnt; i++ {
		connManager.HandleUser(users[i])
	}

	wg.Wait()
}

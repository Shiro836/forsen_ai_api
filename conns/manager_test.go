package conns_test

import (
	"app/conns"
	"app/db"
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

var _ conns.Processor = &mockProc{}

type mockProc struct {
	mock.Mock
}

func (p *mockProc) Process(ctx context.Context, user string) error {
	args := p.Called()
	return args.Error(0)
}

func TestHandleUser(t *testing.T) {
	assert := assert.New(t)

	processor := &mockProc{}
	processor.On("Process", mock.Anything).Return(conns.ErrProcessingEnd)

	connManager := conns.NewConnectionManager(context.Background(), processor)
	connManager.HandleUser(&db.Human{
		Login: "test",
	})

	time.Sleep(100 * time.Millisecond) // is there a better way to wait for goroutine?
	// runtime.Gosched() // doesn't work sometimes

	assert.Len(processor.Calls, 1)
}

func TestWait(t *testing.T) {
	assert := assert.New(t)

	processor := &mockProc{}
	processor.On("Process", mock.Anything).After(100 * time.Millisecond).Return(conns.ErrProcessingEnd)

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

}

package memory_test

import (
	"testing"

	"app/pkg/memory"

	"github.com/stretchr/testify/assert"
)

func toStr(msgs []memory.Message) []string {
	res := make([]string, len(msgs))
	for i, msg := range msgs {
		res[i] = msg.Msg
	}

	return res
}

func TestMemory(t *testing.T) {
	assert := assert.New(t)

	mem := memory.New(4, 2)

	assert.Empty(mem.GetMem())
	assert.Empty(mem.GetUserMem("tst"))
	assert.Empty(mem.GetCombinedMem("tst"))

	mem.Push("tst", "msg")

	assert.Equal([]string{"msg"}, toStr(mem.GetMem()))
	assert.Equal([]string{"msg"}, toStr(mem.GetUserMem("tst")))
	assert.Equal([]string{"msg"}, toStr(mem.GetCombinedMem("tst")))

	assert.Empty([]string{}, mem.GetUserMem("tst2"))

	mem.Push("tst2", "msg2")

	assert.Equal([]string{"msg", "msg2"}, toStr(mem.GetMem()))
	assert.Equal([]string{"msg"}, toStr(mem.GetUserMem("tst")))
	assert.Equal([]string{"msg2"}, toStr(mem.GetUserMem("tst2")))
	assert.Equal([]string{"msg", "msg2"}, toStr(mem.GetCombinedMem("tst")))
	assert.Equal([]string{"msg", "msg2"}, toStr(mem.GetCombinedMem("tst2")))
	assert.Equal([]string{"msg", "msg2"}, toStr(mem.GetCombinedMem("tst3")))

	assert.Empty([]string{}, mem.GetUserMem("tst3"))

	mem.Push("tst", "msg3")

	assert.Equal([]string{"msg", "msg2", "msg3"}, toStr(mem.GetMem()))
	assert.Equal([]string{"msg", "msg3"}, toStr(mem.GetUserMem("tst")))
	assert.Equal([]string{"msg2"}, toStr(mem.GetUserMem("tst2")))
	assert.Equal([]string{"msg", "msg2", "msg3"}, toStr(mem.GetCombinedMem("tst")))
	assert.Equal([]string{"msg", "msg2", "msg3"}, toStr(mem.GetCombinedMem("tst2")))
	assert.Equal([]string{"msg", "msg2", "msg3"}, toStr(mem.GetCombinedMem("tst3")))

	mem.Push("a_lot", "mmm")
	mem.Push("a_lot", "mmm")
	mem.Push("a_lot", "mmm")
	mem.Push("a_lot", "mmm")
	mem.Push("a_lot", "mmm")
	mem.Push("a_lot", "mmm")
	mem.Push("a_lot", "mmm")
	mem.Push("a_lot", "mmm")

	assert.Equal([]string{"mmm", "mmm", "mmm", "mmm"}, toStr(mem.GetMem()))
	assert.Equal([]string{"msg", "msg3"}, toStr(mem.GetUserMem("tst")))
	assert.Equal([]string{"msg2"}, toStr(mem.GetUserMem("tst2")))
	assert.Equal([]string{"mmm", "mmm", "mmm", "mmm", "msg", "msg3"}, toStr(mem.GetCombinedMem("tst")))
	assert.Equal([]string{"mmm", "mmm", "mmm", "mmm", "msg2"}, toStr(mem.GetCombinedMem("tst2")))
	assert.Equal([]string{"mmm", "mmm", "mmm", "mmm"}, toStr(mem.GetCombinedMem("tst3")))
}

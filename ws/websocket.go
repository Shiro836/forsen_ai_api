package ws

import (
	"fmt"
	"sync"

	"github.com/gorilla/websocket"
)

type Message struct {
	MsgType int
	Message []byte
}

type Client struct {
	conn *websocket.Conn

	writeChan chan *Message
	readChan  chan *Message

	closed bool
	lock   sync.Mutex
}

func (ws *Client) Close() error {
	ws.lock.Lock()
	defer ws.lock.Unlock()

	if ws.closed {
		return nil
	}

	ws.closed = true
	close(ws.writeChan)

	return ws.conn.Close()
}

func NewWsClient(conn *websocket.Conn) (client *Client, done chan struct{}) {
	client = &Client{
		conn: conn,

		writeChan: make(chan *Message),
		readChan:  make(chan *Message),
	}

	done = make(chan struct{})

	go func() {
		defer close(client.readChan)
		defer client.Close()

		for {
			msg := &Message{}
			var err error

			msg.MsgType, msg.Message, err = conn.ReadMessage()
			if err != nil {
				break
			}

			client.readChan <- msg
		}
	}()

	go func() {
		defer close(done)
		defer func() {
			for range client.writeChan {
			}
		}()

		for msg := range client.writeChan {
			if err := conn.WriteMessage(msg.MsgType, msg.Message); err != nil {
				fmt.Println("write err:", err)
				break
			}
		}
	}()

	return client, done
}

func (ws *Client) Send(msg *Message) error {
	ws.lock.Lock()
	defer ws.lock.Unlock()

	if ws.closed {
		return fmt.Errorf("ws is closed")
	}

	ws.writeChan <- msg

	return nil
}

func (ws *Client) Read() (*Message, error) {
	msg, ok := <-ws.readChan
	if !ok {
		return nil, fmt.Errorf("ws is closed")
	}

	return msg, nil
}

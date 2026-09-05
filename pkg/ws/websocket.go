package ws

import (
	"errors"
	"fmt"
	"sync"

	"github.com/gorilla/websocket"
)

var ErrClosed = errors.New("ws is closed")

// Upgrader keeps gorilla's default origin check: an Origin header must match
// the request host, no Origin header passes. Every client page opens its
// socket against window.location.host, so this holds on each of our hosts.
var Upgrader = websocket.Upgrader{}

const TextMessage = websocket.TextMessage

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

	metrics.WebSocketConnections.Dec()
	ws.closed = true
	close(ws.writeChan)

	return ws.conn.Close()
}

func NewWsClient(conn *websocket.Conn) (client *Client, done chan struct{}) {
	client = &Client{
		conn: conn,

		writeChan: make(chan *Message, 5),
		readChan:  make(chan *Message),
	}

	metrics.WebSocketConnections.Inc()

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
		return ErrClosed
	}

	ws.writeChan <- msg

	return nil
}

func (ws *Client) Read() (*Message, error) {
	msg, ok := <-ws.readChan
	if !ok {
		return nil, ErrClosed
	}

	return msg, nil
}

// use it when you don't need to read messages
func (ws *Client) DrainRead() {
	for {
		_, err := ws.Read()
		if err != nil {
			return
		}
	}
}

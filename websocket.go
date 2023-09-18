package main

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type wsMessage struct {
	msgType int
	message []byte
}

type wsClient struct {
	conn *websocket.Conn

	writeChan chan *wsMessage
	readChan  chan *wsMessage

	closed bool
	lock   sync.Mutex
}

func (ws *wsClient) close() error {
	ws.lock.Lock()
	defer ws.lock.Unlock()

	if ws.closed {
		return nil
	}

	ws.closed = true
	close(ws.writeChan)

	return ws.conn.Close()
}

func newWsClient(conn *websocket.Conn) (*wsClient, chan struct{}) {
	client := &wsClient{
		conn: conn,

		writeChan: make(chan *wsMessage),
		readChan:  make(chan *wsMessage),
	}

	done := make(chan struct{})

	go func() {
		defer close(client.readChan)
		defer client.close()

		for {
			msg := &wsMessage{}
			var err error

			msg.msgType, msg.message, err = conn.ReadMessage()
			if err != nil {
				fmt.Println("read err:", err)
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
			if err := conn.WriteMessage(msg.msgType, msg.message); err != nil {
				fmt.Println("write err:", err)
				break
			}
		}
	}()

	return client, done
}

func (ws *wsClient) send(msg *wsMessage) error {
	ws.lock.Lock()
	defer ws.lock.Unlock()

	if ws.closed {
		return fmt.Errorf("ws is closed")
	}

	ws.writeChan <- msg

	return nil
}

func (ws *wsClient) read() (*wsMessage, error) {
	msg, ok := <-ws.readChan
	if !ok {
		return nil, fmt.Errorf("ws is closed")
	}

	return msg, nil
}

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
	// "github.com/gorilla/mux"

	"github.com/gorilla/websocket"
	"github.com/satori/go.uuid"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer.
	maxMessageSize = 512
)

var (
	clients []*Client
	rooms   map[string][]*Client
)

type Client struct {
	id   string
	conn *websocket.Conn
	send chan []byte
	room string
}

func getClient(clientID string) *Client {
	for _, c := range clients {
		if c.id == clientID {
			return c
		}
	}
	return nil
}

func (c *Client) HandleEvent(eventName string, data map[string]interface{}) {
	switch eventName {
	case "__join":
		c.handleJoin(data)
	case "__ice_candidate":
		c.handleIceCandidate(data)
	case "__offer":
		c.handleOffer(data)
	case "__answer":
		c.handleAnswer(data)
	}
}

func (c *Client) handleJoin(data map[string]interface{}) {
	clientIDs := make([]string, 0, 1)
	curRoom := make([]*Client, 0, 0)
	room := data["room"].(string)
	var ok bool
	if curRoom, ok = rooms[room]; ok {
		for _, client := range curRoom {
			if c.id == client.id {
				continue
			}
			clientIDs = append(clientIDs, client.id)

			msg := map[string]interface{}{
				"eventName": "_new_peer",
				"data": map[string]string{
					"socketId": c.id,
				},
			}
			message, err := json.Marshal(msg)
			if err != nil {
				log.Printf("Json marshal err, %v", err)
			}
			client.send <- message
		}
	}
	rooms[room] = append(curRoom, c)
	c.room = room

	msg := map[string]interface{}{
		"eventName": "_peers",
		"data": map[string]interface{}{
			"connections": clientIDs,
			"you":         c.id,
		},
	}
	message, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Json marshal err, %v", err)
	}
	c.send <- message

	log.Printf("新用户 %s 加入房间 %s", c.id, room)
}

func (c *Client) handleIceCandidate(data map[string]interface{}) {
	cp := getClient(data["socketId"].(string))
	if cp == nil {
		log.Printf("Not found client in HandleIceCandidate")
	} else {
		msg := map[string]interface{}{
			"eventName": "_ice_candidate",
			"data": map[string]interface{}{
				"label":     data["label"],
				"candidate": data["candidate"],
				"socketId":  c.id,
			},
		}
		message, err := json.Marshal(msg)
		if err != nil {
			log.Printf("Json marshal err, %v", err)
		}
		cp.send <- message
	}
	log.Printf("接收到来自 %s 的ICE Candidate", c.id)
}

func (c *Client) handleOffer(data map[string]interface{}) {
	cp := getClient(data["socketId"].(string))
	if cp == nil {
		log.Printf("Not found client in HandleOffer")
	} else {
		msg := map[string]interface{}{
			"eventName": "_offer",
			"data": map[string]interface{}{
				"sdp":      data["sdp"],
				"socketId": c.id,
			},
		}
		message, err := json.Marshal(msg)
		if err != nil {
			log.Printf("Json marshal err, %v", err)
		}
		cp.send <- message
	}
	log.Printf("接收到来自 %s 的Offer", c.id)
}

func (c *Client) handleAnswer(data map[string]interface{}) {
	cp := getClient(data["socketId"].(string))
	if cp == nil {
		log.Printf("Not found client in HandleAnswer")
	} else {
		msg := map[string]interface{}{
			"eventName": "_answer",
			"data": map[string]interface{}{
				"sdp":      data["sdp"],
				"socketId": c.id,
			},
		}
		message, err := json.Marshal(msg)
		if err != nil {
			log.Printf("Json marshal err, %v", err)
		}
		cp.send <- message
	}
	log.Printf("接收到来自 %s 的Answer", c.id)
}

func (c *Client) readPump() {
	defer c.conn.Close()

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}
		var msg map[string]interface{}
		json.Unmarshal(message, &msg)
		if eventName, ok := msg["eventName"]; ok {
			// data := make(map[string]string)
			// for key, value := range msg["data"].(map[string]interface{}) {
			// 	log.Println(value)
			// 	data[key] = value.(string)
			// }
			c.HandleEvent(eventName.(string), msg["data"].(map[string]interface{}))
		} else {
			log.Printf("接收到来自%s的新消息：%s", c.id, message)
		}

	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// The hub closed the channel.
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

func serveHome(w http.ResponseWriter, r *http.Request) {
	log.Println(r.URL)
	if r.URL.Path != "/home" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	http.ServeFile(w, r, "home.html")
}

func serveWs(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}
	client := &Client{id: fmt.Sprintf("%s", uuid.NewV4()), conn: conn, send: make(chan []byte, 256)}
	clients = append(clients, client)

	go client.writePump()
	go client.readPump()

	log.Printf("创建新连接")
}

func main() {
	clients = make([]*Client, 0, 2)
	rooms = make(map[string][]*Client)

	fs := http.FileServer(http.Dir("public"))
	http.Handle("/", fs)
	http.HandleFunc("/home", serveHome)
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(w, r)
	})
	err := http.ListenAndServeTLS(":8080", "file.crt", "private.pem", nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}

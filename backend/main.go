package main

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"structure-generator/beso"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type Client struct {
	conn  *websocket.Conn
	grid  *beso.Grid
	mu    sync.Mutex
	er    float64
	ar    float64
}

type Message struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

type PointPayload struct {
	X int `json:"x"`
	Y int `json:"y"`
	Z int `json:"z"`
}

type ConfigPayload struct {
	TargetVolume float64 `json:"targetVolume"`
}

const (
	GRID_SIZE = 20
)

func NewClient(conn *websocket.Conn) *Client {
	return &Client{
		conn: conn,
		grid: beso.NewGrid(GRID_SIZE, GRID_SIZE, GRID_SIZE),
		er:   0.05,
		ar:   0.05,
	}
}

func (c *Client) SendJSON(msg Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

func encodeVoxelChangesBinary(changes []beso.VoxelChange) ([]byte, error) {
	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)

	count := len(changes)
	header := make([]byte, 4)
	binary.LittleEndian.PutUint32(header, uint32(count))
	if _, err := gzWriter.Write(header); err != nil {
		return nil, err
	}

	for _, ch := range changes {
		record := make([]byte, 10)
		binary.LittleEndian.PutUint16(record[0:2], uint16(ch.Index))
		binary.LittleEndian.PutUint16(record[2:4], uint16(ch.X))
		binary.LittleEndian.PutUint16(record[4:6], uint16(ch.Y))
		binary.LittleEndian.PutUint16(record[6:8], uint16(ch.Z))
		record[8] = ch.Action

		stressByte := byte(0)
		if ch.Stress >= 0 {
			s := ch.Stress
			if s > 1.0 {
				s = 1.0
			}
			stressByte = byte(s * 255.0)
		}
		record[9] = stressByte

		if _, err := gzWriter.Write(record); err != nil {
			return nil, err
		}
	}

	if err := gzWriter.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func encodeFullGridBinary(grid *beso.Grid) ([]byte, error) {
	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)

	header := make([]byte, 12)
	binary.LittleEndian.PutUint32(header[0:4], uint32(grid.SizeX))
	binary.LittleEndian.PutUint32(header[4:8], uint32(grid.SizeY))
	binary.LittleEndian.PutUint32(header[8:12], uint32(grid.SizeZ))
	if _, err := gzWriter.Write(header); err != nil {
		return nil, err
	}

	total := grid.SizeX * grid.SizeY * grid.SizeZ
	bitCount := (total + 7) / 8
	existsBits := make([]byte, bitCount)
	stressBytes := make([]byte, total)

	for i := 0; i < total; i++ {
		v := grid.Voxels[i]
		if v.Exists || v.IsLoad || v.IsSupport {
			byteIdx := i / 8
			bitIdx := uint(i % 8)
			existsBits[byteIdx] |= 1 << bitIdx
		}

		s := v.Stress
		if s > 1.0 {
			s = 1.0
		}
		if s < 0 {
			s = 0
		}
		stressBytes[i] = byte(s * 255.0)
	}

	if _, err := gzWriter.Write(existsBits); err != nil {
		return nil, err
	}
	if _, err := gzWriter.Write(stressBytes); err != nil {
		return nil, err
	}

	loadCount := len(grid.LoadPoints)
	loadBuf := make([]byte, 4+loadCount*2)
	binary.LittleEndian.PutUint32(loadBuf[0:4], uint32(loadCount))
	for i, idx := range grid.LoadPoints {
		binary.LittleEndian.PutUint16(loadBuf[4+i*2:6+i*2], uint16(idx))
	}
	if _, err := gzWriter.Write(loadBuf); err != nil {
		return nil, err
	}

	supportCount := len(grid.SupportPoints)
	supportBuf := make([]byte, 4+supportCount*2)
	binary.LittleEndian.PutUint32(supportBuf[0:4], uint32(supportCount))
	for i, idx := range grid.SupportPoints {
		binary.LittleEndian.PutUint16(supportBuf[4+i*2:6+i*2], uint16(idx))
	}
	if _, err := gzWriter.Write(supportBuf); err != nil {
		return nil, err
	}

	if err := gzWriter.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func (c *Client) SendBinary(msgType byte, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	frame := append([]byte{msgType}, data...)
	return c.conn.WriteMessage(websocket.BinaryMessage, frame)
}

const (
	BIN_MSG_FULL_GRID     = 1
	BIN_MSG_VOXEL_CHANGES = 2
)

func (c *Client) sendFullState() error {
	c.grid.ComputeStress()

	data, err := encodeFullGridBinary(c.grid)
	if err != nil {
		return err
	}

	if err := c.SendBinary(BIN_MSG_FULL_GRID, data); err != nil {
		return err
	}

	return c.SendJSON(Message{
		Type: "state",
		Payload: map[string]interface{}{
			"iteration":     c.grid.Iteration,
			"volumeRatio":   c.grid.GetVolumeRatio(),
			"targetVolume":  c.grid.TargetVolumeRatio,
			"loadCount":     len(c.grid.LoadPoints),
			"supportCount":  len(c.grid.SupportPoints),
			"isConverged":   c.grid.IsConverged(),
		},
	})
}

func (c *Client) sendIterationResult(changes []beso.VoxelChange) error {
	data, err := encodeVoxelChangesBinary(changes)
	if err != nil {
		return err
	}

	if err := c.SendBinary(BIN_MSG_VOXEL_CHANGES, data); err != nil {
		return err
	}

	return c.SendJSON(Message{
		Type: "iteration",
		Payload: map[string]interface{}{
			"iteration":     c.grid.Iteration,
			"volumeRatio":   c.grid.GetVolumeRatio(),
			"targetVolume":  c.grid.TargetVolumeRatio,
			"changesCount":  len(changes),
			"isConverged":   c.grid.IsConverged(),
		},
	})
}

func (c *Client) handleMessage(msgType int, data []byte) {
	if msgType == websocket.TextMessage {
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("JSON parse error: %v", err)
			return
		}

		switch msg.Type {
		case "setLoad":
			var p PointPayload
			if payload, ok := msg.Payload.(map[string]interface{}); ok {
				p.X = int(payload["x"].(float64))
				p.Y = int(payload["y"].(float64))
				p.Z = int(payload["z"].(float64))
			}
			c.grid.SetLoad(p.X, p.Y, p.Z)
			c.sendFullState()

		case "setSupport":
			var p PointPayload
			if payload, ok := msg.Payload.(map[string]interface{}); ok {
				p.X = int(payload["x"].(float64))
				p.Y = int(payload["y"].(float64))
				p.Z = int(payload["z"].(float64))
			}
			c.grid.SetSupport(p.X, p.Y, p.Z)
			c.sendFullState()

		case "removeLoad":
			var p PointPayload
			if payload, ok := msg.Payload.(map[string]interface{}); ok {
				p.X = int(payload["x"].(float64))
				p.Y = int(payload["y"].(float64))
				p.Z = int(payload["z"].(float64))
			}
			c.grid.RemoveLoad(p.X, p.Y, p.Z)
			c.sendFullState()

		case "removeSupport":
			var p PointPayload
			if payload, ok := msg.Payload.(map[string]interface{}); ok {
				p.X = int(payload["x"].(float64))
				p.Y = int(payload["y"].(float64))
				p.Z = int(payload["z"].(float64))
			}
			c.grid.RemoveSupport(p.X, p.Y, p.Z)
			c.sendFullState()

		case "setConfig":
			if payload, ok := msg.Payload.(map[string]interface{}); ok {
				if tv, ok := payload["targetVolume"].(float64); ok {
					c.grid.TargetVolumeRatio = tv
				}
			}
			c.sendFullState()

		case "iterate":
			changes := c.grid.Evolve(c.er, c.ar)
			c.sendIterationResult(changes)

		case "iterateAll":
			go func() {
				for !c.grid.IsConverged() {
					changes := c.grid.Evolve(c.er, c.ar)
					c.sendIterationResult(changes)
				}
				c.SendJSON(Message{
					Type: "done",
					Payload: map[string]interface{}{
						"iteration":   c.grid.Iteration,
						"volumeRatio": c.grid.GetVolumeRatio(),
					},
				})
			}()

		case "reset":
			c.grid.Reset()
			c.sendFullState()

		case "getState":
			c.sendFullState()
		}
	}
}

func (c *Client) Run() {
	defer c.conn.Close()

	c.sendFullState()

	for {
		msgType, data, err := c.conn.ReadMessage()
		if err != nil {
			if err != io.EOF {
				log.Printf("Read error: %v", err)
			}
			return
		}
		c.handleMessage(msgType, data)
	}
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Upgrade error: %v", err)
		return
	}

	client := NewClient(conn)
	log.Printf("Client connected from %s", conn.RemoteAddr())
	client.Run()
	log.Printf("Client disconnected")
}

func main() {
	fs := http.FileServer(http.Dir("../frontend"))
	http.Handle("/", fs)

	http.HandleFunc("/ws", handleWebSocket)

	log.Println("Server starting on :8081")
	log.Println("Frontend: http://localhost:8081")
	log.Println("WebSocket: ws://localhost:8081/ws")
	log.Fatal(http.ListenAndServe(":8081", nil))
}

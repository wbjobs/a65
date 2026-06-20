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
	"time"

	"github.com/gorilla/websocket"
	"github.com/vmihailenco/msgpack/v5"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  65536,
	WriteBufferSize: 65536,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type Message struct {
	Type    string      `json:"type" msgpack:"t"`
	Payload interface{} `json:"payload" msgpack:"p"`
}

type StatePayload struct {
	Iteration           int     `json:"iteration" msgpack:"i"`
	VolumeRatio         float64 `json:"volumeRatio" msgpack:"vr"`
	TargetVolume        float64 `json:"targetVolume" msgpack:"tv"`
	LoadCount           int     `json:"loadCount" msgpack:"lc"`
	SupportCount        int     `json:"supportCount" msgpack:"sc"`
	IsConverged         bool    `json:"isConverged" msgpack:"ic"`
	TimeStep            int     `json:"timeStep" msgpack:"ts"`
	Time                float64 `json:"time" msgpack:"t"`
	CurrentLoadValue    float64 `json:"currentLoadValue" msgpack:"clv"`
	FatigueWarningCount int     `json:"fatigueWarningCount" msgpack:"fwc"`
	FatigueCriticalCount int    `json:"fatigueCriticalCount" msgpack:"fcc"`
	MaxFatigueDamage    float64 `json:"maxFatigueDamage" msgpack:"mfd"`
	UseDynamicLoad      bool    `json:"useDynamicLoad" msgpack:"udl"`
	DynamicFrequency    float64 `json:"dynamicFrequency" msgpack:"df"`
	DynamicAmplitude    float64 `json:"dynamicAmplitude" msgpack:"da"`
}

type IterationPayload struct {
	Iteration           int     `json:"iteration" msgpack:"i"`
	VolumeRatio         float64 `json:"volumeRatio" msgpack:"vr"`
	TargetVolume        float64 `json:"targetVolume" msgpack:"tv"`
	ChangesCount        int     `json:"changesCount" msgpack:"cc"`
	IsConverged         bool    `json:"isConverged" msgpack:"ic"`
	TimeStep            int     `json:"timeStep" msgpack:"ts"`
	Time                float64 `json:"time" msgpack:"t"`
	CurrentLoadValue    float64 `json:"currentLoadValue" msgpack:"clv"`
	FatigueWarningCount int     `json:"fatigueWarningCount" msgpack:"fwc"`
	FatigueCriticalCount int    `json:"fatigueCriticalCount" msgpack:"fcc"`
	MaxFatigueDamage    float64 `json:"maxFatigueDamage" msgpack:"mfd"`
	DidAnalysis         bool    `json:"didAnalysis" msgpack:"da"`
}

type DynamicLoadPayload struct {
	Frequency float64 `json:"frequency" msgpack:"f"`
	Amplitude float64 `json:"amplitude" msgpack:"a"`
	Enabled   bool    `json:"enabled" msgpack:"e"`
}

type VoxelChangeCompact struct {
	Index         uint16 `msgpack:"i"`
	Action        uint8  `msgpack:"a"`
	Stress        uint8  `msgpack:"s"`
	FatigueDamage uint8  `msgpack:"f"`
}

const (
	GRID_SIZE     = 20
	MSG_JSON      = 0
	MSG_MSGPACK   = 1
	BIN_FULL      = 1
	BIN_CHANGES   = 2
	BIN_STATE     = 3
	BIN_ITER      = 4
	BIN_TIMESTEP  = 5
	MAX_QUEUE     = 100
	SEND_THROTTLE = 8 * time.Millisecond
)

type outboundMessage struct {
	msgType int
	data    []byte
}

type Client struct {
	conn              *websocket.Conn
	grid              *beso.Grid
	mu                sync.Mutex
	sendMu            sync.Mutex
	fatigueUpdateCount int
	er                float64
	ar                float64
	sendQueue         chan outboundMessage
	done              chan struct{}
	wg                sync.WaitGroup
	lastSend          time.Time
	sendThrottle      time.Duration
	pendingMsgs       int
	maxPending        int
	isClosed          bool
	dynamicRunning    bool
	dynamicTicker     *time.Ticker
	dynamicStop       chan struct{}
	dynamicWg         sync.WaitGroup
}

func NewClient(conn *websocket.Conn) *Client {
	return &Client{
		conn:         conn,
		grid:         beso.NewGrid(GRID_SIZE, GRID_SIZE, GRID_SIZE),
		er:           0.05,
		ar:           0.05,
		sendQueue:    make(chan outboundMessage, MAX_QUEUE),
		done:         make(chan struct{}),
		sendThrottle: SEND_THROTTLE,
		maxPending:   MAX_QUEUE,
		dynamicStop:  make(chan struct{}),
	}
}

func (c *Client) Start() {
	c.wg.Add(1)
	go c.sendLoop()
}

func (c *Client) sendLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(c.sendThrottle)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.processQueue()
		}
	}
}

func (c *Client) processQueue() {
	for {
		select {
		case msg := <-c.sendQueue:
			c.mu.Lock()
			if c.isClosed {
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()

			c.sendMu.Lock()
			err := c.conn.WriteMessage(msg.msgType, msg.data)
			c.sendMu.Unlock()

			c.mu.Lock()
			c.pendingMsgs--
			c.mu.Unlock()

			if err != nil {
				log.Printf("Write error: %v, closing connection", err)
				c.Close()
				return
			}
		default:
			return
		}
	}
}

func (c *Client) enqueueMessage(msgType int, data []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isClosed {
		return false
	}

	if c.pendingMsgs >= c.maxPending {
		log.Printf("Queue overflow (%d), dropping message", c.pendingMsgs)
		return false
	}

	select {
	case c.sendQueue <- outboundMessage{msgType: msgType, data: data}:
		c.pendingMsgs++
		return true
	default:
		log.Printf("Queue full, dropping message")
		return false
	}
}

func (c *Client) Close() {
	c.StopDynamic()

	c.mu.Lock()
	if c.isClosed {
		c.mu.Unlock()
		return
	}
	c.isClosed = true
	close(c.done)
	c.mu.Unlock()

	c.sendMu.Lock()
	c.conn.Close()
	c.sendMu.Unlock()

	c.wg.Wait()
}

func encodeVoxelChangesMsgPack(changes []beso.VoxelChange) ([]byte, error) {
	compact := make([]VoxelChangeCompact, len(changes))
	for i, ch := range changes {
		compact[i] = VoxelChangeCompact{
			Index:         uint16(ch.Index),
			Action:        uint8(ch.Action),
			Stress:        uint8(min(max(ch.Stress*255, 0), 255)),
			FatigueDamage: uint8(min(max(ch.FatigueDamage*255, 0), 255)),
		}
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	enc := msgpack.NewEncoder(gz)
	if err := enc.Encode(compact); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func encodeStateMsgPack(sp *StatePayload) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	enc := msgpack.NewEncoder(gz)
	if err := enc.Encode(sp); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeIterationMsgPack(ip *IterationPayload) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	enc := msgpack.NewEncoder(gz)
	if err := enc.Encode(ip); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeFullGridBinaryCompact(grid *beso.Grid) ([]byte, error) {
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
	fatigueBytes := make([]byte, total)

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

		f := v.FatigueDamage
		if f > 1.0 {
			f = 1.0
		}
		if f < 0 {
			f = 0
		}
		fatigueBytes[i] = byte(f * 255.0)
	}

	if _, err := gzWriter.Write(existsBits); err != nil {
		return nil, err
	}
	if _, err := gzWriter.Write(stressBytes); err != nil {
		return nil, err
	}
	if _, err := gzWriter.Write(fatigueBytes); err != nil {
		return nil, err
	}

	metaBuf := make([]byte, 8)
	binary.LittleEndian.PutUint32(metaBuf[0:4], uint32(len(grid.LoadPoints)))
	binary.LittleEndian.PutUint32(metaBuf[4:8], uint32(len(grid.SupportPoints)))
	if _, err := gzWriter.Write(metaBuf); err != nil {
		return nil, err
	}

	for _, idx := range grid.LoadPoints {
		idxBuf := make([]byte, 2)
		binary.LittleEndian.PutUint16(idxBuf, uint16(idx))
		if _, err := gzWriter.Write(idxBuf); err != nil {
			return nil, err
		}
	}

	for _, idx := range grid.SupportPoints {
		idxBuf := make([]byte, 2)
		binary.LittleEndian.PutUint16(idxBuf, uint16(idx))
		if _, err := gzWriter.Write(idxBuf); err != nil {
			return nil, err
		}
	}

	if err := gzWriter.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func (c *Client) sendFullState() error {
	c.grid.ComputeStress()
	c.grid.ComputeFatigueDamage()

	data, err := encodeFullGridBinaryCompact(c.grid)
	if err != nil {
		return err
	}

	frame := append([]byte{BIN_FULL}, data...)
	c.enqueueMessage(websocket.BinaryMessage, frame)

	sp := &StatePayload{
		Iteration:            c.grid.Iteration,
		VolumeRatio:          c.grid.GetVolumeRatio(),
		TargetVolume:         c.grid.TargetVolumeRatio,
		LoadCount:            len(c.grid.LoadPoints),
		SupportCount:         len(c.grid.SupportPoints),
		IsConverged:          c.grid.IsConverged(),
		TimeStep:             c.grid.TimeStep,
		Time:                 c.grid.Time,
		CurrentLoadValue:     c.grid.GetCurrentLoadValue(),
		FatigueWarningCount:  c.grid.GetFatigueWarningCount(),
		FatigueCriticalCount: c.grid.GetFatigueCriticalCount(),
		MaxFatigueDamage:     c.grid.GetMaxFatigueDamage(),
		UseDynamicLoad:       c.grid.UseDynamicLoad,
		DynamicFrequency:     c.grid.DynamicFrequency,
		DynamicAmplitude:     c.grid.DynamicAmplitude,
	}

	stateData, err := encodeStateMsgPack(sp)
	if err != nil {
		return err
	}

	stateFrame := append([]byte{BIN_STATE}, stateData...)
	c.enqueueMessage(websocket.BinaryMessage, stateFrame)

	return nil
}

func (c *Client) sendIterationResult(changes []beso.VoxelChange, didAnalysis bool) error {
	data, err := encodeVoxelChangesMsgPack(changes)
	if err != nil {
		return err
	}

	frame := append([]byte{BIN_CHANGES}, data...)
	c.enqueueMessage(websocket.BinaryMessage, frame)

	ip := &IterationPayload{
		Iteration:            c.grid.Iteration,
		VolumeRatio:          c.grid.GetVolumeRatio(),
		TargetVolume:         c.grid.TargetVolumeRatio,
		ChangesCount:         len(changes),
		IsConverged:          c.grid.IsConverged(),
		TimeStep:             c.grid.TimeStep,
		Time:                 c.grid.Time,
		CurrentLoadValue:     c.grid.GetCurrentLoadValue(),
		FatigueWarningCount:  c.grid.GetFatigueWarningCount(),
		FatigueCriticalCount: c.grid.GetFatigueCriticalCount(),
		MaxFatigueDamage:     c.grid.GetMaxFatigueDamage(),
		DidAnalysis:          didAnalysis,
	}

	iterData, err := encodeIterationMsgPack(ip)
	if err != nil {
		return err
	}

	iterFrame := append([]byte{BIN_ITER}, iterData...)
	c.enqueueMessage(websocket.BinaryMessage, iterFrame)

	return nil
}

func (c *Client) sendTimeStepResult(result beso.TimeStepResult) error {
	sp := &StatePayload{
		Iteration:            c.grid.Iteration,
		VolumeRatio:          c.grid.GetVolumeRatio(),
		TargetVolume:         c.grid.TargetVolumeRatio,
		LoadCount:            len(c.grid.LoadPoints),
		SupportCount:         len(c.grid.SupportPoints),
		IsConverged:          c.grid.IsConverged(),
		TimeStep:             result.TimeStep,
		Time:                 result.Time,
		CurrentLoadValue:     c.grid.GetCurrentLoadValue(),
		FatigueWarningCount:  c.grid.GetFatigueWarningCount(),
		FatigueCriticalCount: c.grid.GetFatigueCriticalCount(),
		MaxFatigueDamage:     c.grid.GetMaxFatigueDamage(),
		UseDynamicLoad:       c.grid.UseDynamicLoad,
		DynamicFrequency:     c.grid.DynamicFrequency,
		DynamicAmplitude:     c.grid.DynamicAmplitude,
	}

	stateData, err := encodeStateMsgPack(sp)
	if err != nil {
		return err
	}

	stateFrame := append([]byte{BIN_STATE}, stateData...)
	c.enqueueMessage(websocket.BinaryMessage, stateFrame)

	if result.FatigueUpdated {
		c.fatigueUpdateCount++
		if c.fatigueUpdateCount%5 == 0 || c.fatigueUpdateCount == 1 {
			c.sendFullState()
		}
	}

	if result.DidAnalysis && len(result.Changes) > 0 {
		c.sendIterationResult(result.Changes, true)
	}

	return nil
}

func (c *Client) StartDynamic() {
	c.mu.Lock()
	if c.dynamicRunning {
		c.mu.Unlock()
		return
	}
	c.dynamicRunning = true
	c.dynamicStop = make(chan struct{})
	c.dynamicTicker = time.NewTicker(32 * time.Millisecond)
	c.mu.Unlock()

	c.dynamicWg.Add(1)
	go func() {
		defer c.dynamicWg.Done()
		for {
			select {
			case <-c.dynamicStop:
				return
			case <-c.dynamicTicker.C:
				c.mu.Lock()
				closed := c.isClosed
				queueFull := c.pendingMsgs >= c.maxPending-10
				c.mu.Unlock()

				if closed {
					return
				}
				if queueFull {
					time.Sleep(10 * time.Millisecond)
					continue
				}

				result := c.grid.StepTimeDynamic(c.er, c.ar)
				c.sendTimeStepResult(result)

				if result.TimeStep%50 == 0 {
					log.Printf("Dynamic step %d, time: %.2fs, load: %.2f, fatigue warn: %d, critical: %d, pending: %d",
						result.TimeStep, result.Time, c.grid.GetCurrentLoadValue(),
						c.grid.GetFatigueWarningCount(), c.grid.GetFatigueCriticalCount(),
						c.pendingMsgs)
				}

				if c.grid.IsConverged() {
					c.StopDynamic()
					doneData, _ := json.Marshal(Message{
						Type: "done",
						Payload: map[string]interface{}{
							"iteration":   c.grid.Iteration,
							"volumeRatio": c.grid.GetVolumeRatio(),
							"timeStep":    c.grid.TimeStep,
						},
					})
					c.enqueueMessage(websocket.TextMessage, doneData)
					return
				}
			}
		}
	}()
}

func (c *Client) StopDynamic() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.dynamicRunning {
		return
	}
	c.dynamicRunning = false
	if c.dynamicTicker != nil {
		c.dynamicTicker.Stop()
	}
	close(c.dynamicStop)
	c.dynamicWg.Wait()
}

type PointPayload struct {
	X int `json:"x" msgpack:"x"`
	Y int `json:"y" msgpack:"y"`
	Z int `json:"z" msgpack:"z"`
}

type ConfigPayload struct {
	TargetVolume float64 `json:"targetVolume" msgpack:"tv"`
}

func (c *Client) handleMessage(msgType int, data []byte) {
	if msgType == websocket.TextMessage {
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("JSON parse error: %v", err)
			return
		}
		c.processCommand(msg.Type, msg.Payload)
	} else if msgType == websocket.BinaryMessage {
		if len(data) < 1 {
			return
		}
		msgTypeByte := data[0]
		payload := data[1:]

		if msgTypeByte == MSG_MSGPACK {
			var msg Message
			if err := msgpack.Unmarshal(payload, &msg); err != nil {
				log.Printf("MsgPack parse error: %v", err)
				return
			}
			c.processCommand(msg.Type, msg.Payload)
		}
	}
}

func (c *Client) processCommand(cmdType string, payload interface{}) {
	switch cmdType {
	case "setLoad":
		p := parsePointPayload(payload)
		c.grid.SetLoad(p.X, p.Y, p.Z)
		c.sendFullState()

	case "setSupport":
		p := parsePointPayload(payload)
		c.grid.SetSupport(p.X, p.Y, p.Z)
		c.sendFullState()

	case "removeLoad":
		p := parsePointPayload(payload)
		c.grid.RemoveLoad(p.X, p.Y, p.Z)
		c.sendFullState()

	case "removeSupport":
		p := parsePointPayload(payload)
		c.grid.RemoveSupport(p.X, p.Y, p.Z)
		c.sendFullState()

	case "setConfig":
		if p, ok := payload.(map[string]interface{}); ok {
			if tv, ok := p["targetVolume"].(float64); ok {
				c.grid.TargetVolumeRatio = tv
			}
			if tv, ok := p["tv"].(float64); ok {
				c.grid.TargetVolumeRatio = tv
			}
		}
		c.sendFullState()

	case "iterate":
		changes := c.grid.Evolve(c.er, c.ar)
		c.sendIterationResult(changes, true)

	case "iterateAll":
		c.StopDynamic()
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("IterateAll panic recovered: %v", r)
				}
			}()

			throttle := time.NewTicker(32 * time.Millisecond)
			defer throttle.Stop()

			for !c.grid.IsConverged() {
				<-throttle.C

				c.mu.Lock()
				closed := c.isClosed
				queueFull := c.pendingMsgs >= c.maxPending-5
				c.mu.Unlock()

				if closed {
					return
				}
				if queueFull {
					time.Sleep(50 * time.Millisecond)
					continue
				}

				changes := c.grid.Evolve(c.er, c.ar)
				c.sendIterationResult(changes, true)

				if c.grid.Iteration%50 == 0 {
					log.Printf("Static iteration %d, volume: %.2f%%, pending: %d",
						c.grid.Iteration, c.grid.GetVolumeRatio()*100, c.pendingMsgs)
				}
			}

			doneData, _ := json.Marshal(Message{
				Type: "done",
				Payload: map[string]interface{}{
					"iteration":   c.grid.Iteration,
					"volumeRatio": c.grid.GetVolumeRatio(),
				},
			})
			c.enqueueMessage(websocket.TextMessage, doneData)
		}()

	case "setDynamicParams":
		var freq, amp float64
		var enabled bool
		if p, ok := payload.(map[string]interface{}); ok {
			if f, ok := p["frequency"].(float64); ok {
				freq = f
			}
			if f, ok := p["f"].(float64); ok {
				freq = f
			}
			if a, ok := p["amplitude"].(float64); ok {
				amp = a
			}
			if a, ok := p["a"].(float64); ok {
				amp = a
			}
			if e, ok := p["enabled"].(bool); ok {
				enabled = e
			}
			if e, ok := p["e"].(bool); ok {
				enabled = e
			}
		}
		c.grid.SetDynamicParams(freq, amp)
		c.grid.UseDynamicLoad = enabled
		c.sendFullState()

	case "startDynamic":
		if len(c.grid.LoadPoints) == 0 || len(c.grid.SupportPoints) == 0 {
			return
		}
		c.grid.UseDynamicLoad = true
		c.StartDynamic()
		log.Printf("Dynamic optimization started for client %s", c.conn.RemoteAddr())

	case "stopDynamic":
		c.StopDynamic()
		log.Printf("Dynamic optimization stopped for client %s", c.conn.RemoteAddr())
		c.sendFullState()

	case "stepTime":
		result := c.grid.StepTimeDynamic(c.er, c.ar)
		c.sendTimeStepResult(result)

	case "reset":
		c.StopDynamic()
		c.grid.Reset()
		c.sendFullState()

	case "getState":
		c.sendFullState()
	}
}

func parsePointPayload(payload interface{}) PointPayload {
	var p PointPayload
	if m, ok := payload.(map[string]interface{}); ok {
		if x, ok := m["x"].(float64); ok {
			p.X = int(x)
		}
		if y, ok := m["y"].(float64); ok {
			p.Y = int(y)
		}
		if z, ok := m["z"].(float64); ok {
			p.Z = int(z)
		}
		if x, ok := m["x"].(int8); ok {
			p.X = int(x)
		}
		if y, ok := m["y"].(int8); ok {
			p.Y = int(y)
		}
		if z, ok := m["z"].(int8); ok {
			p.Z = int(z)
		}
	}
	return p
}

func (c *Client) Run() {
	defer c.Close()

	c.Start()
	c.sendFullState()

	conn := c.conn
	conn.SetReadLimit(1024 * 1024)

	for {
		msgType, data, err := conn.ReadMessage()
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

	conn.SetPingHandler(func(appData string) error {
		return nil
	})

	client := NewClient(conn)
	log.Printf("Client connected from %s", conn.RemoteAddr())
	client.Run()
	log.Printf("Client disconnected, iterations: %d", client.grid.Iteration)
}

func main() {
	fs := http.FileServer(http.Dir("../frontend"))
	http.Handle("/", fs)

	http.HandleFunc("/ws", handleWebSocket)

	log.Println("Server starting on :8081")
	log.Println("Frontend: http://localhost:8081")
	log.Println("WebSocket: ws://localhost:8081/ws")
	log.Println("Optimizations: MessagePack, queue throttling, backpressure control")
	log.Fatal(http.ListenAndServe(":8081", nil))
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

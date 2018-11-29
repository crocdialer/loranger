package nodes

import (
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// StructType serves as a enum to distinguish different json encoded structs
type StructType int

const (
	// NodeType is used as type field by Node structs
	NodeType StructType = 1 << 0

	// NodeCommandType is used as type field by NodeCommand structs
	NodeCommandType StructType = 1 << 1

	// NodeCommandACKType is used as type field by NodeCommandACK structs
	NodeCommandACKType StructType = 1 << 2
)

func (structType StructType) String() string {
	names := map[StructType]string{
		NodeType:           "Node",
		NodeCommandType:    "NodeCommand",
		NodeCommandACKType: "NodeCommandACK"}
	str, ok := names[structType]
	if ok {
		return str
	}
	return "Unknown"
}

// TypeHelper is a small helper struct used to unmarshal json messages to extract their type
type TypeHelper struct {
	Type StructType `json:"type"`
}

// Node structures information of a remote device
type Node struct {
	Type         StructType `json:"type"`
	Address      int        `json:"address"`
	ID           string     `json:"id"`
	LastRssi     int        `json:"rssi"`
	Frequency    float64    `json:"freq"`
	Mode         int        `json:"mode"`
	Temperature  float64    `json:"temp"`
	BatteryLevel float64    `json:"bat"`
	Active       bool       `json:"active"`
	TimeStamp    time.Time  `json:"stamp"`
	GpsPosition  [2]float64 `json:"gps"`
}

// NodeCommand realizes a simple RPC interface
type NodeCommand struct {
	Type      StructType    `json:"type"`
	CommandID int           `json:"cmd_id"`
	Address   int           `json:"dst"`
	Command   string        `json:"cmd"`
	Params    []interface{} `json:"params"`
}

// NodeCommandACK is used as simple ACK for received commands
type NodeCommandACK struct {
	Type      StructType `json:"type"`
	CommandID int        `json:"cmd_id"`
	Ok        bool       `json:"ok"`
}

// CommandLogItem bundles information about one past command
type CommandLogItem struct {
	Command  *NodeCommand `json:"command"`
	Attempts int          `json:"attempts"`
	Stamp    time.Time    `json:"stamp"`
}

// SendTo will encode a NodeCommand and put it into the provided channel
func (nc *NodeCommand) SendTo(outChannel chan<- []byte) {
	jsonStr, err := json.Marshal(nc)

	if err != nil {
		log.Println("could not marshal NodeCommand:", nc)
	} else {
		// send command
		// log.Println("sending command:", string(jsonStr))
		jsonStr = append(jsonStr, []byte("\n\n")...)
		// jsonStr = append(jsonStr, '\n')
		outChannel <- jsonStr
	}
}

// CommandTransfer groups assets for pending commands
type CommandTransfer struct {
	Command *NodeCommand `json:"command"`
	Ticker  *time.Ticker `json:"-"`
	C       chan int     `json:"-"`
	Done    chan bool    `json:"-"`
	sink    chan<- []byte
	Stamps  []time.Time `json:"stamps"`
}

// NewCommandTransfer creates a new instance and sets up a periodic retransmit
func NewCommandTransfer(command *NodeCommand, output chan<- []byte, retransmit time.Duration) (bundle *CommandTransfer) {
	bundle = &CommandTransfer{
		Command: command,
		sink:    output,
		Ticker:  time.NewTicker(retransmit),
		C:       make(chan int, 100),
		Done:    make(chan bool)}
	bundle.Stamps = []time.Time{time.Now()}
	bundle.Command.SendTo(bundle.sink)
	bundle.C <- len(bundle.Stamps)
	go bundle.transmit()
	return bundle
}

func (cmd *CommandTransfer) String() string {
	return fmt.Sprintf("{id: %d -- command: %s -- params: %v -- attempts: %d}", cmd.Command.CommandID,
		cmd.Command.Command, cmd.Command.Params, len(cmd.Stamps))
}

func (cmd *CommandTransfer) transmit() {
	defer log.Println("transmit done", cmd.Command)

	for {
		cmd.C <- len(cmd.Stamps)

		select {
		case <-cmd.Done:
			return
		case t := <-cmd.Ticker.C:
			cmd.Stamps = append(cmd.Stamps, t)
			log.Printf("#%d resending:%v", len(cmd.Stamps), cmd.Command)
			cmd.Command.SendTo(cmd.sink)
		}
	}
}

// FilterNodes filters a slice of Nodes according to the provided duration and granularity
func FilterNodes(nodes []Node, duration, granularity time.Duration) (outNodes []Node) {
	durationAccum := granularity
	lastTimeStamp := nodes[0].TimeStamp

	for _, logItem := range nodes {

		if time.Now().Sub(logItem.TimeStamp) < duration {
			// accum durations, drop too fine-grained values
			durationAccum += logItem.TimeStamp.Sub(lastTimeStamp)

			if durationAccum >= granularity {
				durationAccum = 0
				outNodes = append(outNodes, logItem)
			}
		}
		lastTimeStamp = logItem.TimeStamp
	}
	return outNodes
}

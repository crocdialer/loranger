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
	Success  bool         `json:"success"`
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
	Command     *NodeCommand  `json:"command"`
	Stamps      []time.Time   `json:"stamps"`
	Success     bool          `json:"success"`
	C           chan int      `json:"-"`
	Done        chan bool     `json:"-"`
	Retransmits int           `json:"retransmits"`
	TimeOut     time.Duration `json:"timeout"`
	sink        chan<- []byte
	ticker      *time.Ticker
}

// NewCommandTransfer creates a new instance and sets up a periodic retransmit
func NewCommandTransfer(
	command *NodeCommand,
	output chan<- []byte,
	retransmits int,
	timeOut time.Duration) (bundle *CommandTransfer) {

	bundle = &CommandTransfer{
		Command:     command,
		sink:        output,
		Retransmits: retransmits,
		TimeOut:     timeOut,
		C:           make(chan int, 100),
		Done:        make(chan bool)}

	// creation time
	bundle.Stamps = []time.Time{time.Now()}
	return bundle
}

func (cmd *CommandTransfer) String() string {
	return fmt.Sprintf("{id: %d -- dst:%d -- command: %s -- params: %v -- attempts: %d}",
		cmd.Command.CommandID, cmd.Command.Address, cmd.Command.Command, cmd.Command.Params, len(cmd.Stamps))
}

func (cmd *CommandTransfer) transmitWorker() {
	// defer log.Println("transmit done:", cmd)

	// initial send
	cmd.C <- len(cmd.Stamps)
	cmd.Command.SendTo(cmd.sink)

	// start ticker
	cmd.ticker = time.NewTicker(cmd.TimeOut)

	for {
		cmd.C <- len(cmd.Stamps)

		select {
		case cmd.Success = <-cmd.Done:
			cmd.ticker.Stop()
			close(cmd.C)
			return
		case t := <-cmd.ticker.C:
			cmd.Stamps = append(cmd.Stamps, t)
			cmd.Command.SendTo(cmd.sink)
		}
	}
}

// Transmit the command, issue update-events for changes, monitor number of retransmits
func (cmd *CommandTransfer) Transmit(results chan<- *CommandTransfer, update func()) {
	// start periodic transmission
	go cmd.transmitWorker()

	for numTransmits := range cmd.C {
		// log.Printf("#%d sending:%v", len(cmd.Stamps), cmd.Command)
		if numTransmits >= cmd.Retransmits {
			log.Println("command failed, node unreachable:", cmd)
			cmd.Done <- false

			// remove unsuccessful command
			results <- cmd
		} else {
			update()
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

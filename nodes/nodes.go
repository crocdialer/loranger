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

	// CommandType is used as type field by Command structs
	CommandType StructType = 1 << 1

	// CommandACKType is used as type field by CommandACK structs
	CommandACKType StructType = 1 << 2
)

func (structType StructType) String() string {
	names := map[StructType]string{
		NodeType:       "Node",
		CommandType:    "Command",
		CommandACKType: "CommandACK"}
	if str, ok := names[structType]; ok {
		return str
	}
	return "Unknown"
}

const (
	// SmartBulb3000Type type identifier
	SmartBulb3000Type string = "smart_bulb_3000"
)

// MinimalNode is a helper struct to represent the minimum information we require from a generic lora node.
// Used to unmarshal json messages to extract their type
type MinimalNode struct {
	Type     string `json:"type"`
	Address  int    `json:"address"`
	LastRssi int    `json:"rssi"`
}

// NodeEvent groups a message from a generic node with its timestamp
type NodeEvent struct {
	Active    bool        `json:"active"`
	Data      interface{} `json:"data"`
	TimeStamp time.Time   `json:"stamp"`
}

// Command realizes a simple RPC interface
type Command struct {
	CommandID int           `json:"id"`
	Address   int           `json:"dst"`
	Command   string        `json:"cmd"`
	Arguments []interface{} `json:"arg"`
}

// CommandACK is used as simple ACK for received commands
type CommandACK struct {
	CommandID int  `json:"id"`
	Ok        bool `json:"ok"`
}

// CommandLogItem bundles information about one past command
type CommandLogItem struct {
	Command  *Command  `json:"command"`
	Success  bool      `json:"success"`
	Attempts int       `json:"attempts"`
	Stamp    time.Time `json:"stamp"`
}

// SendTo will encode a Command and put it into the provided channel
func (nc *Command) SendTo(outChannel chan<- []byte) {
	jsonStr, err := json.Marshal(nc)

	if err != nil {
		log.Println("could not marshal Command:", nc)
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
	Command     *Command      `json:"command"`
	Stamps      []time.Time   `json:"stamps"`
	Success     bool          `json:"success"`
	Retransmits int           `json:"retransmits"`
	TimeOut     time.Duration `json:"timeout"`
	C           chan int      `json:"-"`
	Done        chan bool     `json:"-"`
	sink        chan<- []byte
	ticker      *time.Ticker
}

// NewCommandTransfer creates a new instance
func NewCommandTransfer(
	command *Command,
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
	return fmt.Sprintf("{id: %d -- dst:%d -- command: %s -- args: %v -- attempts: %d}",
		cmd.Command.CommandID, cmd.Command.Address, cmd.Command.Command, cmd.Command.Arguments, len(cmd.Stamps))
}

// Start will start the command-transmission, monitor number of retransmits
// and issue update-events for changes using the provided update-function object
func (cmd *CommandTransfer) Start(results chan<- *CommandTransfer, update func()) {
	// start periodic transmission
	go transmitWorker(cmd)

	for numTransmits := range cmd.C {
		// log.Printf("#%d sending:%v", len(cmd.Stamps), cmd.Command)
		if numTransmits >= cmd.Retransmits {
			log.Println("command failed, node unreachable:", cmd)
			cmd.Done <- false

			// remove unsuccessful command
			results <- cmd
		} else if update != nil {
			update()
		}
	}
}

func transmitWorker(cmd *CommandTransfer) {
	// defer log.Println("transmit done:", cmd)

	// initial send
	cmd.C <- len(cmd.Stamps)
	cmd.Command.SendTo(cmd.sink)

	// start ticker
	cmd.ticker = time.NewTicker(cmd.TimeOut)

	for {
		select {
		case cmd.Success = <-cmd.Done:
			cmd.ticker.Stop()
			close(cmd.C)
			return
		case t := <-cmd.ticker.C:
			cmd.C <- len(cmd.Stamps)
			cmd.Stamps = append(cmd.Stamps, t)
			cmd.Command.SendTo(cmd.sink)
		}
	}
}

// FilterNodes filters a slice of Nodes according to the provided duration and granularity
func FilterNodes(nodes []*NodeEvent, duration, granularity time.Duration) (outNodes []*NodeEvent) {
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

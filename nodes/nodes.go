package nodes

import (
	"encoding/json"
	"log"
	"time"
)

// StructType serves as a enum to distinguish different json encoded structs
type StructType int

const (
	// NodeType is used type field by Node structs
	NodeType StructType = 1 << 0

	// NodeCommandType is used type field by NodeCommand structs
	NodeCommandType StructType = 1 << 1

	// NodeCommandACKType is used type field by NodeCommandACK structs
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

// SendTo will encode a NoceCommand and put it into the provided channel
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

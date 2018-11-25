package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/tarm/serial"
)

var serveFilesPath string
var serialDevices []*serial.Port

// create serial channels
var serialInput, serialOutput chan []byte

// nodes
var nodes map[int][]Node

// next command id
var nextCommandID = 1

// pending (sent but unackknowledged commands)
var pendingNodeCommands map[int]*NodeCommand

// StructType serves as a enum to distinguish different json encoded structs
type StructType int

const (
	// NodeType is used type field by Node structs
	NodeType StructType = 1

	// NodeCommandType is used type field by NodeCommand structs
	NodeCommandType StructType = 2

	// NodeCommandACKType is used type field by NodeCommandACK structs
	NodeCommandACKType StructType = 3
)

func (structType StructType) String() string {
	names := [...]string{
		"Node",
		"NodeCommand",
		"NodeCommandACK"}
	if structType < NodeType || structType > NodeCommandACKType {
		return "Unknown"
	}
	return names[structType-1]
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

func (nc *NodeCommand) sendTo(outChannel chan<- []byte) {
	jsonStr, err := json.Marshal(nc)

	if err != nil {
		log.Println("could not marshal NodeCommand:", nc)
	} else {
		// send command
		// log.Println("sending command:", string(jsonStr))
		jsonStr = append(jsonStr, []byte("\n\n")...)
		outChannel <- jsonStr
	}
}

func filterNodes(nodes []Node, duration, granularity time.Duration) (outNodes []Node) {
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

func readData(input chan []byte) {
	for line := range input {
		// log.Println(string(line))

		var typeHelper TypeHelper
		if err := json.Unmarshal(line, &typeHelper); err != nil {
			log.Println("could not extract struct-type from data", string(line))
		} else {
			switch typeHelper.Type {
			case NodeType:
				var node Node
				if err := json.Unmarshal(line, &node); err != nil {
					log.Println("could not parse data as json:", string(line))
				} else {
					node.TimeStamp = time.Now()
					nodes[node.Address] = append(nodes[node.Address], node)
					// log.Println(node)
				}
			case NodeCommandACKType:
				var nodeCommandACK NodeCommandACK
				if err := json.Unmarshal(line, &nodeCommandACK); err != nil {
					log.Println("could not parse data as json:", string(line))
				} else {
					if nodeCommandACK.Ok {
						// log.Println("received ACK for command:", nodeCommandACK)
						delete(pendingNodeCommands, nodeCommandACK.CommandID)
					} else {
						log.Println("resending command:", pendingNodeCommands[nodeCommandACK.CommandID])
						pendingNodeCommands[nodeCommandACK.CommandID].sendTo(serialOutput)
					}
				}
			}
		}
	}
}

// /nodes
// /nodes/{nodeID:[0-9]+}
// /nodes/{nodeID:[0-9]+}/log?duration=1h&granularity=10s
func handleNodes(w http.ResponseWriter, r *http.Request) {
	// log.Printf("request: %s\n", r.URL.Path[1:])

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)

	// all nodes or a specific one
	vars := mux.Vars(r)
	nodeID, ok := vars["nodeID"]

	if ok {
		k, _ := strconv.Atoi(nodeID)
		nodeHistory, ok := nodes[k]

		if ok {
			// entire history vs. last state
			if strings.HasSuffix(r.URL.Path, "/log") {

				//manage time-range and -granularity here
				// Parses the request body
				r.ParseForm()
				duration, err := time.ParseDuration(r.Form.Get("duration"))
				if err != nil {
					duration = time.Hour
				}
				granularity, err := time.ParseDuration(r.Form.Get("granularity"))
				if err != nil {
					granularity = time.Minute
				}
				log.Println("log of last:", duration, "granularity:", granularity)

				nodeOutLog := filterNodes(nodeHistory, duration, granularity)
				enc.Encode(nodeOutLog)
			} else {
				enc.Encode(nodeHistory[len(nodeHistory)-1])
			}
		}
	} else {
		// no nodeId provided, reply with a sorted list of all nodes' last state
		var nodeKeys []int

		for k := range nodes {
			nodeKeys = append(nodeKeys, k)
		}
		sort.Ints(nodeKeys)
		nodeList := make([]Node, len(nodes))

		for i, k := range nodeKeys {
			nodeList[i] = nodes[k][len(nodes[k])-1]
			nodeList[i].Active = time.Now().Sub(nodeList[i].TimeStamp) < time.Second*10
		}
		enc.Encode(nodeList)
	}
}

// POST
func handleNodeCommand(w http.ResponseWriter, r *http.Request) {

	// configure proper CORS
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	// decode json-request
	nodeCommand := &NodeCommand{}
	decoder := json.NewDecoder(r.Body)
	decoder.Decode(nodeCommand)

	// insert struct-type and CommandID
	nodeCommand.Type = NodeCommandType
	nodeCommand.CommandID = nextCommandID
	nextCommandID++

	// check if the node exists
	_, hasNode := nodes[nodeCommand.Address]

	// encode json ACK and send as response
	enc := json.NewEncoder(w)
	ack := NodeCommandACK{NodeCommandACKType, nodeCommand.CommandID, hasNode}
	enc.Encode(ack)

	if hasNode {
		// keep track of the command
		pendingNodeCommands[nodeCommand.CommandID] = nodeCommand
		nodeCommand.sendTo(serialOutput)
	}
}

func handlePendingNodeCommands(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	var cmdKeys []int

	for k := range pendingNodeCommands {
		cmdKeys = append(cmdKeys, k)
	}
	sort.Ints(cmdKeys)
	cmdList := make([]*NodeCommand, len(pendingNodeCommands))

	for i, k := range cmdKeys {
		cmdList[i] = pendingNodeCommands[k]
	}

	// encode pending commands as json and send as response
	enc := json.NewEncoder(w)
	enc.Encode(cmdList)
}

func readSerial(s *serial.Port, output chan<- []byte) {
	buf := make([]byte, 256)
	var message string
	for {
		n, err := s.Read(buf)
		if err != nil {
			log.Fatal(err)
			continue
		}

		message += string(buf[:n])
		message = strings.Replace(message, "\r", "", -1)

		lines := strings.Split(message, "\n")

		// at least one complete line
		if len(lines) > 1 {
			message = lines[len(lines)-1]

			for _, l := range lines[:len(lines)-1] {
				output <- []byte(l)
			}
		}
	}
}

func writeData(input chan []byte) {
	for bytes := range input {
		for _, s := range serialDevices {
			s.Write(bytes)
			s.Flush()
		}
	}
}

func main() {
	log.Println("welcome loranger")

	if len(os.Args) > 1 {
		serveFilesPath = os.Args[1]
	} else {
		serveFilesPath = "static/"
	}

	// make our global state maps
	nodes = make(map[int][]Node)
	pendingNodeCommands = make(map[int]*NodeCommand)

	// create channel
	serialInput = make(chan []byte, 100)
	serialOutput = make(chan []byte, 100)

	files, err := ioutil.ReadDir("/dev")
	if err != nil {
		log.Fatal(err)
	}

	for _, f := range files {
		deviceName := "/dev/" + f.Name()

		if strings.Contains(deviceName, "ttyACM") || strings.Contains(deviceName, "tty.usb") {
			c := &serial.Config{Name: deviceName, Baud: 115200}
			s, err := serial.OpenPort(c)
			if err != nil {
				log.Fatal(err)
				continue
			}
			defer s.Close()
			serialDevices = append(serialDevices, s)

			log.Println("reading from", deviceName)

			// workaround for fishy behaviour: send initial newline char
			s.Write([]byte("\n"))

			// producer feeds lines into channel
			go readSerial(s, serialInput)

			// quit after first found serial
			break
		}
	}
	// consume incoming data
	go readData(serialInput)

	// deliver outgoing data to connected serials
	go writeData(serialOutput)

	// serve static files
	fs := http.FileServer(http.Dir(serveFilesPath))

	muxRouter := mux.NewRouter()

	// services dealing with lora-nodes
	muxRouter.HandleFunc("/nodes", handleNodes)
	muxRouter.HandleFunc("/nodes/cmd", handleNodeCommand).Methods("POST", "OPTIONS")
	muxRouter.HandleFunc("/nodes/cmd/pending", handlePendingNodeCommands)
	muxRouter.HandleFunc("/nodes/{nodeID:[0-9]+}", handleNodes)
	muxRouter.HandleFunc("/nodes/{nodeID:[0-9]+}/log", handleNodes)
	muxRouter.PathPrefix("/").Handler(fs)
	http.Handle("/", muxRouter)

	port := 8080
	log.Println("server listening on port", port, " -- serving files from", serveFilesPath)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}

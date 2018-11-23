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
var nextCommandID int

// pending (sent but unackknowledged commands)
var pendingNodeCommands map[int]*NodeCommand

// Node structures information of a remote device
type Node struct {
	Address      int        `json:"address"`
	ID           string     `json:"id"`
	LastRssi     int        `json:"rssi"`
	Mode         int        `json:"mode"`
	Temperature  float64    `json:"temp"`
	BatteryLevel float64    `json:"bat"`
	Active       bool       `json:"active"`
	TimeStamp    time.Time  `json:"stamp"`
	GpsPosition  [2]float64 `json:"gps"`
}

// NodeCommand realizes a simple RPC interface
type NodeCommand struct {
	CommandID int           `json:"cmd_id"`
	Address   int           `json:"dst"`
	Command   string        `json:"cmd"`
	Params    []interface{} `json:"params"`
}

// NodeCommandACK is used as simple ACK for received commands
type NodeCommandACK struct {
	Ok bool `json:"ok"`
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
		var node Node
		if err := json.Unmarshal(line, &node); err != nil {
			log.Println("could not parse data as json:", string(line))
		} else {
			node.TimeStamp = time.Now()
			nodes[node.Address] = append(nodes[node.Address], node)
			// log.Println(node)
		}
	}
}

// /nodes
// /nodes/{nodeID:[0-9]+}
// /nodes/{nodeID:[0-9]+}/log?duration=1h&granularity=10s
func handleNodes(w http.ResponseWriter, r *http.Request) {
	// log.Printf("request: %s\n", r.URL.Path[1:])

	// TODO: testing only
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

	// insert CommandID
	nodeCommand.CommandID = nextCommandID
	nextCommandID++

	// check if the node exists
	_, hasNode := nodes[nodeCommand.Address]

	// encode json ACK and send as response
	enc := json.NewEncoder(w)
	ack := NodeCommandACK{hasNode}
	enc.Encode(ack)

	if hasNode {
		// keep track of the command
		pendingNodeCommands[nodeCommand.CommandID] = nodeCommand

		jsonStr, err := json.Marshal(nodeCommand)

		if err != nil {
			log.Println("could not marshal NodeCommand:", nodeCommand)
		} else {
			// send record command
			log.Println("sending command:", string(jsonStr))
			jsonStr = append(jsonStr, '\n')
			serialOutput <- jsonStr
		}
	}
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
			// s.Flush()
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

			// producer feeds lines into channel
			go readSerial(s, serialInput)
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
	muxRouter.HandleFunc("/nodes/{nodeID:[0-9]+}", handleNodes)
	muxRouter.HandleFunc("/nodes/{nodeID:[0-9]+}/log", handleNodes)
	muxRouter.PathPrefix("/").Handler(fs)
	http.Handle("/", muxRouter)

	port := 8080
	log.Println("server listening on port", port, " -- serving files from", serveFilesPath)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}

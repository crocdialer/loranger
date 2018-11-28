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
	"sync"
	"time"

	"github.com/crocdialer/loranger/nodes"
	"github.com/crocdialer/loranger/sse"
	"github.com/gorilla/mux"
	"github.com/tarm/serial"
)

var serveFilesPath string
var serialDevices []*serial.Port

// create serial channels
var serialInput, serialOutput chan []byte

// nodes
var nodeMap map[int][]nodes.Node

// deadline timers for active Nodes
var nodeTimers map[int]*time.Timer

// inactivity timeout for Nodes
var nodeTimeout = time.Second * 10

// next command id
var nextCommandID = 1

// pending commands (sent but unackknowledged)
var commands map[int]*nodes.CommandTransfer

// keep track of issued commands
var commandLog []nodes.CommandLogItem

// retransmit timeout for commands
var commandTimeout = time.Millisecond * 1900

// maximum number of command (re-)transmits
var commandMaxNumTransmit = 10

var pendingCommandLock = sync.RWMutex{}

// handle for SSE-Server
var sseServer *sse.Server

func commandList() []*nodes.CommandTransfer {
	var cmdKeys []int

	for k := range commands {
		cmdKeys = append(cmdKeys, k)
	}
	sort.Ints(cmdKeys)
	cmdList := make([]*nodes.CommandTransfer, len(commands))

	for i, k := range cmdKeys {
		cmdList[i] = commands[k]
	}
	return cmdList
}

func readData(input chan []byte) {
	for line := range input {
		// log.Println(string(line))

		var typeHelper nodes.TypeHelper
		if err := json.Unmarshal(line, &typeHelper); err != nil {
			log.Println("could not extract struct-type from data", string(line))
		} else {
			switch typeHelper.Type {
			case nodes.NodeType:
				var node nodes.Node
				if err := json.Unmarshal(line, &node); err != nil {
					log.Println("could not parse data as json:", string(line))
				} else {
					node.Active = true
					node.TimeStamp = time.Now()
					nodeMap[node.Address] = append(nodeMap[node.Address], node)

					// emit SSE-event
					sseServer.NodeEvent <- &node

					// existing timer?
					if timer, hasTimer := nodeTimers[node.Address]; hasTimer {
						timer.Stop()
					}

					// create deadline Timer for inactivity status
					nodeTimers[node.Address] = time.AfterFunc(nodeTimeout, func() {

						// copy last state and set inactive
						newState := nodeMap[node.Address][len(nodeMap[node.Address])-1]
						newState.Active = false
						nodeMap[node.Address] = append(nodeMap[node.Address], newState)

						// emit SSE-event
						sseServer.NodeEvent <- &newState
					})
				}
			case nodes.NodeCommandACKType:
				var nodeCommandACK nodes.NodeCommandACK
				if err := json.Unmarshal(line, &nodeCommandACK); err != nil {
					log.Println("could not parse data as json:", string(line))
				} else {
					if nodeCommandACK.Ok {
						pendingCommandLock.Lock()
						// log.Println("received ACK for command:", commands[nodeCommandACK.CommandID])
						if cmd, ok := commands[nodeCommandACK.CommandID]; ok {
							cmd.Ticker.Stop()
							commandLog = append(commandLog, nodes.CommandLogItem{Command: cmd.Command,
								Attempts: len(cmd.Stamps), Stamp: time.Now()})
						}
						delete(commands, nodeCommandACK.CommandID)
						pendingCommandLock.Unlock()
					}
					// emit SSE-event
					sseServer.NodeCommandEvent <- commandList()
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
		nodeHistory, ok := nodeMap[k]

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

				nodeOutLog := nodes.FilterNodes(nodeHistory, duration, granularity)
				enc.Encode(nodeOutLog)
			} else {
				enc.Encode(nodeHistory[len(nodeHistory)-1])
			}
		}
	} else {
		// no nodeId provided, reply with a sorted list of all nodes' last state
		var nodeKeys []int

		for k := range nodeMap {
			nodeKeys = append(nodeKeys, k)
		}
		sort.Ints(nodeKeys)
		nodeList := make([]nodes.Node, len(nodeMap))

		for i, k := range nodeKeys {
			nodeList[i] = nodeMap[k][len(nodeMap[k])-1]
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
	nodeCommand := &nodes.NodeCommand{}
	decoder := json.NewDecoder(r.Body)
	decoder.Decode(nodeCommand)

	// insert struct-type and CommandID
	nodeCommand.Type = nodes.NodeCommandType
	nodeCommand.CommandID = nextCommandID
	nextCommandID++

	// check if the node exists
	_, hasNode := nodeMap[nodeCommand.Address]

	// encode json ACK and send as response
	enc := json.NewEncoder(w)
	ack := nodes.NodeCommandACK{Type: nodes.NodeCommandACKType, CommandID: nodeCommand.CommandID, Ok: hasNode}
	enc.Encode(ack)

	if hasNode {
		commandTransfer := nodes.NewCommandTransfer(nodeCommand, serialOutput, commandTimeout)

		// lock mutex and keep track of the command
		pendingCommandLock.Lock()
		commands[nodeCommand.CommandID] = commandTransfer
		pendingCommandLock.Unlock()

		// emit SSE-events for changes, monitor number of retransmits
		go func() {
			for numTransmits := range commandTransfer.C {
				if numTransmits > commandMaxNumTransmit {
					log.Println("node unreachable:", commandTransfer)

					cmdID := commandTransfer.Command.CommandID
					pendingCommandLock.Lock()
					if cmd, ok := commands[cmdID]; ok {
						cmd.Ticker.Stop()
						commandLog = append(commandLog, nodes.CommandLogItem{Command: cmd.Command,
							Attempts: 0, Stamp: time.Now()})
					}
					delete(commands, cmdID)
					pendingCommandLock.Unlock()
				}
				sseServer.NodeCommandEvent <- commandList()
			}
		}()
	}
}

func handlePendingCommands(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	// encode pending commands as json and send as response
	enc := json.NewEncoder(w)
	enc.Encode(commandList())
}

func handleCommandLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	// encode pending commands as json and send as response
	enc := json.NewEncoder(w)
	enc.Encode(commandLog)
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
	nodeMap = make(map[int][]nodes.Node)
	nodeTimers = make(map[int]*time.Timer)
	commands = make(map[int]*nodes.CommandTransfer)
	commandLog = []nodes.CommandLogItem{}

	// create serial IO-channels
	serialInput = make(chan []byte)
	serialOutput = make(chan []byte)

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
	// process incoming data
	go readData(serialInput)

	// deliver outgoing data to connected serials
	go writeData(serialOutput)

	// serve static files
	fs := http.FileServer(http.Dir(serveFilesPath))

	// serve eventstream
	sseServer = sse.NewServer()

	// create a gorilla mux-router
	muxRouter := mux.NewRouter()

	// services dealing with lora-nodes
	muxRouter.Handle("/events", sseServer)
	muxRouter.HandleFunc("/nodes", handleNodes)
	muxRouter.HandleFunc("/nodes/cmd", handleNodeCommand).Methods("POST", "OPTIONS")
	muxRouter.HandleFunc("/nodes/cmd/pending", handlePendingCommands)
	muxRouter.HandleFunc("/nodes/cmd/log", handleCommandLog)
	muxRouter.HandleFunc("/nodes/{nodeID:[0-9]+}", handleNodes)
	muxRouter.HandleFunc("/nodes/{nodeID:[0-9]+}/log", handleNodes)
	muxRouter.PathPrefix("/").Handler(fs)
	http.Handle("/", muxRouter)

	port := 8080
	log.Println("server listening on port", port, " -- serving files from", serveFilesPath)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}

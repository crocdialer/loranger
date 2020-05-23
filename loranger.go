package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crocdialer/loranger/modules"
	"github.com/crocdialer/loranger/modules/nodes"
	"github.com/crocdialer/loranger/modules/sse"
	"github.com/gorilla/mux"
	client "github.com/influxdata/influxdb1-client/v2"
	"github.com/tarm/serial"
)

// http listen port
var port = 8080

// static serve directory
var serveFilesPath = "./public"

// loranger_gateway URL
var gatewayURL = "localhost"

// loranger_gateway port
var gatewayPort = 4444

// inactivity timeout for Nodes
var nodeTimeout = time.Second * 10

// do not process more commands at a time
var maxNumConcurrantCommands = 3

// retransmit timeout for commands
var commandTimeout = time.Millisecond * 2500

// maximum number of command (re-)transmits
var commandMaxNumTransmit = 10

// slice of connected serials
var serialDevices []*serial.Port

// tcp-connections
var tcpConnections []net.Conn

// node IO channels
var dataInput, dataOutput chan []byte

// deadline timers for active Nodes
var nodeTimers map[int]*time.Timer

// next command id
var nextCommandID int32 = 1

// pending commands (sent but unackknowledged)
var commandTransfers map[int]*nodes.CommandTransfer

// command channels for pending and finished commands
var commandQueue, commandsDone chan *nodes.CommandTransfer

// keep track of issued commands
var commandLog []nodes.CommandLogItem

// mutex for pending commands
var pendingCommandLock = sync.RWMutex{}

// handle for SSE-Server
var sseServer *sse.Server

// channel to write arbitrary node-data to a database
var nodeStore chan nodes.NodeEvent

func commandList() []*nodes.CommandTransfer {
	pendingCommandLock.RLock()
	defer pendingCommandLock.RUnlock()
	var cmdKeys []int

	for k := range commandTransfers {
		cmdKeys = append(cmdKeys, k)
	}
	sort.Ints(cmdKeys)
	cmdList := make([]*nodes.CommandTransfer, len(commandTransfers))

	for i, k := range cmdKeys {
		cmdList[i] = commandTransfers[k]
	}
	return cmdList
}

func readData(input chan []byte) {
	for line := range input {
		// log.Println(string(line))

		// possible types we just received
		var minimalNode nodes.MinimalNode
		var commandACK nodes.CommandACK

		if err := json.Unmarshal(line, &minimalNode); err == nil {

			var node interface{}

			if err := json.Unmarshal(line, &node); err == nil {
				// log.Println("node:", node)

				lastNodeEvent := nodes.NodeEvent{Active: true, Data: node, TimeStamp: time.Now().Local()}

				// emit SSE-event
				sseServer.NodeEvent <- lastNodeEvent

				// store in database
				nodeStore <- lastNodeEvent
			}
		} else if err := json.Unmarshal(line, &commandACK); err == nil {
			if commandACK.Ok {
				pendingCommandLock.RLock()
				// log.Println("received ACK for command:", commands[CommandACK.CommandID])
				if cmd, ok := commandTransfers[commandACK.CommandID]; ok {
					cmd.Done <- true
					commandsDone <- cmd
				}
				pendingCommandLock.RUnlock()
			}
			// emit SSE-event
			sseServer.CommandEvent <- commandList()
		} else {
			log.Println("unknown data format", string(line))
		}
	}
}

func commandQueueWorker(commands <-chan *nodes.CommandTransfer, results chan<- *nodes.CommandTransfer) {
	for cmdTransfer := range commands {
		cmdTransfer.Start(results, func() {
			sseServer.CommandEvent <- commandList()
		})
	}
}

func commandQueueCollector() {
	for cmd := range commandsDone {
		pendingCommandLock.Lock()
		commandLog = append(commandLog, nodes.CommandLogItem{Command: cmd.Command,
			Success: cmd.Success, Attempts: len(cmd.Stamps), Stamp: time.Now()})
		delete(commandTransfers, cmd.Command.CommandID)
		pendingCommandLock.Unlock()

		// emit SSE update
		sseServer.CommandEvent <- commandList()
	}
}

func nodesFromQueryResult(results []client.Result) (outNodes []nodes.NodeEvent) {

	for _, result := range results {
		if len(result.Series) > 0 {
			var cols []string

			for _, c := range result.Series[0].Columns[1:] {
				cols = append(cols, strings.Replace(c, "median_", "", -1))
			}

			for _, value := range result.Series[0].Values {

				data := make(map[string]interface{})

				// parse timestamp
				timeStamp, err := time.Parse(time.RFC3339, value[0].(string))
				timeStamp = timeStamp.Local()

				if err != nil {
					log.Println(err)
				}

				for i, v := range value[1:] {
					if v != nil {
						data[cols[i]] = v
					}
				}

				nodeEvent := nodes.NodeEvent{}
				nodeEvent.Active = true
				nodeEvent.TimeStamp = timeStamp
				nodeEvent.Data = data

				outNodes = append(outNodes, nodeEvent)
			}
		}
	}
	return outNodes
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

				// create influx-query
				query := fmt.Sprintf("SELECT median(*) FROM nodes WHERE \"address\" = '%d' AND time > now() - %s GROUP BY time(%s) fill(none)", k, duration, granularity)
				results, err := database.Query(query)

				if err != nil {
					log.Println(err)
				}

				outNodes := nodesFromQueryResult(results)

				enc.Encode(outNodes)
			} else {

				// create influx-query
				duration := 12 * time.Hour
				maxNumItems := 1
				query := fmt.Sprintf("SELECT * FROM nodes WHERE \"address\" = '%d' AND time > now() - %s ORDER BY DESC LIMIT %d", k, duration, maxNumItems)
				results, err := database.Query(query)

				if err == nil {
					// encode response
					enc.Encode(nodesFromQueryResult(results))
				} else {
					log.Println(err)
				}
			}
		}
	} else {
		// // create influx-query
		// duration := 12 * time.Hour
		// maxNumItems := 100
		// query := fmt.Sprintf("SELECT * FROM nodes WHERE time > now() - %s ORDER BY DESC LIMIT %d", duration, maxNumItems)
		// results, err := database.Query(query)
		//
		// if err == nil {
		//
		// 	// create and populate a nodeMap
		// 	var nodeMap = make(map[int][]nodes.NodeEvent)
		//
		// 	for _, nodeEvent := range nodesFromQueryResult(results) {
		// 		if castedMap, ok := nodeEvent.Data.(map[string]interface{}); ok {
		//
		// 			// retrieve address
		// 			if addStr, ok := castedMap["address"].(string); ok {
		// 				address, _ := strconv.Atoi(addStr)
		// 				nodeMap[address] = append(nodeMap[address], nodeEvent)
		// 			} else {
		// 				log.Println("address cast failed", castedMap["address"])
		// 			}
		// 		} else {
		// 			log.Println("map cast failed", nodeEvent.Data)
		// 		}
		// 	}
		//
		// 	// reply with a sorted list of all nodes' last state
		// 	var nodeKeys []int
		//
		// 	for k := range nodeMap {
		// 		nodeKeys = append(nodeKeys, k)
		// 	}
		// 	sort.Ints(nodeKeys)
		// 	nodeList := make([]nodes.NodeEvent, len(nodeMap))
		//
		// 	for i, k := range nodeKeys {
		// 		nodeList[i] = nodeMap[k][len(nodeMap[k])-1]
		// 	}
		// 	enc.Encode(nodeList)
		//
		// 	log.Println(nodeList)
		// } else {
		// 	log.Println(err)
		// }

	}
}

// POST
func handleCommand(w http.ResponseWriter, r *http.Request) {

	// configure proper CORS
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	// decode json-request
	command := &nodes.Command{}
	decoder := json.NewDecoder(r.Body)
	decoder.Decode(command)

	// insert struct-type and CommandID
	command.CommandID = int(atomic.AddInt32(&nextCommandID, 1))

	// TODO: check if the node exists
	hasNode := false

	// nodeMutex.RLock()
	// _, hasNode := nodeMap[command.Address]
	// nodeMutex.RUnlock()

	// encode json ACK and send as response
	enc := json.NewEncoder(w)
	ack := nodes.CommandACK{CommandID: command.CommandID, Ok: hasNode}
	enc.Encode(ack)

	if hasNode {
		newCmdTransfer := nodes.NewCommandTransfer(command, dataOutput, commandMaxNumTransmit,
			commandTimeout)

		// lock mutex and keep track of the command
		pendingCommandLock.Lock()
		commandTransfers[command.CommandID] = newCmdTransfer
		pendingCommandLock.Unlock()

		// insert transfer into queue
		commandQueue <- newCmdTransfer
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

func readTCP(url string, port int, output chan<- []byte) {
	for {

		conn, err := net.Dial("tcp", fmt.Sprintf("%s:%d", url, port))

		if err != nil {
			log.Printf("could not connect to %s:%d\n", url, port)
			time.Sleep(5 * time.Second)
			continue
		}

		tcpConnections = append(tcpConnections, conn)
		reader := bufio.NewReader(conn)

		for {
			bytes, err := reader.ReadBytes('\n')

			if err != nil {
				log.Println(err)

				if io.EOF == err {
					conn.Close()
					break
				}
			}
			output <- bytes
		}
	}
}

func writeData(input <-chan []byte) {
	for bytes := range input {
		for _, s := range serialDevices {
			s.Write(bytes)
			s.Flush()
		}

		for _, con := range tcpConnections {
			con.Write(bytes)
		}
	}
}

func storeNodesInDatabase(input <-chan nodes.NodeEvent) {
	for nodeEvent := range input {
		database.Insert(nodeEvent.TimeStamp, nodeEvent.Data)
	}
}

func main() {
	log.Println("welcome loranger")

	// http-server path
	flag.StringVar(&serveFilesPath, "http_dir", serveFilesPath, "http-server path")

	// http-server port
	flag.IntVar(&port, "http_port", port, "http-server port")

	// loranger-gateway url
	flag.StringVar(&gatewayURL, "gateway", gatewayURL, "loranger-gateway URL")

	// http-server port
	flag.IntVar(&gatewayPort, "gateway_port", gatewayPort, "loranger-gateway port")

	// parse commandline flags
	flag.Parse()

	// make our global state maps
	nodeTimers = make(map[int]*time.Timer)
	commandTransfers = make(map[int]*nodes.CommandTransfer)
	commandLog = []nodes.CommandLogItem{}

	// create serial IO-channels
	dataInput = make(chan []byte)
	dataOutput = make(chan []byte)

	// create command channels
	commandQueue = make(chan *nodes.CommandTransfer, 100)
	commandsDone = make(chan *nodes.CommandTransfer, 100)

	// create a channel to write node-data to a database
	nodeStore = make(chan nodes.NodeEvent)

	// read from tcp-connection
	go readTCP(gatewayURL, gatewayPort, dataInput)

	// process incoming data
	go readData(dataInput)

	// deliver outgoing data to connected serials and tcp-connections
	go writeData(dataOutput)

	// write received data from nodes to a database
	go storeNodesInDatabase(nodeStore)

	// start command processing
	for i := 0; i < maxNumConcurrantCommands; i++ {
		go commandQueueWorker(commandQueue, commandsDone)
	}
	go commandQueueCollector()

	// serve static files
	fs := http.FileServer(http.Dir(serveFilesPath))

	// serve eventstream
	sseServer = sse.NewServer()

	// create a gorilla mux-router
	muxRouter := mux.NewRouter()

	// services dealing with lora-nodes
	muxRouter.Handle("/events", sseServer)
	muxRouter.HandleFunc("/nodes", handleNodes)
	muxRouter.HandleFunc("/nodes/cmd", handleCommand).Methods("POST", "OPTIONS")
	muxRouter.HandleFunc("/nodes/cmd/pending", handlePendingCommands)
	muxRouter.HandleFunc("/nodes/cmd/log", handleCommandLog)
	muxRouter.HandleFunc("/nodes/{nodeID:[0-9]+}", handleNodes)
	muxRouter.HandleFunc("/nodes/{nodeID:[0-9]+}/log", handleNodes)
	muxRouter.PathPrefix("/").Handler(fs)
	http.Handle("/", muxRouter)

	log.Println("server listening on port", port, " -- serving files from", serveFilesPath)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}

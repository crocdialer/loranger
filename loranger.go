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

var device string
var serialDevices []*serial.Port

// create serial channels
var serialInput, serialOutput chan []byte

// nodes
var nodes map[int]Node

// Node structures information of a remote device
type Node struct {
	Address      int     `json:"address"`
	LastRssi     int     `json:"rssi"`
	Mode         int     `json:"mode"`
	Temperature  float64 `json:"temp"`
	BatteryLevel float64 `json:"bat"`
	TimeStamp    time.Time
	GpsPosition  [2]float64 `json:"gps"`
}

// NodeCommand realizes a simple RPC interface
type NodeCommand struct {
	Address int           `json:"dst"`
	Command string        `json:"cmd"`
	Params  []interface{} `json:"params"`
}

func readData(input chan []byte) {
	for line := range input {
		var node Node
		if err := json.Unmarshal(line, &node); err != nil {
			log.Println("could not parse data as json:", string(line))
		} else {
			node.TimeStamp = time.Now()
			nodes[node.Address] = node
		}
	}
}

// handle node requests:
// /nodes
// /nodes/{nodeID:[0-9]+}
func handleNodes(w http.ResponseWriter, r *http.Request) {
	log.Printf("request: %s\n", r.URL.Path[1:])
	w.Header().Set("Content-Type", "application/json")

	vars := mux.Vars(r)
	enc := json.NewEncoder(w)

	nodeID, ok := vars["nodeID"]

	if ok {
		k, _ := strconv.Atoi(nodeID)
		log.Println("nodeID: ", k)
		node, ok := nodes[k]

		if ok {
			enc.Encode(node)
		}
	} else {
		// no nodeId provided, reply with a sorted list of all nodes
		// To store the keys in slice in sorted order
		var keys []int

		for k := range nodes {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		nodeList := make([]Node, len(nodes))

		for i, k := range keys {
			nodeList[i] = nodes[k]
		}
		enc.Encode(nodeList)
	}
}

// POST
func handleNodeCommand(w http.ResponseWriter, r *http.Request) {
	// TODO: construct NodeCommand from POST-json
	recCmd := &NodeCommand{1, "record", []interface{}{10}}
	jsonStr, err := json.Marshal(recCmd)

	if err != nil {
		log.Println("could not marshal NodeCommand:", recCmd)
	} else {
		// send record command
		log.Println("sending command:", string(jsonStr))
		jsonStr = append(jsonStr, '\n')
		serialOutput <- jsonStr
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
			s.Flush()
		}
	}
}

func main() {
	log.Println("welcome loranger")

	if len(os.Args) > 1 {
		device = os.Args[1]
	}

	// init our NodeServer instance
	// nodeServer := NewNodeServer()

	nodes = make(map[int]Node)

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
	fs := http.FileServer(http.Dir("static/"))

	muxRouter := mux.NewRouter()

	// services dealing with lora-nodes
	muxRouter.HandleFunc("/nodes", handleNodes)
	muxRouter.HandleFunc("/nodes/cmd", handleNodeCommand) //.Methods("POST")
	muxRouter.HandleFunc("/nodes/{nodeID:[0-9]+}", handleNodes)
	muxRouter.PathPrefix("/").Handler(fs)
	http.Handle("/", muxRouter)

	// http.Handle("/", fs)

	port := 8080
	log.Println("server listening on", port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}

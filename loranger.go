package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/tarm/serial"
)

var device string

// define a Node type
type Node struct {
	Address      uint8   `json:"src"`
	LastRssi     int     `json:"rssi"`
	Mode         int     `json:"mode"`
	Temperature  float64 `json:"temp"`
	BatteryLevel float64 `json:"bat"`
	TimeStamp    time.Time
	GpsPosition  interface{} `json:"gps"`
}

type NodeCommand struct {
	Address uint8         `json:"dst"`
	Command string        `json:"cmd"`
	Params  []interface{} `json:"params"`
}

// define our NodeServer
type NodeServer struct {
	nodes map[uint8]Node
}

// func makeTimestamp() int64 {
// 	return time.Now().UnixNano() / int64(time.Millisecond)
// }

// parse something like this: {"src":1,"dst":0,"rssi":-25,"state":{"mode":0,"bat":1}}
func parseNode(input []byte) (err error, outNode Node) {
	// var json_root interface{}
	err = json.Unmarshal(input, &outNode)
	outNode.TimeStamp = time.Now()
	return err, outNode
}

func NewNodeServer() *NodeServer {
	newServer := &NodeServer{}
	newServer.nodes = make(map[uint8]Node)
	return newServer
}

func (b *NodeServer) readData(input chan []byte) {
	for line := range input {
		if err, node := parseNode(line); err != nil {
			log.Println("could not parse data as json:", string(line))
		} else {
			b.nodes[node.Address] = node
			out_json, _ := json.Marshal(b.nodes)
			log.Println(string(out_json))
		}
	}
}

func (b *NodeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// fmt.Fprintf(w, "Hi there, I love %s!", r.URL.Path[1:])
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(fmt.Sprint(b.nodes)))
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

func main() {
	log.Println("welcome loranger")

	if len(os.Args) > 1 {
		device = os.Args[1]
	}

	// init our NodeServer instance
	node_server := NewNodeServer()

	// create channel
	serial_input := make(chan []byte, 100)

	files, err := ioutil.ReadDir("/dev")
	if err != nil {
		log.Fatal(err)
	}

	for _, f := range files {
		device_name := "/dev/" + f.Name()

		if strings.Contains(device_name, "ttyACM") || strings.Contains(device_name, "tty.usb") {
			c := &serial.Config{Name: device_name, Baud: 115200}
			s, err := serial.OpenPort(c)
			if err != nil {
				log.Fatal(err)
				return
			}
			defer s.Close()

			log.Println("reading from", device_name)

			// producer feeds lines into channel
			go readSerial(s, serial_input)
		}
	}

	// consumer
	go node_server.readData(serial_input)

	port := 8080
	log.Println("server listening on", port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), node_server))
}

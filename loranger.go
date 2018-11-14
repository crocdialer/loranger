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

// Node structures information of a remote device
type Node struct {
	Address      uint8   `json:"src"`
	LastRssi     int     `json:"rssi"`
	Mode         int     `json:"mode"`
	Temperature  float64 `json:"temp"`
	BatteryLevel float64 `json:"bat"`
	TimeStamp    time.Time
	GpsPosition  interface{} `json:"gps"`
}

// NodeCommand realizes a simple RPC interface
type NodeCommand struct {
	Address uint8         `json:"dst"`
	Command string        `json:"cmd"`
	Params  []interface{} `json:"params"`
}

// NodeServer holds information of all remote devices that it had contact with
type NodeServer struct {
	nodes map[uint8]Node
}

// NewNodeServer creates an initialzed instance
func NewNodeServer() *NodeServer {
	newServer := &NodeServer{}
	newServer.nodes = make(map[uint8]Node)
	return newServer
}

// // parse something like this: {"src":1,"dst":0,"rssi":-25,"state":{"mode":0,"bat":1}}
// func parseNode(input []byte) (err error, outNode Node) {
// 	// var json_root interface{}
// 	err = json.Unmarshal(input, &outNode)
// 	outNode.TimeStamp = time.Now()
// 	return err, outNode
// }

func (b *NodeServer) readData(input chan []byte) {
	for line := range input {
		var node Node
		if err := json.Unmarshal(line, &node); err != nil {
			log.Println("could not parse data as json:", string(line))
		} else {
			node.TimeStamp = time.Now()
			b.nodes[node.Address] = node
			outJSON, _ := json.Marshal(b.nodes)
			log.Println(string(outJSON))
		}
	}
}

func (b *NodeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// fmt.Fprintf(w, "Hi there, I love %s!", r.URL.Path[1:])
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.Encode(b.nodes)
	// w.Write([]byte(fmt.Sprint(b.nodes)))
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
	nodeServer := NewNodeServer()

	// create channel
	serialInput := make(chan []byte, 100)

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

			log.Println("reading from", deviceName)

			// producer feeds lines into channel
			go readSerial(s, serialInput)
		}
	}

	// consumer
	go nodeServer.readData(serialInput)

	port := 8080
	log.Println("server listening on", port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nodeServer))
}

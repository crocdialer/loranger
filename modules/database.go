package database

import (
	"log"
	"time"

	_ "github.com/influxdata/influxdb1-client" // this is important because of the bug in go mod
	client "github.com/influxdata/influxdb1-client/v2"
)

const (
	// LorangerDB specifies the name of the underlying influx database
	LorangerDB = "loranger_influx"
)

// Insert saves points to database
func Insert(node interface{}) {
	c, err := client.NewHTTPClient(client.HTTPConfig{
		Addr: "http://localhost:8086",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	// Create a new point batch
	bp, err := client.NewBatchPoints(client.BatchPointsConfig{
		Database:  LorangerDB,
		Precision: "s",
	})
	if err != nil {
		log.Fatal(err)
	}

	// Create a point and add to batch
	m, ok := node.(map[string]interface{})

	if !ok {
		log.Fatal("want type map[string]interface{};  got %T", node)
	}

	tags := map[string]string{"device-type": m["type"].(string)}
	fields := m

	pt, err := client.NewPoint("nodes", tags, fields, time.Now())
	if err != nil {
		log.Fatal(err)
	}
	bp.AddPoint(pt)

	// Write the batch
	if err := c.Write(bp); err != nil {
		log.Fatal(err)
	}

	// Close client resources
	if err := c.Close(); err != nil {
		log.Fatal(err)
	}
}

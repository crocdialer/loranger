package database

import (
	"fmt"
	"log"
	"time"

	_ "github.com/influxdata/influxdb1-client" // this is important because of the bug in go mod
	client "github.com/influxdata/influxdb1-client/v2"
)

const (
	// LorangerDB specifies the name of the underlying influx database
	LorangerDB = "loranger_influx"

	// Address specifies the url to connect to
	Address = "http://localhost:8086"
)

// Insert saves points to database
func Insert(timeStamp time.Time, node interface{}) {
	c, err := client.NewHTTPClient(client.HTTPConfig{
		Addr: Address,
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

	tags := map[string]string{"type": m["type"].(string), "address": fmt.Sprintf("%v", m["address"])}

	fields := make(map[string]interface{})

	for k, v := range m {
		fields[k] = v
	}
	delete(fields, "type")
	delete(fields, "address")

	pt, err := client.NewPoint("nodes", tags, fields, timeStamp)
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

// Query is a convenience function to query the database
func Query(cmd string) (res []client.Result, err error) {
	q := client.Query{
		Command:  cmd,
		Database: LorangerDB,
	}
	c, err := client.NewHTTPClient(client.HTTPConfig{
		Addr: Address,
	})
	if response, err := c.Query(q); err == nil {
		if response.Error() != nil {
			return res, response.Error()
		}
		res = response.Results
	} else {
		return res, err
	}
	return res, nil
}

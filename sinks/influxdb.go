package sinks

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/golang/glog"
	influxdb "github.com/influxdb/influxdb/client"
	"github.com/jimmidyson/jadvisor/sources"
)

var (
	argBufferDuration = flag.Duration("sink_influxdb_buffer_duration", 10*time.Second, "Time duration for which stats should be buffered in influxdb sink before being written as a single transaction")
	argDbUsername     = flag.String("sink_influxdb_username", "root", "InfluxDB username")
	argDbPassword     = flag.String("sink_influxdb_password", "root", "InfluxDB password")
	argDbHost         = flag.String("sink_influxdb_host", "localhost:8086", "InfluxDB host:port")
	argDbName         = flag.String("sink_influxdb_name", "k8s", "Influxdb database name")
)

type InfluxdbSink struct {
	client         *influxdb.Client
	series         []*influxdb.Series
	dbName         string
	bufferDuration time.Duration
	lastWrite      time.Time
}

func (self *InfluxdbSink) containerStatsToValues(pod *sources.Pod, hostname, containerName string, stat *sources.JolokiaStats) (columns []string, values []interface{}) {
	// Timestamp
	columns = append(columns, colTimestamp)
	values = append(values, stat.Timestamp.Unix())

	if pod != nil {
		// Pod name
		columns = append(columns, colPodName)
		values = append(values, pod.Name)

		// Pod Status
		columns = append(columns, colPodStatus)
		values = append(values, pod.Status)

		// Pod IP
		columns = append(columns, colPodIP)
		values = append(values, pod.PodIP)

		labels := []string{}
		for key, value := range pod.Labels {
			labels = append(labels, fmt.Sprintf("%s:%s", key, value))
		}
		columns = append(columns, colLabels)
		values = append(values, strings.Join(labels, ","))
	}

	// Hostname
	columns = append(columns, colHostName)
	values = append(values, hostname)

	// Container name
	columns = append(columns, colContainerName)
	values = append(values, containerName)

	columns = append(columns, colHeapUsageCommitted)
	values = append(values, stat.Memory.HeapUsage.Committed)
	columns = append(columns, colHeapUsageInit)
	values = append(values, stat.Memory.HeapUsage.Init)
	columns = append(columns, colHeapUsageMax)
	values = append(values, stat.Memory.HeapUsage.Max)
	columns = append(columns, colHeapUsageUsed)
	values = append(values, stat.Memory.HeapUsage.Used)

	columns = append(columns, colNonHeapUsageCommitted)
	values = append(values, stat.Memory.NonHeapUsage.Committed)
	columns = append(columns, colNonHeapUsageInit)
	values = append(values, stat.Memory.NonHeapUsage.Init)
	columns = append(columns, colNonHeapUsageMax)
	values = append(values, stat.Memory.NonHeapUsage.Max)
	columns = append(columns, colNonHeapUsageUsed)
	values = append(values, stat.Memory.NonHeapUsage.Used)

	return
}

// Returns a new influxdb series.
func (self *InfluxdbSink) newSeries(tableName string, columns []string, points []interface{}) *influxdb.Series {
	out := &influxdb.Series{
		Name:    tableName,
		Columns: columns,
		// There's only one point for each stats
		Points: make([][]interface{}, 1),
	}
	out.Points[0] = points
	return out
}

func (self *InfluxdbSink) handlePods(pods []sources.Pod) {
	for _, pod := range pods {
		for _, container := range pod.Containers {
			col, val := self.containerStatsToValues(&pod, pod.Hostname, container.Name, container.Stats)
			self.series = append(self.series, self.newSeries(statsTable, col, val))
		}
	}
}

func (self *InfluxdbSink) readyToFlush() bool {
	return time.Since(self.lastWrite) >= self.bufferDuration
}

func (self *InfluxdbSink) StoreData(ip Data) error {
	var seriesToFlush []*influxdb.Series
	if data, ok := ip.(sources.ContainerData); ok {
		self.handlePods(data.Pods)
	} else {
		return fmt.Errorf("Requesting unrecognized type to be stored in InfluxDB")
	}
	if self.readyToFlush() {
		seriesToFlush = self.series
		self.series = make([]*influxdb.Series, 0)
		self.lastWrite = time.Now()
	}

	if len(seriesToFlush) > 0 {
		glog.V(2).Info("flushed data to influxdb sink")
		// TODO(vishh): Do writes in a separate thread.
		err := self.client.WriteSeriesWithTimePrecision(seriesToFlush, influxdb.Second)
		if err != nil {
			glog.Errorf("failed to write stats to influxDb - %s", err)
		}
	}

	return nil
}

func NewInfluxdbSink() (Sink, error) {
	config := &influxdb.ClientConfig{
		Host:     os.ExpandEnv(*argDbHost),
		Username: *argDbUsername,
		Password: *argDbPassword,
		Database: *argDbName,
		IsSecure: false,
	}
	client, err := influxdb.NewClient(config)
	if err != nil {
		return nil, err
	}
	client.DisableCompression()
	if err := client.CreateDatabase(*argDbName); err != nil {
		glog.Infof("Database creation failed - %s", err)
	}
	// Create the database if it does not already exist. Ignore errors.
	return &InfluxdbSink{
		client:         client,
		series:         make([]*influxdb.Series, 0),
		dbName:         *argDbName,
		bufferDuration: *argBufferDuration,
		lastWrite:      time.Now(),
	}, nil
}

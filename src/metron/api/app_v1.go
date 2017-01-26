package api

import (
	"fmt"
	"log"
	"math/rand"
	clientpool "metron/clientpool/v1"
	"metron/config"
	"metron/eventwriter"
	"metron/legacyclientpool"
	"metron/networkreader"
	"metron/writers/dopplerforwarder"
	"metron/writers/eventmarshaller"
	"metron/writers/eventunmarshaller"
	"metron/writers/messageaggregator"
	"metron/writers/tagger"
	"plumbing"
	"time"

	"github.com/cloudfoundry/dropsonde/metric_sender"
	"github.com/cloudfoundry/dropsonde/metricbatcher"
	"github.com/cloudfoundry/dropsonde/metrics"
	"github.com/cloudfoundry/dropsonde/runtime_stats"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type AppV1 struct {
	config *config.Config
}

func NewV1App(c *config.Config) *AppV1 {
	return &AppV1{config: c}
}

func (a *AppV1) Start() {
	if a.config.DisableUDP {
		return
	}

	statsStopChan := make(chan struct{})
	batcher, eventWriter := a.initializeMetrics(statsStopChan)

	log.Print("Startup: Setting up the Metron agent")
	marshaller, err := a.initializeV1DopplerPool(batcher)
	if err != nil {
		log.Panic(fmt.Errorf("Could not initialize doppler connection pool: %s", err))
	}

	messageTagger := tagger.New(a.config.Deployment, a.config.Job, a.config.Index, marshaller)
	aggregator := messageaggregator.New(messageTagger)
	eventWriter.SetWriter(aggregator)

	dropsondeUnmarshaller := eventunmarshaller.New(aggregator, batcher)
	metronAddress := fmt.Sprintf("127.0.0.1:%d", a.config.IncomingUDPPort)
	dropsondeReader, err := networkreader.New(metronAddress, "dropsondeAgentListener", dropsondeUnmarshaller)
	if err != nil {
		log.Panic(fmt.Errorf("Failed to listen on %s: %s", metronAddress, err))
	}

	log.Print("metron v1 API started")
	dropsondeReader.Start()
}

func (a *AppV1) initializeMetrics(stopChan chan struct{}) (*metricbatcher.MetricBatcher, *eventwriter.EventWriter) {
	eventWriter := eventwriter.New("MetronAgent")
	metricSender := metric_sender.NewMetricSender(eventWriter)
	metricBatcher := metricbatcher.New(metricSender, time.Duration(a.config.MetricBatchIntervalMilliseconds)*time.Millisecond)
	metrics.Initialize(metricSender, metricBatcher)

	stats := runtime_stats.NewRuntimeStats(eventWriter, time.Duration(a.config.RuntimeStatsIntervalMilliseconds)*time.Millisecond)
	go stats.Run(stopChan)
	return metricBatcher, eventWriter
}

func (a *AppV1) initializeV1DopplerPool(batcher *metricbatcher.MetricBatcher) (*eventmarshaller.EventMarshaller, error) {
	pools := a.setupGRPC()

	// TODO: delete this legacy pool stuff when UDP goes away
	legacyPool := legacyclientpool.New(a.config.DopplerAddrUDP, 100, 5*time.Second)
	udpWrapper := dopplerforwarder.NewUDPWrapper(legacyPool, []byte(a.config.SharedSecret))
	pools = append(pools, udpWrapper)

	combinedPool := legacyclientpool.NewCombinedPool(pools...)

	marshaller := eventmarshaller.New(batcher)
	marshaller.SetWriter(combinedPool)

	return marshaller, nil
}

func (a *AppV1) setupGRPC() []legacyclientpool.Pool {
	tlsConfig, err := plumbing.NewMutualTLSConfig(
		a.config.GRPC.CertFile,
		a.config.GRPC.KeyFile,
		a.config.GRPC.CAFile,
		"doppler",
	)
	if err != nil {
		log.Printf("Failed to load TLS config: %s", err)
		return nil
	}

	connector := clientpool.MakeGRPCConnector(
		a.config.DopplerAddr,
		a.config.Zone,
		grpc.Dial,
		plumbing.NewDopplerIngestorClient,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
	)

	var connManagers []clientpool.Conn
	for i := 0; i < 5; i++ {
		connManagers = append(connManagers, clientpool.NewConnManager(connector, 10000+rand.Int63n(1000)))
	}

	pool := clientpool.New(connManagers...)
	grpcWrapper := dopplerforwarder.NewGRPCWrapper(pool)
	return []legacyclientpool.Pool{grpcWrapper}
}

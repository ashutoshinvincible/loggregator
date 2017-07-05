package egress

import (
	"errors"
	"fmt"
	"io"
	"log"
	"sync/atomic"

	"code.cloudfoundry.org/loggregator/metricemitter"

	v2 "code.cloudfoundry.org/loggregator/plumbing/v2"

	"golang.org/x/net/context"
)

type HealthRegistrar interface {
	Inc(name string)
	Dec(name string)
}

type Receiver interface {
	Receive(ctx context.Context, req *v2.EgressRequest) (rx func() (*v2.Envelope, error), err error)
}

type Server struct {
	activeConnections int64
	receiver          Receiver
	egressMetric      *metricemitter.CounterMetric
	droppedMetric     *metricemitter.CounterMetric
	health            HealthRegistrar
	ctx               context.Context
}

func NewServer(
	r Receiver,
	m metricemitter.MetricClient,
	h HealthRegistrar,
	c context.Context,
) *Server {
	egressMetric := m.NewCounterMetric("egress",
		metricemitter.WithVersion(2, 0),
	)

	droppedMetric := m.NewCounterMetric("dropped",
		metricemitter.WithVersion(2, 0),
		metricemitter.WithTags(map[string]string{
			"direction": "egress",
		}),
	)

	return &Server{
		receiver:      r,
		egressMetric:  egressMetric,
		droppedMetric: droppedMetric,
		health:        h,
		ctx:           c,
	}
}

func (s *Server) Receiver(r *v2.EgressRequest, srv v2.Egress_ReceiverServer) error {
	s.health.Inc("subscriptionCount")
	defer s.health.Dec("subscriptionCount")
	activeConnections := atomic.AddInt64(&s.activeConnections, 1)
	defer atomic.AddInt64(&s.activeConnections, -1)

	if activeConnections > 500 {
		return errors.New("We have too many connections!")
	}

	if r.GetFilter() != nil &&
		r.GetFilter().SourceId == "" &&
		r.GetFilter().Message != nil {
		return errors.New("invalid request: cannot have type filter without source id")
	}

	ctx, cancel := context.WithCancel(srv.Context())
	defer cancel()

	buffer := make(chan *v2.Envelope, 10000)

	go func() {
		select {
		case <-s.ctx.Done():
			cancel()
		case <-ctx.Done():
			cancel()
		}
	}()

	rx, err := s.receiver.Receive(ctx, r)
	if err != nil {
		log.Printf("Unable to setup subscription: %s", err)
		return fmt.Errorf("unable to setup subscription")
	}

	go s.consumeReceiver(buffer, rx, cancel)

	for data := range buffer {
		if err := srv.Send(data); err != nil {
			log.Printf("Send error: %s", err)
			return io.ErrUnexpectedEOF
		}

		// metric-documentation-v2: (loggregator.rlp.egress) Number of v2
		// envelopes sent to RLP consumers.
		s.egressMetric.Increment(1)
	}

	return nil
}

func (s *Server) Alert(missed int) {
	// metric-documentation-v2: (loggregator.rlp.dropped) Number of v2
	// envelopes dropped while egressing to a consumer.
	s.droppedMetric.Increment(uint64(missed))
	log.Printf("Dropped (egress) %d envelopes", missed)
}

func (s *Server) consumeReceiver(
	buffer chan<- *v2.Envelope,
	rx func() (*v2.Envelope, error),
	cancel func(),
) {

	defer cancel()
	defer close(buffer)

	for {
		e, err := rx()
		if err == io.EOF {
			break
		}

		if err != nil {
			log.Printf("Subscribe error: %s", err)
			break
		}

		select {
		case buffer <- e:
		default:
			s.droppedMetric.Increment(1)
		}
	}
}

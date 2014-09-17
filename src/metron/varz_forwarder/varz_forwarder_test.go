package varz_forwarder_test

import (
	"github.com/cloudfoundry/dropsonde/events"
	"metron/varz_forwarder"

	"github.com/cloudfoundry/loggregatorlib/cfcomponent/instrumentation"
	"github.com/cloudfoundry/loggregatorlib/loggertesthelper"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"time"
)

var _ = Describe("VarzForwarder", func() {
	var (
		forwarder  *varz_forwarder.VarzForwarder
		metricChan chan *events.Envelope
		outputChan chan *events.Envelope
	)

	BeforeEach(func() {
		forwarder = varz_forwarder.NewVarzForwarder("test-component", time.Millisecond*100, loggertesthelper.Logger())
		metricChan = make(chan *events.Envelope)
		outputChan = make(chan *events.Envelope, 2)
	})

	var perform = func() {
		go forwarder.Run(metricChan, outputChan)
	}

	Describe("Emit", func() {
		It("includes metrics for each ValueMetric sent in", func() {
			perform()
			metricChan <- metric("origin-1", "metric", 0)
			metricChan <- metric("origin-2", "metric", 0)

			var varz instrumentation.Context
			Eventually(func() []instrumentation.Metric { varz = forwarder.Emit(); return varz.Metrics }).Should(HaveLen(2))
			Expect(findMetricByName(varz.Metrics, "origin-1.metric")).ToNot(BeNil())
			Expect(findMetricByName(varz.Metrics, "origin-2.metric")).ToNot(BeNil())
		})

		It("includes metrics for each ValueMetric name in a given origin", func() {
			perform()
			metricChan <- metric("origin", "metric-1", 1)
			metricChan <- metric("origin", "metric-2", 2)

			var varz instrumentation.Context
			Eventually(func() []instrumentation.Metric { varz = forwarder.Emit(); return varz.Metrics }).Should(HaveLen(2))
			metric1 := findMetricByName(varz.Metrics, "origin.metric-1")
			Expect(metric1.Value).To(BeNumerically("==", 1))

			metric2 := findMetricByName(varz.Metrics, "origin.metric-2")
			Expect(metric2.Value).To(BeNumerically("==", 2))
		})

		It("increments value for each CounterEvent name in a given origin", func() {
			perform()
			metricChan <- counterEvent("origin-0", "metric-1")
			metricChan <- counterEvent("origin-0", "metric-1")
			metricChan <- counterEvent("origin-1", "metric-1")

			var varz instrumentation.Context
			Eventually(func() []instrumentation.Metric { varz = forwarder.Emit(); return varz.Metrics }).Should(HaveLen(2))

			metric1 := findMetricByName(varz.Metrics, "origin-0.metric-1")
			Expect(metric1.Value).To(BeNumerically("==", 2))

			metric2 := findMetricByName(varz.Metrics, "origin-1.metric-1")
			Expect(metric2.Value).To(BeNumerically("==", 1))
		})

		It("includes the VM name as a tag on each metric", func() {
			perform()
			metricChan <- metric("origin", "metric", 1)

			var varz instrumentation.Context
			Eventually(func() []instrumentation.Metric { varz = forwarder.Emit(); return varz.Metrics }).Should(HaveLen(1))
			metric := findMetricByName(varz.Metrics, "origin.metric")
			Expect(metric.Tags["component"]).To(Equal("test-component"))
		})

		It("ignores non-ValueMetric messages", func() {
			perform()

			metricChan <- metric("origin", "metric-1", 0)
			metricChan <- heartbeat("origin")

			var varz instrumentation.Context
			Consistently(func() []instrumentation.Metric { varz = forwarder.Emit(); return varz.Metrics }).Should(HaveLen(1))
		})

		It("no longer emits metrics when the origin TTL expires", func() {
			perform()

			metricChan <- metric("origin", "metric-X", 0)

			Eventually(func() []instrumentation.Metric { return forwarder.Emit().Metrics }).ShouldNot(HaveLen(0))

			time.Sleep(time.Millisecond * 200)

			Expect(forwarder.Emit().Metrics).To(HaveLen(0))
		})

		It("still emits metrics after origin TTL if new events were received", func() {
			perform()

			metricChan <- metric("origin", "metric-X", 0)

			stopHeartbeats := make(chan struct{})
			heartbeatsStopped := make(chan struct{})
			go func() {
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
					case <-stopHeartbeats:
						close(heartbeatsStopped)
						return
					}
					metricChan <- heartbeat("origin")
					<-outputChan
				}
			}()

			Eventually(func() []instrumentation.Metric { return forwarder.Emit().Metrics }).ShouldNot(HaveLen(0))

			time.Sleep(time.Millisecond * 200)

			Expect(forwarder.Emit().Metrics).ToNot(HaveLen(0))
			close(stopHeartbeats)
			<-heartbeatsStopped
		})
	})

	Describe("Run", func() {
		It("passes ValueMetrics through", func() {
			perform()
			expectedMetric := metric("origin", "metric", 0)
			metricChan <- expectedMetric

			Eventually(outputChan).Should(Receive(Equal(expectedMetric)))
		})

		It("passes other metrics through", func() {
			perform()
			expectedMetric := heartbeat("origin")
			metricChan <- expectedMetric

			Eventually(outputChan).Should(Receive(Equal(expectedMetric)))
		})
	})
})

func metric(origin, name string, value float64) *events.Envelope {
	return &events.Envelope{
		Origin:      &origin,
		EventType:   events.Envelope_ValueMetric.Enum(),
		ValueMetric: &events.ValueMetric{Name: &name, Value: &value},
	}
}

func counterEvent(origin, name string) *events.Envelope {
	return &events.Envelope{
		Origin:       &origin,
		EventType:    events.Envelope_CounterEvent.Enum(),
		CounterEvent: &events.CounterEvent{Name: &name},
	}
}

func heartbeat(origin string) *events.Envelope {
	return &events.Envelope{
		Origin:    &origin,
		EventType: events.Envelope_Heartbeat.Enum(),
		Heartbeat: &events.Heartbeat{},
	}
}

func findMetricByName(metrics []instrumentation.Metric, metricName string) *instrumentation.Metric {

	for _, metric := range metrics {
		if metric.Name == metricName {
			return &metric
		}
	}

	return nil
}

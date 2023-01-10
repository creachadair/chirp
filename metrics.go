package chirp

import "expvar"

// peerMetrics record peer activity counters.
var peerMetrics struct {
	packetRecv    expvar.Int
	packetSent    expvar.Int
	packetDropped expvar.Int
	callIn        expvar.Int // number of inbound calls received
	callInErr     expvar.Int // number of inbound calls reporting an error
	callOut       expvar.Int // number of outbound calls initiated
	callOutErr    expvar.Int // number of outbound calls reporting an error
	cancelIn      expvar.Int // number of cancellations received
	callActive    expvar.Int // inbound
	callPending   expvar.Int // outbound

	emap *expvar.Map
}

func init() {
	m := new(expvar.Map)

	m.Set("packets_received", &peerMetrics.packetRecv)
	m.Set("packets_sent", &peerMetrics.packetSent)
	m.Set("packets_dropped", &peerMetrics.packetDropped)
	m.Set("calls_in", &peerMetrics.callIn)
	m.Set("calls_in_failed", &peerMetrics.callInErr)
	m.Set("calls_active", &peerMetrics.callActive)
	m.Set("calls_out", &peerMetrics.callOut)
	m.Set("calls_out_failed", &peerMetrics.callOutErr)
	m.Set("cancels_in", &peerMetrics.cancelIn)
	m.Set("calls_pending", &peerMetrics.callPending)

	peerMetrics.emap = m
}

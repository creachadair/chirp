// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

package chirp

import "expvar"

// peerMetrics record peer activity counters.
type peerMetrics struct {
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

var rootMetrics peerMetrics

func init() {
	m := new(expvar.Map)

	m.Set("packets_received", &rootMetrics.packetRecv)
	m.Set("packets_sent", &rootMetrics.packetSent)
	m.Set("packets_dropped", &rootMetrics.packetDropped)
	m.Set("calls_in", &rootMetrics.callIn)
	m.Set("calls_in_failed", &rootMetrics.callInErr)
	m.Set("calls_active", &rootMetrics.callActive)
	m.Set("calls_out", &rootMetrics.callOut)
	m.Set("calls_out_failed", &rootMetrics.callOutErr)
	m.Set("cancels_in", &rootMetrics.cancelIn)
	m.Set("calls_pending", &rootMetrics.callPending)

	rootMetrics.emap = m
}

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

var rootMetrics = newPeerMetrics()

func newPeerMetrics() *peerMetrics {
	pm := &peerMetrics{emap: new(expvar.Map)}
	pm.emap.Set("packets_received", &pm.packetRecv)
	pm.emap.Set("packets_sent", &pm.packetSent)
	pm.emap.Set("packets_dropped", &pm.packetDropped)
	pm.emap.Set("calls_in", &pm.callIn)
	pm.emap.Set("calls_in_failed", &pm.callInErr)
	pm.emap.Set("calls_active", &pm.callActive)
	pm.emap.Set("calls_out", &pm.callOut)
	pm.emap.Set("calls_out_failed", &pm.callOutErr)
	pm.emap.Set("cancels_in", &pm.cancelIn)
	pm.emap.Set("calls_pending", &pm.callPending)
	return pm
}

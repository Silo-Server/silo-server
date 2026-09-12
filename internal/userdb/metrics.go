package userdb

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var poolOpen = promauto.NewGauge(prometheus.GaugeOpts{Name: "streamapp_userdb_pool_open", Help: "Number of open user database pool connections."})
var poolEvictions = promauto.NewCounter(prometheus.CounterOpts{Name: "streamapp_userdb_pool_evictions_total", Help: "Total number of user database pool evictions."})

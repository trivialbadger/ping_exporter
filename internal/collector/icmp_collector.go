package collector

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/linode-obs/ping_exporter/internal/metrics"
	probing "github.com/prometheus-community/pro-bing"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
)

type pingParams struct {
	target   string
	timeout  time.Duration
	interval time.Duration
	count    int
	size     int
	ttl      int
	protocol string
	packet   string
}

func parseParams(r *http.Request) pingParams {
	params := r.URL.Query()

	const (
		defaultTimeout  = time.Second * 10
		defaultInterval = time.Second
		defaultCount    = 5
		defaultSize     = 56
		defaultTTL      = 64
		defaultProtocol = "ip4"  // or ip6
		defaultPacket   = "icmp" // or udp
		maxPacketSize   = 65507
		minPacketSize   = 24
	)

	p := pingParams{
		target:   params.Get("target"),
		timeout:  defaultTimeout,
		interval: defaultInterval,
		count:    defaultCount,
		size:     defaultSize,
		ttl:      defaultTTL,
		protocol: defaultProtocol,
		packet:   defaultPacket,
	}

	for k, v := range params {
		switch strings.ToLower(k) {
		case "target":
			p.target = v[0]
		case "timeout":
			if duration, err := time.ParseDuration(v[0]); err == nil {
				p.timeout = duration
			} else {
				log.Errorf("Expected duration in seconds (e.g., 5s). Got: %v", v[0])
			}
		case "interval":
			if duration, err := time.ParseDuration(v[0]); err == nil {
				p.interval = duration
			} else {
				log.Warnf("Expected duration in seconds (e.g., 5s). Got: %v. Using default 1s.", v[0])
			}
		case "count":
			if count, err := strconv.Atoi(v[0]); err == nil && count > 0 {
				p.count = count
			} else {
				p.count = defaultCount
			}
		case "size":
			if size, err := strconv.Atoi(v[0]); err == nil && size <= maxPacketSize && size >= minPacketSize {
				p.size = size
			} else {
				p.size = defaultSize
				log.Warnf("Received request for illegal packet size %v, reducing to %v", size, defaultSize)
			}
		case "ttl":
			if ttl, err := strconv.Atoi(v[0]); err == nil {
				p.ttl = ttl
			} else {
				p.ttl = defaultTTL
			}
		case "protocol", "prot":
			if strings.ToLower(v[0]) != "" {
				p.protocol = strings.ToLower(v[0])
			} else {
				p.protocol = defaultProtocol
			}
		case "packet":
			if strings.ToLower(v[0]) != "" {
				p.packet = strings.ToLower(v[0])
			} else {
				p.packet = defaultPacket
			}
		}

	}

	return p
}

func serveMetricsWithError(w http.ResponseWriter, r *http.Request, registry *prometheus.Registry) {
	if h := promhttp.HandlerFor(registry, promhttp.HandlerOpts{}); h != nil {
		h.ServeHTTP(w, r)
	}
}

func PingHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		const (
			namespace = "ping"
		)

		var (
			pingSuccessGauge = prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "success",
				Help:      "Returns whether the ping succeeded",
			})
			pingTimeoutGauge = prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "timeout",
				Help:      "Returns whether the ping failed by timeout",
			})
			probeDurationGauge = prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "duration_seconds",
				Help:      "Returns how long the probe took to complete in seconds",
			})
			minGauge = prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "rtt_min_seconds",
				Help:      "Best round trip time",
			})
			maxGauge = prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "rtt_max_seconds",
				Help:      "Worst round trip time",
			})
			avgGauge = prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "rtt_avg_seconds",
				Help:      "Mean round trip time",
			})
			stddevGauge = prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "rtt_std_deviation",
				Help:      "Standard deviation",
			})
			lossGauge = prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "loss_ratio",
				Help:      "Packet loss from 0 to 100",
			})
		)

		metrics := metrics.PingMetrics{
			PingSuccessGauge:   pingSuccessGauge,
			PingTimeoutGauge:   pingTimeoutGauge,
			ProbeDurationGauge: probeDurationGauge,
			MinGauge:           minGauge,
			MaxGauge:           maxGauge,
			AvgGauge:           avgGauge,
			StddevGauge:        stddevGauge,
			LossGauge:          lossGauge,
		}
		registry := prometheus.NewRegistry()

		registry.MustRegister(metrics.PingSuccessGauge, metrics.PingTimeoutGauge, metrics.ProbeDurationGauge, metrics.MinGauge, metrics.MaxGauge, metrics.AvgGauge, metrics.StddevGauge, metrics.LossGauge)

		p := parseParams(r)
		start := time.Now()

		log.Debugf("Request received with parameters: target=%v, count=%v, size=%v, interval=%v, timeout=%v, ttl=%v, packet=%v",
			p.target, p.count, p.size, p.interval, p.timeout, p.ttl, p.packet)

		pinger := probing.New(p.target)

		pinger.Count = p.count
		pinger.Size = p.size
		pinger.Interval = p.interval
		pinger.Timeout = p.timeout
		pinger.TTL = p.ttl

		if p.packet == "icmp" {
			pinger.SetPrivileged(true)
		} else {
			pinger.SetPrivileged(false)
		}

		if p.protocol == "v6" || p.protocol == "6" || p.protocol == "ip6" {
			pinger.SetNetwork("ip6")
		} else {
			pinger.SetNetwork("ip4")
		}

		pinger.OnFinish = func(stats *probing.Statistics) {
			log.Debugf("OnFinish: target=%v, PacketsSent=%d, PacketsRecv=%d, PacketLoss=%f%%, MinRtt=%v, AvgRtt=%v, MaxRtt=%v, StdDevRtt=%v, Duration=%v",
				stats.IPAddr, pinger.PacketsSent, pinger.PacketsRecv, stats.PacketLoss, stats.MinRtt, stats.AvgRtt, stats.MaxRtt, stats.StdDevRtt, time.Since(start))

			if pinger.PacketsRecv > 0 && pinger.Timeout > time.Since(start) {
				log.Debugf("Ping successful: target=%v", stats.IPAddr)
				metrics.PingSuccessGauge.Set(1)
				metrics.PingTimeoutGauge.Set(0)
			} else if pinger.Timeout < time.Since(start) {
				log.Infof("Ping timeout: target=%v, timeout=%v, duration=%v", stats.IPAddr, pinger.Timeout, time.Since(start))
				metrics.PingTimeoutGauge.Set(1)
				metrics.PingSuccessGauge.Set(0)
			} else if pinger.PacketsRecv == 0 {
				log.Infof("Ping failed, no packets received: target=%v, packetsRecv=%v, packetsSent=%v", stats.IPAddr, pinger.PacketsRecv, pinger.PacketsSent)
				metrics.PingSuccessGauge.Set(0)
				metrics.PingTimeoutGauge.Set(0)
			}

			metrics.MinGauge.Set(stats.MinRtt.Seconds())
			metrics.AvgGauge.Set(stats.AvgRtt.Seconds())
			metrics.MaxGauge.Set(stats.MaxRtt.Seconds())
			metrics.StddevGauge.Set(float64(stats.StdDevRtt))
			metrics.LossGauge.Set(stats.PacketLoss)
			metrics.ProbeDurationGauge.Set(time.Since(start).Seconds())
		}

		if err := pinger.Run(); err != nil {
			log.Error("Failed to ping target host:", err)
		}
		serveMetricsWithError(w, r, registry)
	}
}

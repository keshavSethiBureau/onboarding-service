package controller

import (
	"strconv"
	"time"

	"github.com/Bureau-Inc/bureau-commons-go/metricx"
	"github.com/gin-gonic/gin"
)

// MetricsMiddleware registers the framework-level HTTP metrics on registry and
// returns a Gin middleware that records, for every request, a count and a
// latency observation labeled by method, matched route and status code.
//
// No business-specific metrics live here — only the request framework. The
// /metrics scrape endpoint is excluded so scrapes don't inflate the counts.
func MetricsMiddleware(registry *metricx.Registry) gin.HandlerFunc {
	requests := metricx.NewCounterVec(metricx.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests processed, by method, route and status code.",
	}, []string{"method", "route", "status"})

	duration := metricx.NewHistogramVec(metricx.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency in seconds, by method, route and status code.",
		Buckets: metricx.DefBuckets,
	}, []string{"method", "route", "status"})

	metricx.MustRegister(registry, requests, duration)

	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		// Use the matched route template (e.g. "/health") rather than the raw
		// URL to keep label cardinality bounded; unmatched paths collapse to one.
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		if route == "/metrics" {
			return
		}

		status := strconv.Itoa(c.Writer.Status())
		elapsed := time.Since(start).Seconds()
		requests.WithLabelValues(c.Request.Method, route, status).Inc()
		duration.WithLabelValues(c.Request.Method, route, status).Observe(elapsed)
	}
}

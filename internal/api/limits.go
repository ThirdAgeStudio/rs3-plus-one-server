package api

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ThirdAgeStudio/rs3-plus-one-server/internal/limit"
)

const (
	pollBurst  = 60
	pollEvery  = 500 * time.Millisecond
	writeBurst = 20
	writeEvery = 3 * time.Second
)

const (
	trafficWindow = 10 * time.Second
	busyPolls     = 1_000
	maxInterval   = 15
)

type limits struct {
	perAddress bool
	polls      *limit.Limiter
	writes     *limit.Limiter
	traffic
}

func newLimits(perAddress bool) *limits {
	return &limits{perAddress: perAddress, polls: limit.New(pollBurst, pollEvery, time.Now),
		writes: limit.New(writeBurst, writeEvery, time.Now), traffic: traffic{now: time.Now}}
}

func (server *handler) limited(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if DevBuild || !server.limits.perAddress {
			next.ServeHTTP(writer, request)
			return
		}
		limiter := server.limits.writes
		if request.URL.Path == "/poll" {
			limiter = server.limits.polls
		}
		if wait := limiter.Take(address(request)); wait > 0 {
			server.busy(writer, wait)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (server *handler) busy(writer http.ResponseWriter, wait time.Duration) {
	seconds := max(1, int(math.Ceil(wait.Seconds())))
	writer.Header().Set("Retry-After", strconv.Itoa(seconds))
	server.write(writer, http.StatusTooManyRequests, errorAnswer{Error: "The server is busy, try again in a few seconds", RetryAfter: seconds})
}

func address(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return request.RemoteAddr
	}
	return strings.TrimSpace(host)
}

type traffic struct {
	now func() time.Time

	mu      sync.Mutex
	started time.Time
	polls   int
	last    int
}

func (counter *traffic) poll() int {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	now := counter.now()
	if elapsed := now.Sub(counter.started); elapsed >= trafficWindow {
		counter.last = counter.polls
		if elapsed >= 2*trafficWindow {
			counter.last = 0
		}
		counter.started, counter.polls = now, 0
	}
	counter.polls++
	return counter.last
}

func activeIntervalFor(polls int) int {
	if polls <= busyPolls {
		return activeInterval
	}
	return min(maxInterval, activeInterval*polls/busyPolls)
}

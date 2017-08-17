package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

var dnsServer = ""
var dnsPort = flag.Int("port", 53, "port to connect to the DNS server")
var hostName = flag.String("host", "wikipedia.org", "host name to ask DNS server to resolve")
var recordType = flag.String("rdatatype", "A", "DNS record type of the query")
var count = flag.Int("c", 10, "number of times to query")
var interval = flag.Duration("W", time.Second*1, "wait time between pings")
var timeout = flag.Duration("t", time.Second*2, "amount of time to wait for a response")

// atomic -- 0 if running, non-zero if exiting
var stopping int32

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr,
			`Usage:
  %s [options] [server]

Measure response time to the given DNS server by asking it to resolve a specified host. 

OPTIONS
  -h                    show this help
  -port <int>           port to connect to the DNS server (default 53)
  -host	<string>        host name to ask DNS server to resolve (default "wikipedia.org")
  -rdatatype <string>   DNS record type of the query (default "A")
  -c <int>              number of times to query (default 10)
  -W <duration>         wait time between pings (default 1s)
  -t <duration>         amount of time to wait for a server response (default 2s)
`, os.Args[0])

	}
	flag.Parse()

	if len(flag.Args()) != 1 {
		flag.Usage()
		os.Exit(2)
	}

	dnsServer = flag.Args()[0]

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		for range signalChan {
			if isStopping() {
				// second ctrl-C means immediate exit
				os.Exit(0)
			}
			atomic.StoreInt32(&stopping, 1)
			cancel()
		}
	}()

	// make sure our DNS server is a legit IP
	if ip := net.ParseIP(dnsServer); ip == nil {
		// not an IP, so resolve it as a DNS name
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, dnsServer)
		if err != nil || len(ips) == 0 {
			fmt.Fprintf(os.Stderr, "Error: cannot resolve dns server hostname: %v\n", dnsServer)
			os.Exit(1)
		}

		dnsServer = ips[0].IP.String()
	}

	// make sure our type is valid
	if _, ok := dns.StringToType[*recordType]; !ok {
		fmt.Fprintf(os.Stderr, "Error: invalid DNS record type %v", *recordType)
		os.Exit(1)
	}

	fmt.Printf("PING DNS: %s:%d, hostname: %s, rdatatype: %s\n", dnsServer, *dnsPort, *hostName, *recordType)

	var responseTimes []time.Duration
	var requests int

	resolver := &dns.Client{
		Timeout: *timeout,
	}

	m := new(dns.Msg).SetQuestion(dns.Fqdn(*hostName), dns.StringToType[*recordType])

	for i := 0; i < *count; i++ {
		if isStopping() {
			break
		}

		requests++
		resp, dur, err := resolver.ExchangeContext(ctx, m, fmt.Sprintf("%v:%v", dnsServer, *dnsPort))
		//fmt.Printf("Response: %#v", resp)
		if err != nil {
			if e, ok := err.(*net.OpError); ok {
				if e.Timeout() {
					fmt.Printf("Request timeout for seq %v\n", i)
					continue
				}
			}
			// all other errors are considered fatal for now
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		invalid := ""
		if len(resp.Answer) == 0 {
			// no error, but no answer means invalid hostname response
			// inform the user -- could impact response time of server
			invalid = " (invalid hostname)"
		}

		responseTimes = append(responseTimes, dur)
		fmt.Printf("%d bytes from %s: seq=%-3d time=%0.3f ms%v\n", resp.Len(), dnsServer, i, inMilli(dur), invalid)
		// sleep as needed
		if sleepTime := *interval - dur; sleepTime > 0 {
			time.Sleep(sleepTime)
		}
	}

	lostPercent := 0.0
	if requests > 0 {
		lostPercent = float64(100*(requests-len(responseTimes))) / float64(requests)
	}

	fmt.Printf("\n--- %s dnsping statistics ---\n", dnsServer)
	fmt.Printf("%d requests transmitted, %d responses received, %.1f%% lost\n", requests, len(responseTimes), lostPercent)
	fmt.Printf("round-trip min/avg/max/stddev = %.3f/%.3f/%.3f/%.3f ms\n", min(responseTimes), avg(responseTimes), max(responseTimes), stddev(responseTimes))
}

func min(times []time.Duration) float64 {
	if len(times) == 0 {
		return 0
	}
	low := times[0]
	for _, t := range times {
		if t < low {
			low = t
		}
	}

	return inMilli(low)
}

func max(times []time.Duration) float64 {
	if len(times) == 0 {
		return 0
	}
	max := times[0]
	for _, t := range times {
		if t > max {
			max = t
		}
	}

	return inMilli(max)
}

func avg(times []time.Duration) float64 {
	if len(times) == 0 {
		return 0
	}
	sum := 0.0
	for _, t := range times {
		sum += inMilli(t)
	}

	return sum / float64(len(times))
}

func stddev(times []time.Duration) float64 {
	if len(times) == 0 {
		return 0
	}

	avg := avg(times)
	sum := 0.0
	for _, t := range times {
		sum += math.Pow(inMilli(t)-avg, 2)
	}

	variance := sum / float64(len(times))

	return math.Sqrt(variance)
}

func inMilli(t time.Duration) float64 {
	return float64(t.Nanoseconds()) / 1000000.0
}

func isStopping() bool {
	return atomic.LoadInt32(&stopping) != 0
}

/*
 * Minio Cloud Storage, (C) 2016 Minio, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"time"
	"strings"

	"github.com/minio/dsync"
)

var nodes = []string{
	"10.x0.y0.z0:12345",
	"10.x1.y1.z1:12346",
	"10.x2.y2.z2:12347",
	"10.x3.y3.z3:12348",
	"10.x4.y4.z4:12349",
	"10.x5.y5.z5:12350",
	"10.x6.y6.z6:12351",
	"10.x7.y7.z7:12352"}

var (
	portFlag = flag.Int("p", 0, "Port for server to listen on")
	rpcPaths []string
)

func lockLoop(w *sync.WaitGroup, timeStart *time.Time, runs int, done *bool, nr int, ch chan<- float64) {
	defer w.Done()
	dm := dsync.NewDRWMutex(fmt.Sprintf("chaos-%d-%d", *portFlag, nr))

	delayMax := float64(0.0)
	timeLast := time.Now()
	var run int
	for run = 1; !*done && run <= runs; run++ {
		dm.Lock()

		if run == 1 { // re-initialize timing info to account for initial delay to start all nodes
			*timeStart = time.Now()
			timeLast = time.Now()
		}

		duration := time.Since(timeLast)
		if delayMax < duration.Seconds() || run%100 == 0 {
			if delayMax < duration.Seconds() {
				delayMax = duration.Seconds()
			}
			fmt.Print(".")
		}
		timeLast = time.Now()
		dm.Unlock()
	}

	ch <- delayMax
}

func startRPCServer(port int) {
	server := rpc.NewServer()
	server.RegisterName("Dsync", &lockServer{
		mutex:   sync.Mutex{},
		lockMap: make(map[string]int64),
	})
	// For some reason the registration paths need to be different (even for different server objs)
	server.HandleHTTP(rpcPaths[port-12345], fmt.Sprintf("%s-debug", rpcPaths[port-12345]))
	l, e := net.Listen("tcp", ":"+strconv.Itoa(port))
	if e != nil {
		log.Fatal("listen error:", e)
	}
	go http.Serve(l, nil)
}

func main() {

	rand.Seed(time.Now().UTC().UnixNano())

	flag.Parse()

	if *portFlag == 0 {
		log.Fatalf("No port number specified")
	}

	rpcPaths = make([]string, 0, len(nodes)) // list of rpc paths where lock server is serving.
	for i := range nodes {
		rpcPaths = append(rpcPaths, dsync.RpcPath+"-"+strconv.Itoa(i))
	}

	// Initialize net/rpc clients for dsync.
	var clnts []dsync.RPC
	for i := 0; i < len(nodes); i++ {
		clnts = append(clnts, newClient(nodes[i], rpcPaths[i]))
	}

	if err := dsync.SetNodesWithClients(clnts, getSelfNode(clnts, *portFlag)); err != nil {
		log.Fatalf("set nodes failed with %v", err)
	}

	// Start server
	startRPCServer(*portFlag)

	timeStart := time.Now()

	done := false

	// Catch Ctrl-C and abort gracefully with release of locks
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for sig := range c {
			fmt.Println("Ctrl-C intercepted", sig)
			done = true
		}
	}()

	runs := 40000
	parallel := 5
	wait := sync.WaitGroup{}
	wait.Add(parallel)

	// Create channel to get back max delay
	ch := make(chan float64, parallel)

	fmt.Println("Test starting...")

	for i := 0; i < parallel; i++ {
		go lockLoop(&wait, &timeStart, runs, &done, i, ch)
	}
	totalRuns := runs * parallel

	wait.Wait()
	close(ch)

	delayMax := float64(0.0)
	for c := range ch {
		if delayMax < c {
			delayMax = c
		}
	}

	fmt.Println("")
	fmt.Printf("        Locks/sec: %7.0f\n", 1.0/(time.Since(timeStart).Seconds()/float64(totalRuns)))
	fmt.Printf("         Msgs/sec: %7.0f\n", float64(len(nodes))*2.0*1.0/(time.Since(timeStart).Seconds()/float64(totalRuns)))
	fmt.Printf(" Worst case delay: %5.3f s\n", delayMax)

	if !done {
		// Let release messages get out
		fmt.Println("Waiting for test to close...")
		time.Sleep(10000 * time.Millisecond)
	}
}

func getSelfNode(rpcClnts []dsync.RPC, port int) int {

	index := -1
	for i, c := range rpcClnts {
		p, _ := strconv.Atoi(strings.Split(c.Node(), ":")[1])
		if port == p {
			if index == -1 {
				index = i
			} else {
				panic("More than one port found")
			}
		}
	}
	return index
}

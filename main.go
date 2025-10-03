package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/reuseport"
)

var words = []string{
	"The", "quick", "brown", "fox", "jumps", "over", "the", "lazy", "dog",
	"Hello", "world", "this", "is", "simulated", "streaming", "response",
	"from", "vLLM", "emulator", "generating", "tokens", "sequentially",
	"Lorem", "ipsum", "dolor", "sit", "amet", "consectetur", "adipiscing",
	"elit", "sed", "do", "eiusmod", "tempor", "incididunt", "ut", "labore",
}

var quiet bool
var myhostname string

func main() {
	var err error

	flag.BoolVar(&quiet, "quiet", true, "quiet")
	addr := flag.String("addr", "0.0.0.0:8080", "server listen address")
	flag.Parse()

	if myhostname, err = os.Hostname(); err != nil {
		log.Fatalf("error getting hostname: %v", err)
	}

	// Create a new listener on the given address using port reuse
	ln, err := reuseport.Listen("tcp4", *addr)
	if err != nil {
		log.Fatalf("error creating listener: %v", err)
	}
	defer ln.Close()

	// Create a new fasthttp server
	server := &fasthttp.Server{
		TCPKeepalive: true,
		LogAllErrors: true,
		ReadTimeout:  90 * time.Second,
		WriteTimeout: 120 * time.Second,
		Handler:      requestHandler,
	}

	// Start the server in a goroutine
	go func() {
		log.Printf("starting server on %s", *addr)
		if err := server.Serve(ln); err != nil {
			log.Fatalf("error starting server: %v", err)
		}
	}()

	// Wait for a signal to stop the server
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	// Stop the server
	if err := server.Shutdown(); err != nil {
		log.Fatalf("error stopping server: %v", err)
	}
}

func requestHandler(ctx *fasthttp.RequestCtx) {
	if !quiet {
		log.Printf("received request from %s: %s %s", ctx.RemoteAddr(), ctx.Method(), ctx.Path())
	}

	// Wait 100ms before processing response to emulate queuing/batching
	time.Sleep(100 * time.Millisecond)

	// Set headers for Server-Sent Events (SSE) - the standard vLLM/OpenAI streaming format
	ctx.SetContentType("text/event-stream")
	ctx.Response.Header.Set("Cache-Control", "no-cache")
	ctx.Response.Header.Set("Connection", "keep-alive")
	ctx.Response.Header.Set("X-Accel-Buffering", "no")
	ctx.Response.Header.Set("My-Hostname", myhostname)
	ctx.SetStatusCode(fasthttp.StatusOK)

	// Use SetBodyStreamWriter for chunked transfer encoding
	// This automatically sets Transfer-Encoding: chunked
	ctx.SetBodyStreamWriter(func(w *bufio.Writer) {
		defer func() {
			// Send final [DONE] marker (standard for OpenAI/vLLM streaming)
			doneChunk := "data: [DONE]\n\n"
			if _, err := w.WriteString(doneChunk); err != nil {
				log.Printf("error writing done marker: %v", err)
			}

			if err := w.Flush(); err != nil {
				log.Printf("error flushing writer: %v", err)
			}
		}()

		const (
			numChunks = 200 // 200 chunks total
			chunkSize = 500 // 500 bytes per chunk
		)

		// Send 200 chunks sequentially in SSE format
		for i := range numChunks {
			if ctx.Err() != nil {
				log.Printf("context done, stopping")
				return
			}
			// Generate a vLLM-style streaming response chunk
			// Format: data: {JSON}\n\n (Server-Sent Events standard)
			jsonPayload := generateVLLMChunk(i, chunkSize)
			sseChunk := fmt.Sprintf("data: %s\n\n", jsonPayload)

			// Write the SSE chunk
			if _, err := w.WriteString(sseChunk); err != nil {
				log.Printf("error writing chunk %d: %v", i, err)
				return
			}

			// Flush after each chunk to ensure sequential delivery
			if err := w.Flush(); err != nil {
				log.Printf("error flushing chunk %d: %v", i, err)
				return
			}

			if !quiet && i%50 == 0 {
				log.Printf("sent chunk %d/%d", i+1, numChunks)
			}

			time.Sleep(40 * time.Millisecond) // to emulate the latency of the LLM or etc
			// total latency is supposed to be 40 * 200 = 8000ms
		}

		if !quiet {
			log.Printf("completed sending all %d chunks", numChunks)
		}
	})
}

// generateVLLMChunk creates a vLLM/OpenAI-style streaming response chunk
// Returns a JSON string that, when wrapped in SSE format (data: {json}\n\n),
// will be at least targetSize bytes
func generateVLLMChunk(chunkIndex int, targetSize int) string {
	// Standard vLLM/OpenAI streaming response structure:
	// {
	//   "id": "cmpl-xxx",
	//   "object": "text_completion",
	//   "created": 1234567890,
	//   "model": "model-name",
	//   "choices": [{
	//     "text": "generated text chunk",
	//     "index": 0,
	//     "logprobs": null,
	//     "finish_reason": null
	//   }]
	// }

	baseJSON := fmt.Sprintf(`{"id":"cmpl-vllm-%d","object":"text_completion","created":%d,"model":"vllm-emulator","choices":[{"text":"`,
		chunkIndex, time.Now().Unix())

	suffix := `","index":0,"logprobs":null,"finish_reason":null}]}`

	// Generate realistic text content
	// Simulate token-by-token generation like a real LLM

	var text strings.Builder
	text.Grow(targetSize) // Pre-allocate capacity
	wordIdx := chunkIndex % len(words)

	// Generate text content up to approximately targetSize
	for text.Len() < targetSize {
		word := words[wordIdx%len(words)]
		text.WriteString(word)
		text.WriteByte(' ')
		wordIdx++
	}

	return baseJSON + text.String() + suffix
}

// Command scenario-model is a deterministic Anthropic-compatible supervisor
// used by distributed long-task labs. It is not a production model endpoint.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
)

type request struct {
	Messages []struct {
		Content any `json:"content"`
	} `json:"messages"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:17810", "listen address")
	flag.Parse()
	h := http.NewServeMux()
	h.HandleFunc("/v1/messages", supervise)
	h.HandleFunc("/messages", supervise)
	log.Printf("scenario supervisor listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, h))
}

func supervise(w http.ResponseWriter, r *http.Request) {
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	raw, _ := json.Marshal(req.Messages)
	text := string(raw)
	verdict := `{"status":"done","reason":"all required steps are complete","followup":""}`
	if strings.Contains(strings.ToLower(text), "remains") {
		verdict = `{"status":"continue","reason":"implementation is still missing","followup":"implement and verify all remaining steps"}`
	}
	w.Header().Set("content-type", "application/json")
	_, _ = fmt.Fprintf(w, `{"content":[{"type":"text","text":%q}]}`, verdict)
}

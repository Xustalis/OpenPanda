// Command task-timeline prints a task and its event timeline from one or more
// node databases. It is read-only and intended for distributed lab reports.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

type nodeTimeline struct {
	DB     string       `json:"db"`
	Task   *core.Task   `json:"task,omitempty"`
	Events []core.Event `json:"events,omitempty"`
	Error  string       `json:"error,omitempty"`
}

func main() {
	dbs := flag.String("db", "", "comma-separated SQLite database paths")
	taskID := flag.String("task", "", "task id")
	jsonOut := flag.Bool("json", false, "emit JSON")
	flag.Parse()
	if strings.TrimSpace(*dbs) == "" || strings.TrimSpace(*taskID) == "" {
		fmt.Fprintln(os.Stderr, "usage: task-timeline --db path[,path...] --task ID [--json]")
		os.Exit(2)
	}
	var out []nodeTimeline
	ctx := context.Background()
	for _, path := range strings.Split(*dbs, ",") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		db, err := storage.Open(path)
		if err != nil {
			out = append(out, nodeTimeline{DB: path, Error: err.Error()})
			continue
		}
		store := core.NewTaskStore(db, nil)
		row := nodeTimeline{DB: path}
		if task, err := store.Get(ctx, *taskID); err == nil {
			row.Task = &task
			row.Events, err = store.Events(ctx, *taskID)
			if err != nil {
				row.Error = err.Error()
			}
		} else {
			row.Error = err.Error()
		}
		out = append(out, row)
		_ = db.Close()
	}
	if *jsonOut {
		if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
			log.Fatal(err)
		}
		return
	}
	for _, node := range out {
		fmt.Printf("db: %s\n", node.DB)
		if node.Error != "" {
			fmt.Printf("  error: %s\n", node.Error)
			continue
		}
		fmt.Printf("  state: %s owner: %s attempt: %s\n", node.Task.State, node.Task.OwnerNode, node.Task.AttemptID)
		for _, event := range node.Events {
			fmt.Printf("  %d %-14s %s\n", event.TS, event.Type, event.DataJSON)
		}
	}
}

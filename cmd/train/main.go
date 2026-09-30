// Command train is the offline training job (D-5). It fetches the occupancy
// sensor readings of one or more recorded runs from storage, builds the
// per-room time-of-day profile, and stores it as a model. The controller
// picks up the newest model on its own.
//
//	TRAIN_RUNS=train-a,train-b docker compose run --rm train
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/env"
	"github.com/MisterD0ctor/d7065e-project/internal/predict"
)

func main() {
	storage := strings.TrimRight(env.String("STORAGE_URL", "http://storage:8080"), "/")
	runs := strings.Split(env.Must("TRAIN_RUNS"), ",")
	name := env.String("MODEL_NAME", "profile-"+time.Now().UTC().Format("20060102T150405"))
	client := &http.Client{Timeout: 5 * time.Minute}

	var obs []predict.Observation
	for _, run := range runs {
		run = strings.TrimSpace(run)
		u := fmt.Sprintf("%s/history?run=%s&kind=occupancy", storage, url.QueryEscape(run))
		resp, err := client.Get(u)
		if err != nil {
			log.Fatalf("storage: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			log.Fatalf("storage: run %s: status %d", run, resp.StatusCode)
		}
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		n := 0
		for sc.Scan() {
			var o predict.Observation
			if json.Unmarshal(sc.Bytes(), &o) == nil {
				obs = append(obs, o)
				n++
			}
		}
		resp.Body.Close()
		if err := sc.Err(); err != nil {
			log.Fatalf("reading run %s: %v", run, err)
		}
		log.Printf("run %s: %d occupancy readings", run, n)
	}

	p, err := predict.Train(name, runs, obs)
	if err != nil {
		log.Fatal(err)
	}
	body, _ := json.Marshal(p)
	req, _ := http.NewRequest(http.MethodPut, storage+"/models/"+url.PathEscape(name), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("storing model: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		log.Fatalf("storing model: status %d", resp.StatusCode)
	}
	log.Printf("stored model %s: %d rooms, %d weekdays, %d readings (%s … %s)",
		name, len(p.Rooms), p.TrainedOn.Weekdays, p.TrainedOn.Readings, p.TrainedOn.From, p.TrainedOn.To)
}

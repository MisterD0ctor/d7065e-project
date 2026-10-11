package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/MisterD0ctor/d7065e-project/internal/msg"
)

// record builds the sensor value the physical model would have written: the
// given minutes after 10:00, two rooms each.
func record(minutes ...int) string {
	var rec msg.Record
	for _, m := range minutes {
		at := fmt.Sprintf("2026-09-21T10:%02d:00Z", m)
		rec.Minutes = append(rec.Minutes, msg.RecordMinute{ModelTime: at, Rooms: []msg.Truth{
			{Room: "level0/1570", ModelTime: at, CO2: 600 + float64(m), Occupancy: 12},
			{Room: "level0/A109", ModelTime: at, CO2: 420},
		}})
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

func TestRecorderCopiesEveryMinuteOnce(t *testing.T) {
	var value string
	var readErr error
	var sent []string
	r := &recorder{
		runID: "eval-1",
		read:  func(context.Context) (string, error) { return value, readErr },
		publish: func(topic string, v any) {
			tr := v.(msg.Truth)
			if tr.RunID != "eval-1" {
				t.Errorf("published without the run id: %+v", tr)
			}
			sent = append(sent, topic+" "+tr.ModelTime[11:16])
		},
	}
	ctx := context.Background()
	step := func(v string, want ...string) {
		t.Helper()
		value, sent = v, nil
		r.poll(ctx)
		if fmt.Sprint(sent) != fmt.Sprint(want) {
			t.Errorf("published %v, want %v", sent, want)
		}
	}

	step("")                                                                    // the physical model has written nothing yet
	step(`{"minutes":[]}`)                                                      // or has not reached a minute
	step(record(0, 1, 2), "truth/level0/1570 10:02", "truth/level0/A109 10:02") // a new recorder starts at the newest
	step(record(0, 1, 2))                                                       // nothing new
	step(record(1, 2, 3, 4),                                                    // two minutes since the last poll: both, in order
		"truth/level0/1570 10:03", "truth/level0/A109 10:03", "truth/level0/1570 10:04", "truth/level0/A109 10:04")

	readErr = errors.New("BuildSim is away")
	step(record(2, 3, 4, 5))
	readErr = nil
	step(record(2, 3, 4, 5), "truth/level0/1570 10:05", "truth/level0/A109 10:05")

	// A new experiment: the clock goes back, and the recorder follows.
	step(record(0), "truth/level0/1570 10:00", "truth/level0/A109 10:00")
	step(record(0, 1), "truth/level0/1570 10:01", "truth/level0/A109 10:01")
}

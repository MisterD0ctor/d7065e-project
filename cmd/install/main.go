// Command install is the installer's tool: it registers devices in the
// registry, one at a time or from an installation list.
//
//	go run ./cmd/install -by kasper -capacity 25 co2-0001 co2 level0/1570
//	go run ./cmd/install -by kasper -csv deploy/devices.csv -rooms deploy/rooms.csv
//
// The room list gives each room's design capacity (how many people it is
// built for), which sizes its ventilation. It is the installer's knowledge,
// from the drawings; a room without one is sized from its floor area.
//
// A device already registered is skipped, so the list can be rerun.
package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	registry := flag.String("registry", "http://127.0.0.1:8070", "registry URL")
	by := flag.String("by", "", "who installs the devices (required)")
	list := flag.String("csv", "", "installation list with columns id,kind,room")
	roomList := flag.String("rooms", "", "room list with columns room,capacity (with -csv)")
	capacity := flag.Int("capacity", 0, "design capacity of the room (for a single device)")
	flag.Parse()
	if *by == "" {
		log.Fatal("-by is required: the registry records who installed each device")
	}

	var rows [][3]string
	capacities := map[string]int{}
	switch {
	case *list != "":
		var err error
		if rows, err = readCSV(*list); err != nil {
			log.Fatal(err)
		}
		if *roomList != "" {
			if capacities, err = readRooms(*roomList); err != nil {
				log.Fatal(err)
			}
		}
	case flag.NArg() == 3:
		rows = [][3]string{{flag.Arg(0), flag.Arg(1), flag.Arg(2)}}
		capacities[flag.Arg(2)] = *capacity
	default:
		log.Fatal("usage: install -by NAME (-csv FILE [-rooms FILE] | [-capacity N] ID KIND LEVEL/ROOM)")
	}

	client := &http.Client{Timeout: 5 * time.Second}
	var installed, skipped, failed int
	for _, r := range rows {
		body, _ := json.Marshal(map[string]any{"id": r[0], "kind": r[1], "room": r[2], "installed_by": *by, "capacity": capacities[r[2]]})
		resp, err := client.Post(strings.TrimRight(*registry, "/")+"/devices", "application/json", bytes.NewReader(body))
		if err != nil {
			log.Fatalf("registry: %v", err)
		}
		var res struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusCreated:
			installed++
		case http.StatusConflict:
			skipped++
		default:
			failed++
			fmt.Printf("%s: %s\n", r[0], res.Error)
		}
	}
	fmt.Printf("installed %d, already registered %d, failed %d\n", installed, skipped, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

func readCSV(path string) ([][3]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, err
	}
	var rows [][3]string
	for i, rec := range records {
		if i == 0 && rec[0] == "id" {
			continue // header
		}
		if len(rec) != 3 {
			return nil, fmt.Errorf("%s line %d: want id,kind,room", path, i+1)
		}
		rows = append(rows, [3]string{rec[0], rec[1], rec[2]})
	}
	return rows, nil
}

// readRooms reads the room list: room,capacity.
func readRooms(path string) (map[string]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for i, rec := range records {
		if i == 0 && rec[0] == "room" {
			continue // header
		}
		if len(rec) != 2 {
			return nil, fmt.Errorf("%s line %d: want room,capacity", path, i+1)
		}
		n, err := strconv.Atoi(rec[1])
		if err != nil || n < 1 {
			return nil, fmt.Errorf("%s line %d: capacity %q", path, i+1, rec[1])
		}
		out[rec[0]] = n
	}
	return out, nil
}

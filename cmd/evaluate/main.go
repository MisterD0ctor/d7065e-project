// Command evaluate turns recorded runs into the numbers the report needs
// (report §11): air quality (NFR-1), comfort (NFR-2), the energy proxy
// (NFR-3) and data completeness per run and room role, and the predictor's
// accuracy against a naive "same as now" forecast (FR-4, T-06).
//
//	go run ./cmd/evaluate -runs eval-constant,eval-reactive,eval-predictive,eval-oracle \
//	    -accuracy-run eval-reactive -out results/
//
// It reads finished runs from storage, so the truth it uses is a recording.
package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/predict"
)

var client = &http.Client{Timeout: 10 * time.Minute}

func main() {
	storage := flag.String("storage", "http://127.0.0.1:8090", "storage URL")
	occsim := flag.String("occupancysim", "http://127.0.0.1:8081", "occupancysim URL, for room roles")
	runs := flag.String("runs", "", "comma-separated runs to compare")
	accRun := flag.String("accuracy-run", "", "run whose occupancy readings test the predictor (not a training run)")
	out := flag.String("out", "results", "directory for CSV output")
	flag.Parse()
	if *runs == "" {
		log.Fatal("-runs is required")
	}
	roles, err := roomRoles(*occsim)
	if err != nil {
		log.Printf("room roles: %v (reporting all rooms together)", err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}

	var results []runResult
	for _, run := range strings.Split(*runs, ",") {
		r, err := evaluateRun(*storage, strings.TrimSpace(run), roles)
		if err != nil {
			log.Fatalf("run %s: %v", run, err)
		}
		results = append(results, r)
		if err := r.writeHourly(filepath.Join(*out, r.run+"-hourly.csv")); err != nil {
			log.Fatal(err)
		}
	}
	printRuns(results)

	if *accRun != "" {
		if err := accuracy(*storage, *accRun, roles); err != nil {
			log.Fatalf("accuracy: %v", err)
		}
	}
}

// ---- per-run metrics ----

type truthRecord struct {
	Room      string  `json:"room"`
	ModelTime string  `json:"model_time"`
	CO2       float64 `json:"co2_ppm"`
	Temp      float64 `json:"temp_c"`
	Occupancy int     `json:"occupancy"`
	FanW      float64 `json:"fan_w"`
	AHUHeatW  float64 `json:"ahu_heat_w"`
	RadiatorW float64 `json:"radiator_w"`
}

type tally struct {
	minutes, occupied, co2OK, comfortOK int
	over1000                            int // occupied minutes above 1000 ppm
	maxCO2                              float64
	fanKWh, ahuKWh, radKWh              float64
}

func (t *tally) add(r truthRecord, dt time.Duration) {
	t.minutes++
	if r.Occupancy > 0 {
		t.occupied++
		if r.CO2 <= 1000 {
			t.co2OK++
		} else {
			t.over1000++
		}
		if r.Temp >= 20 && r.Temp <= 24 {
			t.comfortOK++
		}
	}
	t.maxCO2 = max(t.maxCO2, r.CO2)
	h := dt.Hours()
	t.fanKWh += r.FanW * h / 1000
	t.ahuKWh += r.AHUHeatW * h / 1000
	t.radKWh += r.RadiatorW * h / 1000
}

type runResult struct {
	run      string
	from, to time.Time
	rooms    int
	all      tally
	byRole   map[string]*tally
	hourly   map[string]map[int][2]float64 // role → hour of week → (sum CO₂, n)
	expected int                           // room-minutes in the span
	step     time.Duration                 // mean model time between records
}

func evaluateRun(storage, run string, roles map[string]string) (runResult, error) {
	res := runResult{run: run, byRole: map[string]*tally{}, hourly: map[string]map[int][2]float64{}}
	last := map[string]time.Time{}
	err := stream(storage, run, "truth", func(b []byte) {
		var r truthRecord
		if json.Unmarshal(b, &r) != nil {
			return
		}
		t, err := time.Parse(time.RFC3339, r.ModelTime)
		if err != nil {
			return
		}
		// A record earlier than the room's previous one is the stray written
		// when the next run reset the clock before this run's physics was
		// replaced: it doesn't belong to this run's span.
		p, seen := last[r.Room]
		if seen && !t.After(p) {
			return
		}
		// Energy over the gap since this room's previous record, capped so a
		// pause in the run doesn't count as hours of power.
		dt := time.Minute
		if seen {
			dt = min(t.Sub(p), 5*time.Minute)
		}
		last[r.Room] = t
		if res.from.IsZero() || t.Before(res.from) {
			res.from = t
		}
		if t.After(res.to) {
			res.to = t
		}
		role := roles[r.Room]
		if role == "" {
			role = "room"
		}
		if res.byRole[role] == nil {
			res.byRole[role] = &tally{}
			res.hourly[role] = map[int][2]float64{}
		}
		res.all.add(r, dt)
		res.byRole[role].add(r, dt)
		how := int(t.Weekday())*24 + t.Hour()
		h := res.hourly[role][how]
		res.hourly[role][how] = [2]float64{h[0] + r.CO2, h[1] + 1}
	})
	res.rooms = len(last)
	res.expected = res.rooms * int(res.to.Sub(res.from).Minutes()+1)
	res.step = res.to.Sub(res.from) / time.Duration(max(res.all.minutes/max(res.rooms, 1)-1, 1))
	return res, err
}

func pct(a, b int) string {
	if b == 0 {
		return "–"
	}
	return fmt.Sprintf("%.1f %%", 100*float64(a)/float64(b))
}

func printRuns(rs []runResult) {
	fmt.Println("## Runs")
	fmt.Println()
	fmt.Println("Shares count truth records; at factor 120 there is one per room every ~2 model-minutes, so \"records\" below are ~2-minute samples.")
	fmt.Println()
	fmt.Println("| Run | Span (model time) | Rooms | Record step | CO₂ ≤ 1000 (NFR-1) | 20–24 °C (NFR-2) | Occupied records > 1000 ppm | Max CO₂ | Energy kWh (fan + AHU + radiator) |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|")
	for _, r := range rs {
		a := r.all
		fmt.Printf("| %s | %s … %s | %d | %.1f min | %s | %s | %d | %.0f ppm | %.0f (%.0f + %.0f + %.0f) |\n",
			r.run, r.from.Format("Mon 02 15:04"), r.to.Format("Mon 02 15:04"), r.rooms, r.step.Minutes(),
			pct(a.co2OK, a.occupied), pct(a.comfortOK, a.occupied),
			a.over1000, a.maxCO2, a.fanKWh+a.ahuKWh+a.radKWh, a.fanKWh, a.ahuKWh, a.radKWh)
	}
	fmt.Println()
	fmt.Println("## By room role")
	fmt.Println()
	fmt.Println("| Run | Role | Occupied records | CO₂ ≤ 1000 | 20–24 °C | Records > 1000 ppm | Energy kWh |")
	fmt.Println("|---|---|---|---|---|---|---|")
	for _, r := range rs {
		var roles []string
		for role := range r.byRole {
			roles = append(roles, role)
		}
		sort.Strings(roles)
		for _, role := range roles {
			t := r.byRole[role]
			fmt.Printf("| %s | %s | %d | %s | %s | %d | %.0f |\n", r.run, role, t.occupied,
				pct(t.co2OK, t.occupied), pct(t.comfortOK, t.occupied), t.over1000, t.fanKWh+t.ahuKWh+t.radKWh)
		}
	}
	fmt.Println()
}

// writeHourly writes the mean CO₂ per role and hour of the week, for plots.
func (r runResult) writeHourly(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	w.Write([]string{"run", "role", "weekday", "hour", "mean_co2_ppm"})
	for role, hours := range r.hourly {
		for how, h := range hours {
			w.Write([]string{r.run, role, time.Weekday(how / 24).String(), fmt.Sprint(how % 24), fmt.Sprintf("%.1f", h[0]/h[1])})
		}
	}
	w.Flush()
	return w.Error()
}

// ---- predictor accuracy (T-06) ----

// accuracy compares, at every occupancy reading on weekdays 07–18, the peak
// count within the lead time with (a) the controller's forecast, the most of
// the count now and the model's expectation, and (b) the naive "same as now".
func accuracy(storage, run string, roles map[string]string) error {
	model, err := latestModel(storage)
	if err != nil {
		return err
	}
	for _, id := range model.TrainedOn.RunIDs {
		if id == run {
			return fmt.Errorf("run %s was used to train %s: pick a held-out run", run, model.Name)
		}
	}
	type reading struct {
		t time.Time
		n float64
	}
	series := map[string][]reading{}
	err = stream(storage, run, "occupancy", func(b []byte) {
		var o predict.Observation
		if json.Unmarshal(b, &o) != nil {
			return
		}
		t, err := time.Parse(time.RFC3339, o.ModelTime)
		if err == nil {
			series[o.Room] = append(series[o.Room], reading{t, o.Value})
		}
	})
	if err != nil {
		return err
	}
	// An arrival is the count rising by at least arrivalRise within the lead
	// time. Forecasting is for warning ahead of arrivals, which "same as now"
	// by definition never does; mean error alone hides that.
	const arrivalRise = 5
	type errs struct {
		model, naive               float64
		n                          int
		arrivals, foreseen, alarms int // alarms: forecast a rise that didn't come
		quiet                      int // readings with no arrival ahead
	}
	byRole := map[string]*errs{}
	for room, s := range series {
		sort.Slice(s, func(i, j int) bool { return s[i].t.Before(s[j].t) })
		lead := 20 * time.Minute // the median lead of the room set
		for i, r := range s {
			if wd := r.t.Weekday(); wd == time.Saturday || wd == time.Sunday || r.t.Hour() < 7 || r.t.Hour() >= 18 {
				continue
			}
			var actual float64
			for j := i + 1; j < len(s) && !s[j].t.After(r.t.Add(lead)); j++ {
				actual = max(actual, s[j].n)
			}
			forecast := max(r.n, model.Expected(room, r.t, lead))
			role := roles[room]
			if role == "" {
				role = "room"
			}
			if byRole[role] == nil {
				byRole[role] = &errs{}
			}
			e := byRole[role]
			e.model += abs(forecast - actual)
			e.naive += abs(r.n - actual)
			e.n++
			warned := forecast-r.n >= arrivalRise
			if actual-r.n >= arrivalRise {
				e.arrivals++
				if warned {
					e.foreseen++
				}
			} else {
				e.quiet++
				if warned {
					e.alarms++
				}
			}
		}
	}
	fmt.Printf("## Predictor accuracy (T-06): model %s on held-out run %s\n\n", model.Name, run)
	fmt.Println("At each occupancy reading on weekdays 07–18: the peak count within the next 20 min, against the forecast (count now, or the profile's 80 % quantile if higher) and the naive \"same as now\". An arrival is a rise of ≥ 5 people within the 20 min; \"same as now\" foresees none by definition.")
	fmt.Println()
	fmt.Println("| Role | Readings | Forecast MAE | Naive MAE | Arrivals | Foreseen | False alarms |")
	fmt.Println("|---|---|---|---|---|---|---|")
	var names []string
	for r := range byRole {
		names = append(names, r)
	}
	sort.Strings(names)
	for _, role := range names {
		e := byRole[role]
		fmt.Printf("| %s | %d | %.2f | %.2f | %d | %s | %s |\n", role, e.n, e.model/float64(e.n), e.naive/float64(e.n),
			e.arrivals, pct(e.foreseen, e.arrivals), pct(e.alarms, e.quiet))
	}
	fmt.Println()
	return nil
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// ---- storage and occupancysim ----

func stream(storage, run, kind string, each func([]byte)) error {
	u := fmt.Sprintf("%s/history?run=%s&kind=%s", storage, url.QueryEscape(run), kind)
	resp, err := client.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: status %d (is it the live run? storage won't serve its truth)", u, resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		each(sc.Bytes())
	}
	return sc.Err()
}

func latestModel(storage string) (predict.Profile, error) {
	var p predict.Profile
	resp, err := client.Get(storage + "/models/latest")
	if err != nil {
		return p, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return p, fmt.Errorf("no model in storage (status %d)", resp.StatusCode)
	}
	return p, json.NewDecoder(resp.Body).Decode(&p)
}

// roomRoles asks occupancysim which rooms are offices, lecture rooms and
// fika rooms, keyed "<level>/<room>".
func roomRoles(occsim string) (map[string]string, error) {
	resp, err := client.Get(occsim + "/api/state")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var st struct {
		Sim struct {
			Rooms []struct {
				Name, Level, Role string
			} `json:"rooms"`
		} `json:"sim"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, err
	}
	roles := map[string]string{}
	for _, r := range st.Sim.Rooms {
		roles[r.Level+"/"+r.Name] = r.Role
	}
	return roles, nil
}

// Package site loads the configured rooms' sizes from BuildSim's floor plans.
package site

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/clock"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

// Sizes returns each room's sizing, retrying until BuildSim answers. Floor
// areas come from BuildSim; capacities from occupancysim, falling back to an
// estimate from the area if it can't be reached. A room that BuildSim doesn't
// know is a configuration error.
func Sizes(ctx context.Context, bs *buildsim.Client, clk *clock.Client, keys []rooms.Key) (map[rooms.Key]sizing.Room, error) {
	caps, err := clk.Capacities(ctx)
	if err != nil {
		log.Printf("capacities from occupancysim: %v (estimating from area)", err)
	}
	for {
		sizes, err := load(ctx, bs, caps, keys)
		if err == nil {
			return sizes, nil
		}
		if _, missing := err.(missingRoom); missing {
			return nil, err
		}
		log.Printf("waiting for BuildSim: %v", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

type missingRoom struct{ key rooms.Key }

func (m missingRoom) Error() string { return fmt.Sprintf("room %s not found in BuildSim", m.key) }

func load(ctx context.Context, bs *buildsim.Client, caps map[string]int, keys []rooms.Key) (map[rooms.Key]sizing.Room, error) {
	floors := map[string]buildsim.Floor{}
	sizes := map[rooms.Key]sizing.Room{}
	for _, k := range keys {
		floor, ok := floors[k.Level]
		if !ok {
			var err error
			if floor, err = bs.Floor(ctx, k.Level); err != nil {
				return nil, err
			}
			floors[k.Level] = floor
		}
		for _, r := range floor.Rooms {
			if r.Name == k.Name {
				area := sizing.AreaM2(r.Area)
				if c, ok := caps[k.String()]; ok && c > 0 {
					sizes[k] = sizing.ForCapacity(area, c)
				} else {
					sizes[k] = sizing.For(area)
				}
			}
		}
		if _, ok := sizes[k]; !ok {
			return nil, missingRoom{k}
		}
	}
	return sizes, nil
}

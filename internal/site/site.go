// Package site loads the configured rooms' sizes from BuildSim's floor plans.
package site

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/buildsim"
	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

// Sizes returns each room's size, retrying until BuildSim answers. The floor
// area, and with it the volume, comes from BuildSim's floor plan, which is
// all the physical model needs. The capacity is estimated from the area: the
// one the ventilation is sized by is commissioning data and lives in the
// registry (IF-12). A room that BuildSim doesn't know is a configuration
// error.
func Sizes(ctx context.Context, bs *buildsim.Client, keys []rooms.Key) (map[rooms.Key]sizing.Room, error) {
	for {
		sizes, err := load(ctx, bs, keys)
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

func load(ctx context.Context, bs *buildsim.Client, keys []rooms.Key) (map[rooms.Key]sizing.Room, error) {
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
				sizes[k] = sizing.For(sizing.AreaM2(r.Area))
			}
		}
		if _, ok := sizes[k]; !ok {
			return nil, missingRoom{k}
		}
	}
	return sizes, nil
}

package registry

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
)

var (
	fika = rooms.Key{Level: "level0", Name: "1570"}
	now  = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	info = RoomInfo{AreaM2: 101.2, Capacity: 25}
)

func open(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestInstallAndLookUp(t *testing.T) {
	db := open(t)
	dev, err := db.Install("co2-0001", rooms.CO2, fika, "kasper", info, now)
	if err != nil {
		t.Fatal(err)
	}
	if dev.Room != "level0/1570" || dev.Status != "active" || dev.Size.Capacity != 25 {
		t.Fatalf("installed %+v", dev)
	}
	got, err := db.Get("co2-0001")
	if err != nil || got.Kind != rooms.CO2 {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := db.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}

func TestInstallRejects(t *testing.T) {
	db := open(t)
	db.Install("co2-0001", rooms.CO2, fika, "kasper", info, now)
	for name, tc := range map[string]struct {
		id, kind, by string
		want         error
	}{
		"duplicate id":   {"co2-0001", rooms.CO2, "kasper", ErrExists},
		"unknown kind":   {"x-1", "smoke", "kasper", ErrInvalid},
		"bad id":         {"co2 0002", rooms.CO2, "kasper", ErrInvalid},
		"no installer":   {"co2-0002", rooms.CO2, "", ErrInvalid},
		"path in the id": {"../etc", rooms.CO2, "kasper", ErrInvalid},
	} {
		if _, err := db.Install(tc.id, tc.kind, fika, tc.by, info, now); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
}

func TestListRetireCheckIn(t *testing.T) {
	db := open(t)
	db.Install("co2-0001", rooms.CO2, fika, "kasper", info, now)
	db.Install("damper-0001", rooms.Damper, fika, "kasper", info, now)
	db.Install("co2-0002", rooms.CO2, rooms.Key{Level: "level0", Name: "A117"}, "kasper", RoomInfo{213.9, 53}, now)

	list, _ := db.List(Filter{Kind: rooms.CO2})
	if len(list) != 2 {
		t.Fatalf("co2 devices: %d, want 2", len(list))
	}
	list, _ = db.List(Filter{Room: "level0/1570"})
	if len(list) != 2 {
		t.Fatalf("devices in 1570: %d, want 2", len(list))
	}

	dev, err := db.CheckIn("damper-0001", "http://damper-0001:8080", now)
	if err != nil || dev.Endpoint != "http://damper-0001:8080" || dev.LastSeen == "" {
		t.Fatalf("check-in: %+v %v", dev, err)
	}

	if err := db.Retire("co2-0001"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CheckIn("co2-0001", "", now); !errors.Is(err, ErrRetired) {
		t.Errorf("retired device checked in: %v", err)
	}
	active, _ := db.List(Filter{Kind: rooms.CO2, Status: "active"})
	if len(active) != 1 {
		t.Errorf("active co2 devices: %d, want 1", len(active))
	}
}

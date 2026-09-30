// Package registry is the device database behind the registry service
// (D-10). An installer records each device's id, kind and room; a device that
// boots asks for its own record and learns where it is. The database is the
// source of truth for what is installed: BuildSim's equipment list is
// recreated from it, because BuildSim starts blank after a restart.
package registry

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	_ "modernc.org/sqlite"

	"github.com/MisterD0ctor/d7065e-project/internal/rooms"
	"github.com/MisterD0ctor/d7065e-project/internal/sizing"
)

var (
	ErrNotFound = errors.New("no such device")
	ErrExists   = errors.New("device id already installed")
	ErrRetired  = errors.New("device is retired")
	ErrInvalid  = errors.New("invalid device")
)

// idPattern: what an installer may type as a device id (a serial number).
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// Kinds a device can be.
var Kinds = map[string]bool{
	rooms.CO2: true, rooms.Temp: true, rooms.Occupancy: true,
	rooms.Damper: true, rooms.Heating: true,
}

func IsSensor(kind string) bool   { _, ok := rooms.Sensors[kind]; return ok }
func IsActuator(kind string) bool { _, ok := rooms.Actuators[kind]; return ok }

// Device is one installed device, as the registry API returns it.
type Device struct {
	ID          string      `json:"id"`
	Kind        string      `json:"kind"`
	Room        string      `json:"room"` // <level>/<room>
	Status      string      `json:"status"`
	InstalledAt string      `json:"installed_at"`
	InstalledBy string      `json:"installed_by"`
	Endpoint    string      `json:"endpoint,omitempty"`  // actuators: where to send commands
	LastSeen    string      `json:"last_seen,omitempty"` // real time of the last check-in
	Size        sizing.Room `json:"size"`                // the room's ventilation sizing
}

// BuildSim ids for a device: the equipment is the device itself; the sensor
// or actuator inside it gets a suffix so the ids stay unique building-wide.
func SensorID(deviceID string) string   { return deviceID + "-reading" }
func ActuatorID(deviceID string) string { return deviceID + "-state" }

type DB struct{ db *sql.DB }

func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; SQLite serialises anyway
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS rooms (
	level     TEXT NOT NULL,
	room      TEXT NOT NULL,
	area_m2   REAL NOT NULL,
	capacity  INTEGER NOT NULL,
	PRIMARY KEY (level, room)
);
CREATE TABLE IF NOT EXISTS devices (
	id           TEXT PRIMARY KEY,
	kind         TEXT NOT NULL,
	level        TEXT NOT NULL,
	room         TEXT NOT NULL,
	status       TEXT NOT NULL DEFAULT 'active',
	installed_at TEXT NOT NULL,
	installed_by TEXT NOT NULL,
	endpoint     TEXT NOT NULL DEFAULT '',
	last_seen    TEXT NOT NULL DEFAULT '',
	FOREIGN KEY (level, room) REFERENCES rooms (level, room)
);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db: db}, nil
}

func (d *DB) Close() error { return d.db.Close() }

// RoomInfo is what the installer's room must have: it exists in the floor
// plan, so it has an area, and a design capacity.
type RoomInfo struct {
	AreaM2   float64
	Capacity int
}

// Install records a new device. roomInfo is looked up by the caller (from
// BuildSim and occupancysim) and stored the first time a room is used.
func (d *DB) Install(id, kind string, k rooms.Key, by string, info RoomInfo, now time.Time) (Device, error) {
	if !idPattern.MatchString(id) {
		return Device{}, fmt.Errorf("%w: id %q (letters, digits, '-' and '_')", ErrInvalid, id)
	}
	if !Kinds[kind] {
		return Device{}, fmt.Errorf("%w: kind %q", ErrInvalid, kind)
	}
	if by == "" {
		return Device{}, fmt.Errorf("%w: installed_by is required", ErrInvalid)
	}
	tx, err := d.db.Begin()
	if err != nil {
		return Device{}, err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM devices WHERE id = ?`, id).Scan(&exists); err != nil {
		return Device{}, err
	}
	if exists > 0 {
		return Device{}, ErrExists
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO rooms (level, room, area_m2, capacity) VALUES (?, ?, ?, ?)`,
		k.Level, k.Name, info.AreaM2, info.Capacity); err != nil {
		return Device{}, err
	}
	if _, err := tx.Exec(`INSERT INTO devices (id, kind, level, room, installed_at, installed_by) VALUES (?, ?, ?, ?, ?, ?)`,
		id, kind, k.Level, k.Name, now.UTC().Format(time.RFC3339), by); err != nil {
		return Device{}, err
	}
	if err := tx.Commit(); err != nil {
		return Device{}, err
	}
	return d.Get(id)
}

const selectDevice = `
SELECT d.id, d.kind, d.level, d.room, d.status, d.installed_at, d.installed_by,
       d.endpoint, d.last_seen, r.area_m2, r.capacity
FROM devices d JOIN rooms r ON r.level = d.level AND r.room = d.room`

func scan(row interface{ Scan(...any) error }) (Device, error) {
	var dev Device
	var level, room string
	var area float64
	var capacity int
	err := row.Scan(&dev.ID, &dev.Kind, &level, &room, &dev.Status, &dev.InstalledAt,
		&dev.InstalledBy, &dev.Endpoint, &dev.LastSeen, &area, &capacity)
	if err != nil {
		return Device{}, err
	}
	dev.Room = rooms.Key{Level: level, Name: room}.String()
	dev.Size = sizing.ForCapacity(area, capacity)
	return dev, nil
}

func (d *DB) Get(id string) (Device, error) {
	dev, err := scan(d.db.QueryRow(selectDevice+` WHERE d.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	return dev, err
}

// Filter selects devices for List; empty fields match everything.
type Filter struct {
	Kind   string
	Room   string // <level>/<room>
	Status string
}

func (d *DB) List(f Filter) ([]Device, error) {
	q := selectDevice + ` WHERE 1=1`
	var args []any
	if f.Kind != "" {
		q += ` AND d.kind = ?`
		args = append(args, f.Kind)
	}
	if f.Room != "" {
		k, err := rooms.Parse(f.Room)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		q += ` AND d.level = ? AND d.room = ?`
		args = append(args, k.Level, k.Name)
	}
	if f.Status != "" {
		q += ` AND d.status = ?`
		args = append(args, f.Status)
	}
	rows, err := d.db.Query(q+` ORDER BY d.level, d.room, d.kind, d.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		dev, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dev)
	}
	return out, rows.Err()
}

// Retire marks a device as removed. It stays in the database for the record.
func (d *DB) Retire(id string) error {
	res, err := d.db.Exec(`UPDATE devices SET status = 'retired' WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CheckIn records that a device is up, and where to reach it.
func (d *DB) CheckIn(id, endpoint string, now time.Time) (Device, error) {
	dev, err := d.Get(id)
	if err != nil {
		return Device{}, err
	}
	if dev.Status != "active" {
		return Device{}, ErrRetired
	}
	_, err = d.db.Exec(`UPDATE devices SET endpoint = ?, last_seen = ? WHERE id = ?`,
		endpoint, now.UTC().Format(time.RFC3339), id)
	if err != nil {
		return Device{}, err
	}
	return d.Get(id)
}

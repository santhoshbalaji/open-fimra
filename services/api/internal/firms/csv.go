package firms

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

const Source = "firms"

var Products = [...]string{"VIIRS_SNPP_NRT", "VIIRS_NOAA20_NRT", "VIIRS_NOAA21_NRT", "MODIS_NRT"}

type Observation struct {
	Latitude    float64           `json:"latitude"`
	Longitude   float64           `json:"longitude"`
	ObservedAt  time.Time         `json:"observed_at"`
	Satellite   string            `json:"satellite"`
	Instrument  string            `json:"instrument"`
	ScanMetres  *float64          `json:"scan_m"`
	TrackMetres *float64          `json:"track_m"`
	Brightness  *float64          `json:"brightness_k"`
	FRP         *float64          `json:"frp_mw"`
	Confidence  string            `json:"confidence"`
	DayNight    string            `json:"day_night"`
	Raw         map[string]string `json:"raw"`
}

func Parse(input io.Reader, product string) ([]Observation, error) {
	instrument, brightness := "VIIRS", "bright_ti4"
	switch product {
	case "VIIRS_SNPP_NRT", "VIIRS_NOAA20_NRT", "VIIRS_NOAA21_NRT":
	case "MODIS_NRT":
		instrument, brightness = "MODIS", "brightness"
	default:
		return nil, errors.New("unknown FIRMS product")
	}

	reader := csv.NewReader(input)
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read FIRMS header: %w", err)
	}
	columns := make(map[string]int, len(header))
	for i, name := range header {
		name = strings.TrimSpace(strings.TrimPrefix(name, "\ufeff"))
		if _, ok := columns[name]; ok {
			return nil, errors.New("duplicate FIRMS column")
		}
		columns[name] = i
		header[i] = name
	}
	for _, name := range []string{"latitude", "longitude", "acq_date", "acq_time", "satellite", "scan", "track", brightness, "frp", "confidence", "daynight"} {
		if _, ok := columns[name]; !ok {
			return nil, fmt.Errorf("missing FIRMS column %s", name)
		}
	}

	var observations []Observation
	for row := 2; ; row++ {
		values, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return observations, nil
		}
		if err != nil {
			return observations, fmt.Errorf("read FIRMS row %d: %w", row, err)
		}
		raw := make(map[string]string, len(header))
		for i, name := range header {
			raw[name] = strings.TrimSpace(values[i])
		}
		observation, err := parseObservation(raw, instrument, brightness)
		if err != nil {
			return observations, fmt.Errorf("parse FIRMS row %d: %w", row, err)
		}
		observations = append(observations, observation)
	}
}

func parseObservation(raw map[string]string, instrument, brightness string) (Observation, error) {
	var o Observation
	o.Raw, o.Instrument = raw, instrument
	latitude, err := number(raw["latitude"], 64)
	if err != nil {
		return o, fmt.Errorf("invalid latitude: %w", err)
	}
	if latitude < -90 || latitude > 90 {
		return o, errors.New("latitude must be between -90 and 90")
	}
	longitude, err := number(raw["longitude"], 64)
	if err != nil {
		return o, fmt.Errorf("invalid longitude: %w", err)
	}
	if longitude < -180 || longitude > 180 {
		return o, errors.New("longitude must be between -180 and 180")
	}
	if latitude != 0 {
		o.Latitude = latitude
	}
	if longitude != 0 {
		o.Longitude = longitude
	}

	// Some exports leave out leading zeroes from the UTC time.
	hhmm := raw["acq_time"]
	if len(hhmm) == 0 || len(hhmm) > 4 || strings.Trim(hhmm, "0123456789") != "" {
		return o, errors.New("acq_time must be a UTC time in HHMM format")
	}
	o.ObservedAt, err = time.Parse("2006-01-02 1504", raw["acq_date"]+" "+strings.Repeat("0", 4-len(hhmm))+hhmm)
	if err != nil {
		return o, fmt.Errorf("invalid acquisition time: %w", err)
	}
	o.Satellite, o.Confidence, o.DayNight = raw["satellite"], raw["confidence"], raw["daynight"]
	if o.Satellite == "" {
		return o, errors.New("missing satellite")
	}
	if o.DayNight != "" && o.DayNight != "D" && o.DayNight != "N" {
		return o, errors.New("daynight must be D or N")
	}

	for _, field := range []struct {
		column string
		scale  float64
		dst    **float64
	}{
		{"scan", 1000, &o.ScanMetres}, {"track", 1000, &o.TrackMetres},
		{brightness, 1, &o.Brightness}, {"frp", 1, &o.FRP},
	} {
		if raw[field.column] == "" {
			continue
		}
		value, err := number(raw[field.column], 32)
		if err != nil {
			return o, fmt.Errorf("invalid %s: %w", field.column, err)
		}
		value *= field.scale
		if math.Abs(value) > math.MaxFloat32 {
			return o, fmt.Errorf("%s is out of range", field.column)
		}
		*field.dst = &value
	}
	return o, nil
}

func number(s string, bits int) (float64, error) {
	value, err := strconv.ParseFloat(s, bits)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, errors.New("expected a finite number")
	}
	return value, nil
}

func (o Observation) SourceKey(source, product string) string {
	return fmt.Sprintf("%s:%s:%s:%s:%s:%s", source, product,
		strconv.FormatFloat(o.Latitude, 'f', -1, 64), strconv.FormatFloat(o.Longitude, 'f', -1, 64),
		o.ObservedAt.UTC().Format("2006-01-02:1504"), o.Satellite)
}

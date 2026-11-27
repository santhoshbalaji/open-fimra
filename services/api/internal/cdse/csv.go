package cdse

import (
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

// sensingSlack allows pixel times just outside a granule's catalogue sensing
// interval. Near-real-time granules have been seen with pixels about four
// seconds after the catalogue end; anything far outside is still rejected.
const sensingSlack = 2 * time.Minute

func Attribution(observed time.Time) string {
	return fmt.Sprintf("Contains modified Copernicus Sentinel data %d", observed.UTC().Year())
}

func Parse(input io.Reader, granule Granule, etag string) ([]firms.Observation, error) {
	name := namePattern.FindStringSubmatch(granule.Name)
	if name == nil || granule.ContentDate.Start.IsZero() || granule.ContentDate.End.Before(granule.ContentDate.Start) || granule.PublicationDate.IsZero() {
		return nil, errors.New("invalid CDSE granule metadata")
	}
	buffer := bufio.NewReader(input)
	metadata := make(map[string]string)
	for {
		next, err := buffer.Peek(1)
		if err != nil {
			return nil, errors.New("missing CDSE fire CSV header")
		}
		if next[0] != '#' {
			break
		}
		line, err := buffer.ReadSlice('\n')
		if err != nil {
			return nil, errors.New("invalid CDSE fire CSV metadata")
		}
		if key, value, ok := strings.Cut(string(line[1:]), "="); ok {
			metadata[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}

	reader := csv.NewReader(buffer)
	header, err := reader.Read()
	if err != nil {
		return nil, errors.New("invalid CDSE fire CSV header")
	}
	columns := make(map[string]bool, len(header))
	for i, cell := range header {
		key := strings.TrimSpace(cell)
		if key == "" || columns[key] {
			return nil, errors.New("invalid CDSE fire CSV column")
		}
		header[i] = key
		columns[key] = true
	}
	for _, key := range []string{"lat(deg)", "lon(deg)", "day", "time", "D/N", "FRP(MW)", "MWIR_BT(K)",
		"confidence(%)", "confidence_class(lower=0;nominal=1;higher=2)", "used_channel", "actrack(km)", "altrack(km)", "satellite"} {
		if !columns[key] {
			return nil, fmt.Errorf("missing CDSE fire CSV column %s", key)
		}
	}

	var observations []firms.Observation
	for row := 1; ; row++ {
		values, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid CDSE fire CSV record %d", row)
		}
		raw := make(map[string]string, len(header)+10)
		for i, column := range header {
			raw[column] = strings.TrimSpace(values[i])
		}
		o, err := observation(raw, name[1], name[2])
		if err != nil {
			return nil, fmt.Errorf("CDSE fire CSV record %d: %w", row, err)
		}
		// Whole-second CSV times may be truncated from fractional-second times,
		// and pixel times can fall a few seconds outside the catalogue's sensing
		// interval at granule edges.
		if o.ObservedAt.Before(granule.ContentDate.Start.Truncate(time.Second).Add(-sensingSlack)) ||
			o.ObservedAt.After(granule.ContentDate.End.Add(sensingSlack)) {
			return nil, fmt.Errorf("CDSE fire CSV record %d is outside the sensing interval", row)
		}
		raw["native_id"] = o.NativeID
		raw["catalogue_id"] = granule.ID
		raw["granule"] = granule.Name
		raw["object_key"] = strings.TrimPrefix(granule.S3Path, "/eodata/") + "/" + File
		raw["etag"] = etag
		raw["sensing_start"] = granule.ContentDate.Start.UTC().Format(time.RFC3339Nano)
		raw["sensing_end"] = granule.ContentDate.End.UTC().Format(time.RFC3339Nano)
		raw["published_at"] = granule.PublicationDate.UTC().Format(time.RFC3339Nano)
		raw["processing_baseline"] = metadata["processing_baseline"]
		raw["attribution"] = Attribution(o.ObservedAt)
		observations = append(observations, o)
	}
	if count, present := metadata["number_of_detected_fires"]; present {
		reported, err := strconv.Atoi(count)
		if err != nil || reported != len(observations) {
			return nil, errors.New("CDSE fire CSV record count differs from the header")
		}
	}
	return observations, nil
}

func observation(raw map[string]string, satellite, granuleStart string) (firms.Observation, error) {
	o := firms.Observation{Raw: raw, Satellite: satellite, Instrument: "SLSTR", DayNight: raw["D/N"]}
	if raw["satellite"] != satellite || (o.DayNight != "D" && o.DayNight != "N") {
		return o, errors.New("invalid satellite or day/night flag")
	}
	var err error
	o.ObservedAt, err = time.Parse("2006-01-02 15:04:05", raw["day"]+" "+raw["time"])
	if err != nil {
		return o, errors.New("invalid acquisition time")
	}
	for _, field := range []struct {
		name     string
		min, max float64
		dst      *float64
	}{
		{"lat(deg)", -90, 90, &o.Latitude},
		{"lon(deg)", -180, 180, &o.Longitude},
	} {
		if *field.dst, err = number(raw[field.name], field.min, field.max); err != nil {
			return o, fmt.Errorf("invalid %s", field.name)
		}
	}
	for _, field := range []struct {
		name  string
		scale float64
		dst   **float64
	}{
		{"FRP(MW)", 1, &o.FRP},
		{"MWIR_BT(K)", 1, &o.Brightness},
		{"actrack(km)", 1000, &o.ScanMetres},
		{"altrack(km)", 1000, &o.TrackMetres},
	} {
		value, err := number(raw[field.name], 0, math.MaxFloat32/field.scale)
		if err != nil {
			return o, fmt.Errorf("invalid %s", field.name)
		}
		value *= field.scale
		*field.dst = &value
	}
	confidence, err := number(raw["confidence(%)"], 0, 100)
	if err != nil {
		return o, errors.New("invalid confidence percentage")
	}
	o.Confidence = strconv.FormatFloat(confidence, 'f', -1, 64)
	// The CSV contains no pixel indices. Use its native time and coordinates,
	// ignoring processing time, collection version and CSV row order.
	o.NativeID = fmt.Sprintf("%s:%s:%s:%.6f:%.6f", satellite, granuleStart,
		o.ObservedAt.Format(time.RFC3339Nano), o.Latitude, o.Longitude)
	return o, nil
}

func number(raw string, min, max float64) (float64, error) {
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(n) || n < min || n > max {
		return 0, errors.New("invalid numeric value")
	}
	return n + 0, nil // Canonicalize negative zero for the native ID.
}

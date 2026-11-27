package nodd

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/batchatco/go-native-netcdf/netcdf"
	"github.com/batchatco/go-native-netcdf/netcdf/api"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

type bounds = firms.Bounds

func contains(box bounds, lon, lat float64) bool {
	return lon >= box.West && lon <= box.East && lat >= box.South && lat <= box.North
}

func intersects(a, b bounds) bool {
	return a.West <= b.East && a.East >= b.West && a.South <= b.North && a.North >= b.South
}

var polygonWKT = regexp.MustCompile(`(?i)^POLYGON\s*\(\s*\(([0-9 .,eE+\-]+)\)\s*\)$`)

func footprint(attrs api.AttributeMap) *bounds {
	value, _ := attrs.Get("geospatial_bounds")
	wkt, _ := value.(string)
	match := polygonWKT.FindStringSubmatch(strings.TrimSpace(wkt))
	if match == nil {
		return nil
	}
	box := bounds{West: 180, South: 90, East: -180, North: -90}
	vertices := strings.Split(match[1], ",")
	if len(vertices) < 4 {
		return nil
	}
	for _, vertex := range vertices {
		xy := strings.Fields(vertex)
		if len(xy) != 2 {
			return nil
		}
		lon, xerr := strconv.ParseFloat(xy[0], 64)
		lat, yerr := strconv.ParseFloat(xy[1], 64)
		if xerr != nil || yerr != nil || math.Abs(lon) > 180 || math.Abs(lat) > 90 {
			return nil
		}
		box.West, box.East = min(box.West, lon), max(box.East, lon)
		box.South, box.North = min(box.South, lat), max(box.North, lat)
	}
	// The coordinates are rounded and the edges are sampled. For polar or
	// dateline-crossing swaths, fall back to the actual fire points.
	if box.East-box.West >= 180 || box.North > 65 || box.South < -65 {
		return nil
	}
	const padding = 0.25
	return &bounds{West: box.West - padding, East: box.East + padding, South: box.South - padding, North: box.North + padding}
}

type decoded struct {
	box    *bounds
	pixels []firms.Observation
	// A swath outside the current areas may need decoding if the areas change.
	pixelsRead bool
}

func readPixels(file api.ReadSeekerCloser, g granule, sat Satellite, boxes []bounds) (data decoded, err error) {
	// Isolate any parser panic to the bad granule.
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("NetCDF parser: %v", p)
		}
	}()
	group, err := netcdf.New(file)
	if err != nil {
		return decoded{}, err
	}
	defer group.Close()
	data.box = footprint(group.Attributes())
	if !data.covers(boxes) {
		return data, nil
	}
	data.pixelsRead = true
	lat, err := group.GetVarGetter("FP_Latitude")
	if err != nil {
		return decoded{}, err
	}
	dims := lat.Dimensions()
	if len(dims) != 1 || dims[0] != "nfire" || lat.Len() < 0 || lat.Len() > 20_000 {
		return decoded{}, fmt.Errorf("invalid fire pixel dimension: %v, %d pixels", dims, lat.Len())
	}
	count := int(lat.Len())
	if count == 0 {
		return data, nil
	}
	netCDFID, _ := group.Attributes().Get("id")
	id, _ := netCDFID.(string)
	columns := make(map[string][]string)
	for _, name := range group.ListVariables() {
		if !strings.HasPrefix(name, "FP_") {
			continue
		}
		v, err := group.GetVarGetter(name)
		if err != nil {
			return decoded{}, err
		}
		dims := v.Dimensions()
		if len(dims) != 1 || dims[0] != "nfire" || v.Len() != int64(count) {
			return decoded{}, fmt.Errorf("unexpected dimensions for %s", name)
		}
		column, err := pixelColumn(v)
		if err != nil {
			return decoded{}, fmt.Errorf("read %s: %w", name, err)
		}
		columns[name] = column
	}
	for _, name := range []string{"FP_Latitude", "FP_Longitude", "FP_Line", "FP_Sample"} {
		if columns[name] == nil {
			return decoded{}, fmt.Errorf("missing %s", name)
		}
	}
	for i := range count {
		raw := make(map[string]string, len(columns)+8)
		for name, column := range columns {
			raw[name] = column[i]
		}
		lat, lon := nativeNumber(raw["FP_Latitude"]), nativeNumber(raw["FP_Longitude"])
		line, lerr := strconv.Atoi(raw["FP_Line"])
		sample, serr := strconv.Atoi(raw["FP_Sample"])
		if lat == nil || lon == nil || math.Abs(*lat) > 90 || math.Abs(*lon) > 180 ||
			lerr != nil || serr != nil || line < 0 || sample < 0 {
			return decoded{}, fmt.Errorf("invalid location or index for fire pixel %d", i)
		}
		raw["granule_id"], raw["netcdf_id"], raw["pixel_index"] = g.ID, id, strconv.Itoa(i)
		raw["object_key"], raw["object_etag"] = g.Key, g.ETag
		raw["granule_start"], raw["granule_end"] = g.Start.Format(timestamp), g.End.Format(timestamp)
		raw["object_last_modified"] = g.Modified.Format(timestamp)
		pixel := firms.Observation{
			NativeID: fmt.Sprintf("%s:%d:%d", g.ID, line, sample),
			Latitude: *lat, Longitude: *lon, ObservedAt: g.Start,
			Satellite: strings.ToUpper(sat.Code), Instrument: "VIIRS",
			FRP: nativeNumber(raw["FP_FireRadiativePower"]), Brightness: nativeNumber(raw["FP_BT4um"]),
			Confidence: raw["FP_Confidence"], Raw: raw,
		}
		switch raw["FP_Day"] {
		case "1":
			pixel.DayNight = "D"
		case "0":
			pixel.DayNight = "N"
		}
		// NOAA gives the area of each pixel, so both dimensions are the
		// equivalent square width. The original area is retained in raw.
		if area := nativeNumber(raw["FP_PixelArea"]); area != nil && *area > 0 {
			width := 1000 * math.Sqrt(*area)
			pixel.ScanMetres, pixel.TrackMetres = &width, &width
		}
		data.pixels = append(data.pixels, pixel)
	}
	return data, nil
}

const timestamp = "2006-01-02T15:04:05.999999999Z07:00"

func (d decoded) covers(boxes []bounds) bool {
	for _, box := range boxes {
		if d.box == nil || intersects(*d.box, box) {
			return true
		}
	}
	return false
}

func nativeNumber(s string) *float64 {
	f, err := strconv.ParseFloat(s, 32)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return &f
}

// Only the sparse record uses these three numeric types in the operational
// files. Fill values become empty fields in the raw record.
func pixelColumn(v api.VarGetter) ([]string, error) {
	value, err := v.Values()
	if err != nil {
		return nil, err
	}
	fill, _ := v.Attributes().Get("_FillValue")
	out := make([]string, int(v.Len()))
	switch values := value.(type) {
	case []int16:
		for i, n := range values {
			if any(n) != fill {
				out[i] = strconv.FormatInt(int64(n), 10)
			}
		}
	case []int32:
		for i, n := range values {
			if any(n) != fill {
				out[i] = strconv.FormatInt(int64(n), 10)
			}
		}
	case []float32:
		for i, n := range values {
			if any(n) != fill && !math.IsNaN(float64(n)) && !math.IsInf(float64(n), 0) {
				out[i] = strconv.FormatFloat(float64(n), 'g', -1, 32)
			}
		}
	default:
		return nil, fmt.Errorf("unexpected pixel array %T", value)
	}
	return out, nil
}

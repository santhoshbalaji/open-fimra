package lsasaf

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
	"github.com/scigolib/hdf5"
)

type Pixel struct {
	Observation  firms.Observation
	Line, Column int
	Footprint    [5][2]float64
}

func (p Pixel) Geometry() json.RawMessage {
	encoded, _ := json.Marshal(struct {
		Type        string           `json:"type"`
		Coordinates [1][5][2]float64 `json:"coordinates"`
	}{"Polygon", [1][5][2]float64{p.Footprint}})
	return encoded
}

type column struct {
	values                 []float64
	scale, offset, missing float64
}

func (c column) at(i int) float64 {
	value := c.values[i]
	if value == c.missing {
		return math.NaN()
	}
	return value/c.scale + c.offset
}

func decode(path string, slot time.Time, keep func(Pixel) bool) ([]Pixel, error) {
	f, err := hdf5.Open(path)
	if err != nil {
		return nil, errors.New("invalid LSA SAF HDF5 header")
	}
	defer f.Close()
	f.SetReadLimit(16 << 20)
	attrs, err := f.Root().Attributes()
	if err != nil {
		return nil, errors.New("invalid LSA SAF scan metadata")
	}
	attributes := make(map[string]string)
	for _, attr := range attrs {
		switch attr.Name {
		case "IMAGE_ACQUISITION_TIME", "OVERALL_QUALITY_FLAG", "PRODUCT_ALGORITHM_VERSION",
			"NOMINAL_PRODUCT_TIME", "PROJECTION_NAME", "NL", "COFF", "LOFF", "CFAC", "LFAC":
			value, err := attr.ReadValue()
			if err != nil {
				return nil, errors.New("could not decode LSA SAF scan metadata")
			}
			attributes[attr.Name] = strings.TrimSpace(fmt.Sprint(value))
		}
	}
	if attributes["IMAGE_ACQUISITION_TIME"] != slot.Format(stamp+"05") ||
		attributes["PROJECTION_NAME"] != "GEOS(+045.5)" || attributes["OVERALL_QUALITY_FLAG"] != "OK" {
		return nil, errors.New("LSA SAF scan time, projection or overall quality failed validation")
	}
	count, err := strconv.Atoi(attributes["NL"])
	if err != nil || count < 0 || count > 100_000 {
		return nil, errors.New("invalid LSA SAF pixel count")
	}
	if count == 0 {
		return nil, nil
	}
	nav := navigation{}
	for name, dst := range map[string]*float64{"COFF": &nav.coff, "LOFF": &nav.loff, "CFAC": &nav.cfac, "LFAC": &nav.lfac} {
		value, err := strconv.Atoi(attributes[name])
		if err != nil || value <= 0 {
			return nil, errors.New("invalid LSA SAF grid navigation")
		}
		*dst = float64(value)
	}
	datasets := make(map[string]*hdf5.Dataset)
	for _, obj := range f.Root().Children() {
		if dataset, ok := obj.(*hdf5.Dataset); ok {
			datasets[dataset.Name()] = dataset
		}
	}
	columns := make(map[string]column, len(datasets))
	for _, name := range []string{"ABS_LINE", "ABS_PIXEL", "LATITUDE", "LONGITUDE", "FRP", "FIRE_CONFIDENCE", "PIXEL_SIZE", "BT_MIR"} {
		c, err := readColumn(datasets[name], count)
		if err != nil {
			return nil, fmt.Errorf("invalid LSA SAF %s column", name)
		}
		columns[name] = c
	}
	var pixels []Pixel
	for i := 0; i < count; i++ {
		lat, lon := columns["LATITUDE"].at(i), columns["LONGITUDE"].at(i)
		line, col := columns["ABS_LINE"].at(i), columns["ABS_PIXEL"].at(i)
		if !(lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 &&
			line >= 1 && line <= 3712 && col >= 1 && col <= 3712) {
			continue
		}
		footprint, ok := nav.footprint(col, line, lon, lat)
		if !ok {
			continue
		}
		pixel := Pixel{
			Line: int(line), Column: int(col), Footprint: footprint,
			Observation: firms.Observation{
				NativeID: fmt.Sprintf("%s:%d:%d", slot.Format(stamp), int(line), int(col)),
				Latitude: lat, Longitude: lon, ObservedAt: slot,
				Satellite: "Meteosat-9", Instrument: "SEVIRI",
				FRP:        measurement(columns["FRP"].at(i)),
				Brightness: measurement(columns["BT_MIR"].at(i)),
			},
		}
		if confidence := measurement(columns["FIRE_CONFIDENCE"].at(i)); confidence != nil && *confidence <= 1 {
			pixel.Observation.Confidence = strconv.FormatFloat(*confidence*100, 'f', 0, 64)
		}
		if area := measurement(columns["PIXEL_SIZE"].at(i)); area != nil && *area > 0 {
			width := 1000 * math.Sqrt(*area)
			pixel.Observation.ScanMetres, pixel.Observation.TrackMetres = &width, &width
		}
		if keep != nil && !keep(pixel) {
			continue
		}
		raw := make(map[string]string, len(datasets)+len(attributes)+1)
		for key, value := range attributes {
			raw[key] = value
		}
		raw["product_file"] = filePrefix + slot.Format(stamp)
		// Load ancillary columns lazily, only if a workspace can use the record.
		if len(pixels) == 0 {
			for name, dataset := range datasets {
				if _, exists := columns[name]; !exists {
					c, err := readColumn(dataset, count)
					if err != nil {
						return nil, errors.New("invalid ancillary LSA SAF column")
					}
					columns[name] = c
				}
			}
		}
		for key, c := range columns {
			raw[key] = strconv.FormatFloat(c.values[i], 'f', -1, 64)
		}
		pixel.Observation.Raw = raw
		pixels = append(pixels, pixel)
	}
	return pixels, nil
}

func readColumn(d *hdf5.Dataset, count int) (column, error) {
	var c column
	if d == nil {
		return c, errors.New("missing column")
	}
	shape, err := d.Shape()
	if err != nil || len(shape) != 1 || shape[0] != uint64(count) {
		return c, errors.New("invalid column shape")
	}
	c.values, err = d.Read()
	if err != nil || len(c.values) != count {
		return c, errors.New("invalid column data")
	}
	for name, dst := range map[string]*float64{"SCALING_FACTOR": &c.scale, "OFFSET": &c.offset, "MISSING_VALUE": &c.missing} {
		attribute, err := d.ReadAttribute(name)
		if err != nil {
			return c, err
		}
		value, err := strconv.ParseFloat(fmt.Sprint(attribute), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return c, errors.New("invalid column metadata")
		}
		*dst = value
	}
	if c.scale <= 0 {
		return c, errors.New("invalid scale factor")
	}
	return c, nil
}

func measurement(value float64) *float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return nil
	}
	return &value
}

type navigation struct{ coff, loff, cfac, lfac float64 }

func (n navigation) footprint(col, line, lon, lat float64) ([5][2]float64, bool) {
	var ring [5][2]float64
	centre, ok := n.inverse(col, line)
	if !ok || math.Abs(centre[0]-lon) > .03 || math.Abs(centre[1]-lat) > .03 {
		return ring, false
	}
	for i, delta := range [4][2]float64{{-.5, .5}, {.5, .5}, {.5, -.5}, {-.5, -.5}} {
		ring[i], ok = n.inverse(col+delta[0], line+delta[1])
		if !ok {
			return ring, false
		}
	}
	ring[4] = ring[0]
	return ring, true
}

// Invert the MSG fixed-grid projection at a point on the scan-angle grid.
func (n navigation) inverse(col, line float64) ([2]float64, bool) {
	const (
		rad   = math.Pi / 180
		a     = 6378.169
		b     = 6356.5838
		h     = 42164.0
		ratio = a * a / (b * b)
	)
	x := (col - n.coff) * 65536 / n.cfac * rad
	y := (n.loff - line) * 65536 / n.lfac * rad
	sx, cx := math.Sincos(x)
	sy, cy := math.Sincos(y)
	c := cx * cy
	k := cy*cy + ratio*sy*sy
	discriminant := h*h*c*c - k*(h*h-a*a)
	if discriminant < 0 {
		return [2]float64{}, false
	}
	d := (h*c - math.Sqrt(discriminant)) / k
	s1, s2, s3 := h-d*c, d*sx*cy, d*sy
	return [2]float64{45.5 + math.Atan2(s2, s1)/rad, math.Atan2(ratio*s3, math.Hypot(s1, s2)) / rad}, true
}

package main

import (
	"encoding/binary"
	"math"
	"testing"
)

// Read just the MVT layer and property fields needed to check the HTTP response.
func tileFeatures(t *testing.T, tile []byte) []map[string]any {
	t.Helper()
	layers := decodeTileLayers(t, tile)
	features, ok := layers["detections"]
	if !ok || len(layers) != 1 {
		t.Fatalf("expected a detection layer, got %v", layers)
	}
	return features
}

func decodeTileLayers(t *testing.T, tile []byte) map[string][]map[string]any {
	t.Helper()
	layers := make(map[string][]map[string]any)
	readMVTFields(t, tile, func(n uint64, b []byte, _ uint64) {
		if n == 3 {
			name, features := decodeTileLayer(t, b)
			layers[name] = features
		}
	})
	return layers
}

func decodeTileLayer(t *testing.T, layer []byte) (string, []map[string]any) {
	t.Helper()
	var name string
	var keys []string
	var values []any
	var tags [][]byte
	readMVTFields(t, layer, func(n uint64, b []byte, _ uint64) {
		switch n {
		case 1:
			name = string(b)
		case 2:
			readMVTFields(t, b, func(n uint64, b []byte, _ uint64) {
				if n == 2 {
					tags = append(tags, b)
				}
			})
		case 3:
			keys = append(keys, string(b))
		case 4:
			readMVTFields(t, b, func(n uint64, b []byte, v uint64) {
				switch n {
				case 1:
					values = append(values, string(b))
				case 2:
					values = append(values, math.Float32frombits(uint32(v)))
				case 3:
					values = append(values, math.Float64frombits(v))
				case 4, 5:
					values = append(values, v)
				default:
					t.Fatalf("unexpected tile value type %d", n)
				}
			})
		}
	})

	features := make([]map[string]any, 0, len(tags))
	for _, b := range tags {
		properties := make(map[string]any)
		for len(b) != 0 {
			key, rest := takeMVTVarint(t, b)
			value, rest := takeMVTVarint(t, rest)
			if key >= uint64(len(keys)) || value >= uint64(len(values)) {
				t.Fatal("invalid tile property")
			}
			properties[keys[key]] = values[value]
			b = rest
		}
		features = append(features, properties)
	}
	return name, features
}

func readMVTFields(t *testing.T, b []byte, field func(uint64, []byte, uint64)) {
	t.Helper()
	for len(b) != 0 {
		tag, rest := takeMVTVarint(t, b)
		b = rest
		n := tag >> 3
		switch tag & 7 {
		case 0:
			value, rest := takeMVTVarint(t, b)
			field(n, nil, value)
			b = rest
		case 1:
			if len(b) < 8 {
				t.Fatal("truncated double")
			}
			field(n, nil, binary.LittleEndian.Uint64(b))
			b = b[8:]
		case 2:
			size, rest := takeMVTVarint(t, b)
			if uint64(len(rest)) < size {
				t.Fatal("truncated byte field")
			}
			field(n, rest[:size], 0)
			b = rest[size:]
		case 5:
			if len(b) < 4 {
				t.Fatal("truncated float")
			}
			field(n, nil, uint64(binary.LittleEndian.Uint32(b)))
			b = b[4:]
		default:
			t.Fatalf("unsupported tile wire type %d", tag&7)
		}
	}
}

func takeMVTVarint(t *testing.T, b []byte) (uint64, []byte) {
	t.Helper()
	value, size := binary.Uvarint(b)
	if size <= 0 {
		t.Fatal("invalid tile varint")
	}
	return value, b[size:]
}

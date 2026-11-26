import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:geolocator/geolocator.dart';
import 'package:maplibre_gl/maplibre_gl.dart';

import '../data/field_store.dart';
import '../map/map_style.dart';
import '../theme/app_theme.dart';
import '../ui/alert_detail.dart';

class GeometryField extends StatelessWidget {
  const GeometryField({
    super.key,
    required this.label,
    required this.hint,
    required this.kind,
    required this.value,
    required this.anchor,
    required this.reference,
    required this.maxAccuracy,
    required this.changed,
  });
  final String label;
  final String hint;
  final String kind;
  final Json? value;
  final Json anchor;
  final Json? reference;
  final num? maxAccuracy;
  final ValueChanged<Json?>? changed;

  @override
  Widget build(BuildContext context) {
    final coordinates = value?['coordinates'] as List?;
    final summary = switch (kind) {
      'point' when coordinates != null => '${coordinates[1]}, ${coordinates[0]}',
      'track' when coordinates != null => '${coordinates.length} points',
      'polygon' when coordinates != null => '${(coordinates.first as List).length - 1} corners',
      _ => 'No location saved',
    };
    Future<void> open() async {
      final result = await Navigator.of(context).push<Json>(
        MaterialPageRoute(
          builder: (_) => _GeometryEditor(
            kind: kind,
            title: label,
            initial: value,
            reference: reference,
            anchor: anchor,
            maxAccuracy: maxAccuracy,
          ),
        ),
      );
      if (result != null && context.mounted) changed?.call(result);
    }

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(label),
        if (hint.isNotEmpty) Text(hint),
        Text(summary),
        Wrap(
          spacing: 8,
          children: [
            OutlinedButton.icon(
              onPressed: changed == null ? null : open,
              icon: const Icon(Icons.place_outlined),
              label: Text(switch (kind) {
                'track' => 'Record track',
                'polygon' => 'Draw area',
                _ => 'Set location',
              }),
            ),
            if (value != null)
              TextButton(onPressed: changed == null ? null : () => changed!(null), child: const Text('Clear')),
          ],
        ),
      ],
    );
  }
}

class _GeometryEditor extends StatefulWidget {
  const _GeometryEditor({
    required this.kind,
    required this.title,
    required this.initial,
    required this.reference,
    required this.anchor,
    required this.maxAccuracy,
  });
  final String kind;
  final String title;
  final Json? initial;
  final Json? reference;
  final Json anchor;
  final num? maxAccuracy;

  @override
  State<_GeometryEditor> createState() => _GeometryEditorState();
}

class _GeometryEditorState extends State<_GeometryEditor> {
  late final List<LatLng> _points = _initialPoints();
  MapLibreMapController? _map;
  StreamSubscription<Position>? _recording;
  bool _locating = false;
  bool _ready = false;
  String? _problem;

  bool get _point => widget.kind == 'point';
  bool get _polygon => widget.kind == 'polygon';

  LatLng _latLng(List<dynamic> coord) => LatLng((coord[1] as num).toDouble(), (coord[0] as num).toDouble());
  List<double> _coords(LatLng point) => [point.longitude, point.latitude];

  List<LatLng> _initialPoints() {
    final raw = widget.initial?['coordinates'] as List?;
    if (raw == null) return [];
    if (_point) return [_latLng(raw)];
    final rows = _polygon ? (raw.first as List).take((raw.first as List).length - 1) : raw;
    return rows.map((row) => _latLng(row as List)).toList();
  }

  Json? _geometry() {
    if (_points.isEmpty) return null;
    if (_point || _points.length == 1) return {'type': 'Point', 'coordinates': _coords(_points.first)};
    final rows = _points.map(_coords).toList();
    return _polygon && rows.length >= 3
        ? {
            'type': 'Polygon',
            'coordinates': [
              [...rows, rows.first],
            ],
          }
        : {'type': 'LineString', 'coordinates': rows};
  }

  Json _features() => {
    'type': 'FeatureCollection',
    'features': [
      if (_geometry() case final geometry?)
        {'type': 'Feature', 'geometry': geometry, 'properties': <String, dynamic>{}},
    ],
  };

  Future<void> _redraw() async {
    final map = _map;
    if (_ready && map != null) await map.setGeoJsonSource('shape', _features());
  }

  void _add(LatLng value) {
    setState(() {
      if (_point) _points.clear();
      if (_points.lastOrNull != value) _points.add(value);
      _problem = null;
    });
    unawaited(_redraw());
  }

  void _fix(Position fix, {bool centre = false}) {
    final max = widget.maxAccuracy;
    if (max != null && (fix.accuracy > max || !fix.accuracy.isFinite)) {
      setState(
        () => _problem = 'Location is only accurate to ${fix.accuracy.round()} m. Wait for a fix within $max m.',
      );
      return;
    }
    final point = LatLng(fix.latitude, fix.longitude);
    _add(point);
    if (centre) unawaited(_map?.animateCamera(CameraUpdate.newLatLng(point)));
  }

  Future<void> _gps({required bool record}) async {
    if (_locating) return;
    setState(() {
      _locating = true;
      _problem = null;
    });
    try {
      await ensureLocationPermission();
      if (!mounted) return;
      const settings = LocationSettings(accuracy: LocationAccuracy.high, distanceFilter: 4);
      if (record) {
        _recording = Geolocator.getPositionStream(locationSettings: settings).listen(
          (fix) {
            if (mounted) _fix(fix, centre: _points.isEmpty);
          },
          onError: (Object _) {
            if (mounted) {
              _stop();
              setState(() => _problem = 'Could not record your location. Try again.');
            }
          },
        );
      } else {
        final fix = await Geolocator.getCurrentPosition(
          locationSettings: const LocationSettings(accuracy: LocationAccuracy.high, timeLimit: Duration(seconds: 30)),
        );
        if (mounted) _fix(fix, centre: true);
      }
    } on LocationProblem catch (error) {
      if (mounted) setState(() => _problem = error.message);
    } on Exception {
      if (mounted) setState(() => _problem = 'Could not read your location. Try again.');
    } finally {
      if (mounted) setState(() => _locating = false);
    }
  }

  void _stop() {
    unawaited(_recording?.cancel());
    setState(() => _recording = null);
  }

  @override
  void dispose() {
    unawaited(_recording?.cancel());
    super.dispose();
  }

  late final String _style = jsonEncode(_mapStyle(FimraColours.of(context)));

  Json _mapStyle(FimraPalette colours) {
    final colour = hex(colours.brand);
    return {
      'version': 8,
      'sources': {
        if (widget.reference != null) 'reference': {'type': 'geojson', 'data': widget.reference},
        'shape': {'type': 'geojson', 'data': _features()},
      },
      'layers': [
        {
          'id': 'background',
          'type': 'background',
          'paint': {'background-color': hex(colours.mapBackground)},
        },
        if (widget.reference != null) ...[
          {
            'id': 'forest',
            'type': 'fill',
            'source': 'reference',
            'filter': [
              '==',
              ['get', 'kind'],
              'forest',
            ],
            'paint': {'fill-color': hex(colours.chart[2]), 'fill-opacity': 0.3},
          },
          {
            'id': 'units',
            'type': 'line',
            'source': 'reference',
            'filter': [
              '==',
              ['get', 'kind'],
              'units',
            ],
            'paint': {'line-color': hex(colours.textSecondary), 'line-width': 2},
          },
        ],
        {
          'id': 'area',
          'type': 'fill',
          'source': 'shape',
          'paint': {'fill-color': colour, 'fill-opacity': 0.35},
        },
        {
          'id': 'line',
          'type': 'line',
          'source': 'shape',
          'paint': {'line-color': colour, 'line-width': 3},
        },
        {
          'id': 'point',
          'type': 'circle',
          'source': 'shape',
          'filter': [
            '==',
            ['geometry-type'],
            'Point',
          ],
          'paint': {'circle-color': colour, 'circle-radius': 7},
        },
      ],
    };
  }

  @override
  Widget build(BuildContext context) {
    final anchor = widget.anchor['coordinates'];
    final centre = _points.firstOrNull ?? (anchor is List ? _latLng(anchor) : const LatLng(22, 79));
    final minimum = _point
        ? 1
        : _polygon
        ? 3
        : 2;
    final recording = _recording != null;
    return Scaffold(
      appBar: AppBar(title: const Text('Report map')),
      body: SafeArea(
        child: LayoutBuilder(
          builder: (context, constraints) => Column(
            children: [
              Expanded(
                child: MapLibreMap(
                  initialCameraPosition: CameraPosition(target: centre, zoom: 14),
                  styleString: _style,
                  onMapCreated: (map) => _map = map,
                  onStyleLoadedCallback: () {
                    _ready = true;
                    unawaited(_redraw());
                  },
                  onMapClick: (_, point) {
                    if (!recording && (_point || _polygon)) _add(point);
                  },
                  compassEnabled: false,
                  attributionEnabled: false,
                  rotateGesturesEnabled: false,
                  tiltGesturesEnabled: false,
                ),
              ),
              ConstrainedBox(
                constraints: BoxConstraints(maxHeight: constraints.maxHeight * 0.6),
                child: SingleChildScrollView(
                  padding: const EdgeInsets.all(12),
                  child: Column(
                    children: [
                      Semantics(
                        headingLevel: 1,
                        child: Text(widget.title, style: Theme.of(context).textTheme.titleMedium),
                      ),
                      Text(
                        _point
                            ? 'Tap the map or use your location.'
                            : _polygon
                            ? 'Tap the map to add corners, or use your location at each corner.'
                            : recording
                            ? 'Walk the route, then stop recording.'
                            : 'Record while walking or add points from your location.',
                      ),
                      Text('${_points.length} ${_polygon ? 'corners' : 'points'}'),
                      if (_problem != null) Semantics(liveRegion: true, child: Text(_problem!)),
                      Wrap(
                        spacing: 8,
                        children: [
                          if (!_point && !_polygon)
                            OutlinedButton(
                              onPressed: _locating
                                  ? null
                                  : recording
                                  ? _stop
                                  : () => _gps(record: true),
                              child: Text(recording ? 'Stop recording' : 'Start recording'),
                            ),
                          if (!recording)
                            OutlinedButton(
                              onPressed: _locating ? null : () => _gps(record: false),
                              child: Text(_point ? 'Use GPS' : 'Add GPS point'),
                            ),
                          if (_points.isNotEmpty && !recording)
                            TextButton(
                              onPressed: () {
                                setState(_points.removeLast);
                                unawaited(_redraw());
                              },
                              child: const Text('Undo'),
                            ),
                          FilledButton(
                            onPressed: recording || _locating || _points.length < minimum
                                ? null
                                : () => Navigator.pop(context, _geometry()),
                            child: const Text('Use shape'),
                          ),
                        ],
                      ),
                    ],
                  ),
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

import 'dart:ui';

import '../api/models.dart';
import '../data/field_store.dart';
import '../theme/tokens.g.dart';

/// Layer IDs that show detections, for finding the point under a tap.
const detectionLayers = ['detections-outside', 'detections-fixed', 'detections-near-edge', 'detections-inside'];

/// The image name for fixed heat sources, which are drawn as squares.
const squareImage = 'fixed-heat-source';

String hex(Color colour) => '#${(colour.toARGB32() & 0xFFFFFF).toRadixString(16).padLeft(6, '0')}';

/// A MapLibre style for one workspace. Every source is the core API's
/// permission-filtered vector tiles; the style loads nothing from outside
/// services, so it needs no licence key and no glyph server.
Map<String, Object?> mapStyle({
  required String referenceTiles,
  required String detectionTiles,
  required WorkspaceMap info,
  required FimraPalette palette,
  List<RoleAssignment> jurisdiction = const [],
  Json? offlineReference,
  List<Alert> offlineAlerts = const [],
}) {
  final p = palette;
  final saved = offlineReference != null;
  Map<String, Object> source(String kind) => saved ? {'source': kind} : {'source': 'reference', 'source-layer': kind};
  final units = source('units');
  final forest = source('forest');
  final exclusions = source('exclusions');
  final detections = {'source': 'detections', if (!saved) 'source-layer': 'detections'};
  final features = objects(offlineReference?['features']);
  Json collection(List<Json> rows) => {'type': 'FeatureCollection', 'features': rows};
  final now = DateTime.now();
  final ageColour = [
    'match',
    ['get', 'age'],
    0,
    hex(p.fireAge[0]),
    1,
    hex(p.fireAge[1]),
    2,
    hex(p.fireAge[2]),
    hex(p.fireAge[3]),
  ];
  final levels = [...info.levels]..sort((a, b) => b.depth.compareTo(a.depth));
  return {
    'version': 8,
    'name': 'Open FIMRA field map',
    'sources': {
      if (saved) ...{
        for (final kind in ['units', 'forest', 'exclusions'])
          kind: {
            'type': 'geojson',
            'data': collection(features.where((row) => object(row['properties'])['kind'] == kind).toList()),
          },
        'detections': {
          'type': 'geojson',
          'data': collection([
            for (final alert in offlineAlerts)
              {
                'type': 'Feature',
                'geometry': {
                  'type': 'Point',
                  'coordinates': [alert.longitude, alert.latitude],
                },
                'properties': {
                  'id': alert.detectionId,
                  'forest_class': alert.forestClass,
                  'age': switch (now.difference(alert.observedAt).inHours) {
                    < 6 => 0,
                    < 24 => 1,
                    < 72 => 2,
                    _ => 3,
                  },
                },
              },
          ]),
        },
      } else ...{
        'reference': {
          'type': 'vector',
          'tiles': [referenceTiles],
          'minzoom': 0,
          'maxzoom': 14,
        },
        'detections': {
          'type': 'vector',
          'tiles': [detectionTiles],
          'minzoom': 0,
          'maxzoom': 14,
        },
      },
    },
    'layers': [
      {
        'id': 'background',
        'type': 'background',
        'paint': {'background-color': hex(p.mapBackground)},
      },
      {
        'id': 'land',
        'type': 'fill',
        ...units,
        'filter': [
          '==',
          ['get', 'depth'],
          0,
        ],
        'paint': {'fill-color': hex(p.card)},
      },
      {
        'id': 'forest',
        'type': 'fill',
        ...forest,
        'paint': {'fill-color': hex(p.chart[2]), 'fill-opacity': 0.34},
      },
      {
        'id': 'forest-edge',
        'type': 'line',
        ...forest,
        'paint': {'line-color': hex(p.textSecondary), 'line-width': 1},
      },
      {
        'id': 'exclusions',
        'type': 'fill',
        ...exclusions,
        'paint': {'fill-color': hex(p.neutralMark)},
      },
      {
        'id': 'exclusion-edge',
        'type': 'line',
        ...exclusions,
        'paint': {'line-color': hex(p.text), 'line-width': 2},
      },
      for (final level in levels)
        {
          'id': 'unit-${level.hierarchy}-${level.depth}',
          'type': 'line',
          ...units,
          'filter': [
            'all',
            [
              '==',
              ['get', 'hierarchy'],
              level.hierarchy,
            ],
            [
              '==',
              ['get', 'depth'],
              level.depth,
            ],
          ],
          'paint': {
            'line-color': hex(
              level.depth == 0
                  ? p.text
                  : level.depth == 1
                  ? p.brand
                  : p.textSecondary,
            ),
            'line-width': 4 / (1 + level.depth * 0.65),
            'line-opacity': 1 / (1 + level.depth * 0.13),
            if (level.depth > 0) 'line-dasharray': [(9 - level.depth * 2).clamp(2, 9), 2],
          },
        },
      if (jurisdiction.isNotEmpty)
        {
          'id': 'my-area',
          'type': 'line',
          ...units,
          'filter': [
            'any',
            for (final grant in jurisdiction)
              [
                'all',
                [
                  '==',
                  ['get', 'hierarchy'],
                  grant.hierarchy,
                ],
                [
                  '==',
                  ['get', 'level_slug'],
                  grant.unit.levelSlug,
                ],
                [
                  '==',
                  ['get', 'code'],
                  grant.unit.code,
                ],
              ],
          ],
          'paint': {'line-color': hex(p.brand), 'line-width': 3.5},
        },
      {
        'id': 'detections-outside',
        'type': 'circle',
        ...detections,
        'filter': [
          '==',
          ['get', 'forest_class'],
          'outside',
        ],
        'paint': {'circle-radius': 3, 'circle-color': hex(p.neutralMark)},
      },
      {
        'id': 'detections-fixed',
        'type': 'symbol',
        ...detections,
        'filter': [
          '==',
          ['get', 'forest_class'],
          'fixed_heat_source',
        ],
        'layout': {'icon-image': squareImage, 'icon-allow-overlap': true, 'icon-ignore-placement': true},
      },
      {
        'id': 'detections-near-edge',
        'type': 'circle',
        ...detections,
        'filter': [
          '==',
          ['get', 'forest_class'],
          'near_edge',
        ],
        'paint': {
          'circle-radius': 6,
          'circle-color': 'rgba(0,0,0,0)',
          'circle-stroke-width': 2.5,
          'circle-stroke-color': ageColour,
        },
      },
      {
        'id': 'detections-inside',
        'type': 'circle',
        ...detections,
        'filter': [
          '==',
          ['get', 'forest_class'],
          'inside',
        ],
        'paint': {
          'circle-radius': 7,
          'circle-color': ageColour,
          'circle-stroke-width': 1.5,
          'circle-stroke-color': hex(p.card),
        },
      },
    ],
  };
}

/// The best place to start the map: the user's own units, then the
/// workspace boundary.
Bounds? jurisdictionBounds(List<RoleAssignment> grants, WorkspaceMap info) {
  final boxes = grants.map((g) => g.unit.bounds).nonNulls.toList();
  if (boxes.isEmpty) return info.boundary;
  return Bounds(
    boxes.map((b) => b.west).reduce((a, b) => a < b ? a : b),
    boxes.map((b) => b.south).reduce((a, b) => a < b ? a : b),
    boxes.map((b) => b.east).reduce((a, b) => a > b ? a : b),
    boxes.map((b) => b.north).reduce((a, b) => a > b ? a : b),
  );
}
